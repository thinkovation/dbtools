package dbinspect

import (
	"bytes"
	"context"
	"os"
	"strings"
	"testing"
	"time"
)

func fixture() *Schema {
	return &Schema{Dialect: "test", Name: "app", Tables: []*Table{
		{
			Name: "customers", Kind: "table", PrimaryKey: []string{"id"},
			Columns: []*Column{{Name: "id", Type: "bigint"}},
			Indexes: []*Index{{Name: "customers_pkey", Columns: []string{"id"}, Unique: true, Primary: true}},
		},
		{
			Name: "orders", Kind: "table", PrimaryKey: []string{"id"},
			Columns: []*Column{
				{Name: "id", Type: "bigint"},
				{Name: "customer_id", Type: "bigint", Nullable: true},
				{Name: "coupon_id", Type: "bigint"},
			},
			ForeignKeys: []*ForeignKey{{Name: "orders_customer_fk", Columns: []string{"customer_id"}, RefTable: "customers", RefColumns: []string{"id"}}},
			Indexes:     []*Index{{Name: "orders_pkey", Columns: []string{"id"}, Unique: true, Primary: true}},
		},
		{Name: "log", Kind: "table", Columns: []*Column{{Name: "msg", Type: "text"}}},
	}}
}

func checks(fs []Finding) map[string]string {
	out := map[string]string{}
	for _, f := range fs {
		out[f.Table+"/"+f.Check] = f.Message
	}
	return out
}

func TestAnalyze(t *testing.T) {
	got := checks(Analyze(fixture()))
	for _, want := range []string{
		"orders/unindexed-foreign-key",
		"orders/implicit-relationship", // coupon_id
		"log/missing-primary-key",
		"log/isolated-table",
	} {
		if _, ok := got[want]; !ok {
			t.Errorf("missing finding %s; got %v", want, got)
		}
	}
	if _, ok := got["customers/isolated-table"]; ok {
		t.Error("customers is referenced by orders and should not be isolated")
	}
	if len(got) != 4 {
		t.Errorf("got %d findings, want 4: %v", len(got), got)
	}
}

func TestIndexedFKNotFlagged(t *testing.T) {
	s := fixture()
	o := s.Table("orders")
	o.Indexes = append(o.Indexes, &Index{Name: "orders_customer_idx", Columns: []string{"customer_id", "id"}})
	if _, ok := checks(Analyze(s))["orders/unindexed-foreign-key"]; ok {
		t.Error("FK covered by composite index prefix should not be flagged")
	}
}

func TestMermaid(t *testing.T) {
	var buf bytes.Buffer
	if err := NewReport(fixture()).WriteMermaid(&buf); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	for _, want := range []string{"erDiagram", "bigint customer_id FK", `customers |o--o{ orders : "customer_id"`} {
		if !strings.Contains(out, want) {
			t.Errorf("mermaid output missing %q:\n%s", want, out)
		}
	}
}

// TestPostgresIntegration runs against a real database when DBINSPECT_PG_DSN
// is set, e.g. postgres://postgres@localhost:5432/postgres?sslmode=disable.
func TestPostgresIntegration(t *testing.T) {
	dsn := os.Getenv("DBINSPECT_PG_DSN")
	if dsn == "" {
		t.Skip("DBINSPECT_PG_DSN not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	db, insp, err := Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	ddl, err := os.ReadFile("testdata/postgres.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, string(ddl)); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Exec(`DROP SCHEMA dbinspect_test CASCADE`) })

	s, err := Inspect(ctx, db, insp, "dbinspect_test")
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Tables) != 5 {
		t.Fatalf("got %d tables, want 5", len(s.Tables))
	}
	loc := s.Table("locations")
	if loc == nil || len(loc.ForeignKeys) != 1 || loc.ForeignKeys[0].OnDelete != "CASCADE" {
		t.Fatalf("locations FK not loaded correctly: %+v", loc)
	}
	if v := s.Table("active_locations"); v == nil || v.Kind != "view" {
		t.Errorf("expected active_locations view, got %+v", v)
	}

	var oneToOne bool
	for _, r := range s.Relationships() {
		if r.From == "location_settings" {
			oneToOne = r.OneToOne
		}
	}
	if !oneToOne {
		t.Error("location_settings → locations should be one-to-one")
	}

	got := checks(Analyze(s))
	for _, want := range []string{
		"audit_log/missing-primary-key",
		"audit_log/duplicate-index",
		"locations/unindexed-foreign-key",
		"locations/implicit-relationship",
	} {
		if _, ok := got[want]; !ok {
			t.Errorf("missing finding %s; got %v", want, got)
		}
	}
}
