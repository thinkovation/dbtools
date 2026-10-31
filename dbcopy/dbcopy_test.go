package main

import (
	"context"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

func mustURL(t *testing.T, s string) *url.URL {
	t.Helper()
	u, err := url.Parse(s)
	if err != nil {
		t.Fatal(err)
	}
	return u
}

func TestSameDatabase(t *testing.T) {
	for _, tc := range []struct {
		a, b string
		same bool
	}{
		{"postgres://u@localhost/app", "postgres://other:pw@127.0.0.1:5432/app", true},
		{"postgres://u@localhost/app", "postgres://u@localhost/app_test", false},
		{"postgres://u@localhost:5432/app", "postgres://u@localhost:5433/app", false},
		{"postgres://u@db1/app", "postgres://u@db2/app", false},
		{"postgres://u@x/app?host=/tmp", "postgres://u@y/app?host=/tmp", true},
	} {
		if got := sameDatabase(mustURL(t, tc.a), mustURL(t, tc.b)); got != tc.same {
			t.Errorf("sameDatabase(%s, %s) = %v, want %v", tc.a, tc.b, got, tc.same)
		}
	}
}

func TestLibpqArgsHidesPassword(t *testing.T) {
	conn, env := libpqArgs(mustURL(t, "postgres://alice:s3cret@db:5432/app?sslmode=require"))
	if strings.Contains(conn, "s3cret") {
		t.Errorf("password leaked into connection string: %s", conn)
	}
	if conn != "postgres://alice@db:5432/app?sslmode=require" {
		t.Errorf("unexpected connection string %q", conn)
	}
	if env[len(env)-1] != "PGPASSWORD=s3cret" {
		t.Errorf("PGPASSWORD not set: %v", env[len(env)-1])
	}
}

func TestTempNameFitsIdentifierLimit(t *testing.T) {
	if n := tempName(strings.Repeat("x", 63)); len(n) > 63 {
		t.Errorf("temp name too long: %d bytes", len(n))
	}
	if got := tempName("app_test"); got != "app_test_dbcopy_tmp" {
		t.Errorf("tempName = %q", got)
	}
}

func TestParsePGURL(t *testing.T) {
	for _, bad := range []string{"", "mysql://u@h/db", "postgres://u@h", "postgres://u@h/"} {
		if _, err := parsePGURL(bad, "x"); err == nil {
			t.Errorf("parsePGURL(%q) should fail", bad)
		}
	}
}

func TestRestoreRefusesSystemDatabases(t *testing.T) {
	o := &Options{MaintenanceDB: "postgres", Log: io.Discard}
	for _, name := range []string{"postgres", "template0", "template1"} {
		err := Restore(context.Background(), "x.dump", mustURL(t, "postgres://u@h/"+name), o)
		if err == nil || !strings.Contains(err.Error(), "refusing") {
			t.Errorf("restore into %s: got %v, want refusal", name, err)
		}
	}
}

func TestCloneRefusesSameDatabase(t *testing.T) {
	u := mustURL(t, "postgres://u@h/app")
	err := Clone(context.Background(), u, u, "x.dump", &Options{Log: io.Discard})
	if err == nil || !strings.Contains(err.Error(), "same database") {
		t.Errorf("got %v, want same-database refusal", err)
	}
}

// TestPostgresIntegration needs a server where the user can create
// databases, e.g. DBCOPY_PG_DSN=postgres://postgres:postgres@localhost:5432/postgres?sslmode=disable
func TestPostgresIntegration(t *testing.T) {
	dsn := os.Getenv("DBCOPY_PG_DSN")
	if dsn == "" {
		t.Skip("DBCOPY_PG_DSN not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	base := mustURL(t, dsn)
	src, dst := withDB(base, "dbcopy_test_src"), withDB(base, "dbcopy_test_dst")
	admin, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close(ctx)
	cleanup := func() {
		for _, db := range []string{"dbcopy_test_src", "dbcopy_test_dst", tempName("dbcopy_test_dst")} {
			dropDatabase(context.Background(), admin, db)
		}
	}
	cleanup()
	t.Cleanup(cleanup)

	if _, err := admin.Exec(ctx, `CREATE DATABASE dbcopy_test_src`); err != nil {
		t.Fatal(err)
	}
	execSQL(t, ctx, src, `
		CREATE SCHEMA app;
		CREATE TABLE app.customers (id serial PRIMARY KEY, email text UNIQUE NOT NULL);
		CREATE TABLE public.orders (id serial PRIMARY KEY, customer_id int REFERENCES app.customers(id));
		INSERT INTO app.customers (email) VALUES ('a@x'), ('b@x');
		INSERT INTO public.orders (customer_id) VALUES (1), (1), (2);`)

	dir := t.TempDir()
	o := &Options{MaintenanceDB: "postgres", Jobs: 2, Log: io.Discard,
		Confirm: func(string, string) bool { t.Error("unexpected confirmation prompt"); return false }}

	// 1. Clone into a database that doesn't exist yet: no prompt needed.
	dump := filepath.Join(dir, "first.dump")
	if err := Clone(ctx, src, dst, dump, o); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Stat(dump); err != nil || fi.Mode().Perm() != 0o600 {
		t.Errorf("dump file missing or not private: %v %v", fi, err)
	}
	if n := count(t, ctx, dst, `SELECT count(*) FROM public.orders`); n != 3 {
		t.Errorf("orders after first clone = %d, want 3", n)
	}

	// 2. Clone again over the existing target while something is connected
	// to it. Without -yes the user must confirm; a wrong answer aborts.
	execSQL(t, ctx, src, `INSERT INTO app.customers (email) VALUES ('c@x')`)
	o.Confirm = func(_, want string) bool { return false }
	if err := Clone(ctx, src, dst, filepath.Join(dir, "x.dump"), o); err != errAborted {
		t.Fatalf("declined confirmation: got %v, want errAborted", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "x.dump")); !os.IsNotExist(err) {
		t.Error("declined clone should not have dumped the source")
	}
	held, err := pgx.Connect(ctx, dst.String())
	if err != nil {
		t.Fatal(err)
	}
	defer held.Close(context.Background())
	o.Confirm = func(_, want string) bool { return want == "dbcopy_test_dst" }
	if err := Clone(ctx, src, dst, filepath.Join(dir, "second.dump"), o); err != nil {
		t.Fatal(err)
	}
	if n := count(t, ctx, dst, `SELECT count(*) FROM app.customers`); n != 3 {
		t.Errorf("customers after re-clone = %d, want 3", n)
	}
	if held.Ping(ctx) == nil {
		t.Error("existing connection to the replaced database should have been terminated")
	}

	// 3. A failed restore leaves the existing target as it was.
	bad := filepath.Join(dir, "bad.dump")
	os.WriteFile(bad, []byte("not a dump"), 0o600)
	o.Yes = true
	if err := Restore(ctx, bad, dst, o); err == nil || !strings.Contains(err.Error(), "left unchanged") {
		t.Fatalf("restore of corrupt dump: got %v", err)
	}
	if n := count(t, ctx, dst, `SELECT count(*) FROM app.customers`); n != 3 {
		t.Errorf("customers after failed restore = %d, want 3 (target should be untouched)", n)
	}
	if ok, _ := databaseExists(ctx, admin, tempName("dbcopy_test_dst")); ok {
		t.Error("temporary database left behind after failed restore")
	}
}

func execSQL(t *testing.T, ctx context.Context, u *url.URL, sql string) {
	t.Helper()
	conn, err := pgx.Connect(ctx, u.String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(ctx)
	if _, err := conn.Exec(ctx, sql); err != nil {
		t.Fatal(err)
	}
}

func count(t *testing.T, ctx context.Context, u *url.URL, sql string) int {
	t.Helper()
	conn, err := pgx.Connect(ctx, u.String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(ctx)
	var n int
	if err := conn.QueryRow(ctx, sql).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}
