package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
)

// systemDatabases can never be a restore target.
var systemDatabases = map[string]bool{"postgres": true, "template0": true, "template1": true}

// parsePGURL validates a postgres:// URL and returns it with a database name.
func parsePGURL(raw, what string) (*url.URL, error) {
	if raw == "" {
		return nil, fmt.Errorf("no %s database URL given", what)
	}
	u, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("parse %s URL: %w", what, err)
	}
	if u.Scheme != "postgres" && u.Scheme != "postgresql" {
		return nil, fmt.Errorf("%s URL must start with postgres:// (got %q)", what, u.Scheme)
	}
	if dbName(u) == "" {
		return nil, fmt.Errorf("%s URL has no database name", what)
	}
	return u, nil
}

func dbName(u *url.URL) string { return strings.TrimPrefix(u.Path, "/") }

// withDB returns a copy of u pointing at another database on the same server.
func withDB(u *url.URL, name string) *url.URL {
	c := *u
	c.Path = "/" + name
	c.RawPath = ""
	return &c
}

// redact returns u as a string with any password removed, for messages.
func redact(u *url.URL) string { return u.Redacted() }

// serverKey identifies the server a URL points at, so we can tell whether two
// URLs name the same database. host= in the query (e.g. a Unix socket
// directory) overrides the URL host, as it does in libpq.
func serverKey(u *url.URL) string {
	host := strings.ToLower(u.Hostname())
	if h := u.Query().Get("host"); h != "" {
		host = h
	}
	if host == "" || host == "127.0.0.1" || host == "::1" {
		host = "localhost"
	}
	port := u.Port()
	if p := u.Query().Get("port"); p != "" {
		port = p
	}
	if port == "" {
		port = "5432"
	}
	return host + ":" + port
}

func sameDatabase(a, b *url.URL) bool {
	return serverKey(a) == serverKey(b) && dbName(a) == dbName(b)
}

// libpqArgs returns a connection URI without the password plus the
// environment for a pg_dump/pg_restore child process, so the password never
// appears in the process list.
func libpqArgs(u *url.URL) (conn string, env []string) {
	c := *u
	env = os.Environ()
	if c.User != nil {
		if pw, ok := c.User.Password(); ok {
			env = append(env, "PGPASSWORD="+pw)
			c.User = url.User(c.User.Username())
		}
	}
	return c.String(), env
}

// runTool runs a PostgreSQL client tool, returning its stderr in the error.
func runTool(ctx context.Context, verbose bool, env []string, name string, args ...string) error {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Env = env
	var stderr bytes.Buffer
	if verbose {
		cmd.Stdout, cmd.Stderr = os.Stderr, os.Stderr
	} else {
		cmd.Stderr = &stderr
	}
	if err := cmd.Run(); err != nil {
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			return fmt.Errorf("%s failed: %w\n%s", name, err, msg)
		}
		return fmt.Errorf("%s failed: %w", name, err)
	}
	return nil
}

var versionRe = regexp.MustCompile(`\s(\d+)(?:\.\d+)?`)

// toolMajor returns the major version of a PostgreSQL client tool.
func toolMajor(name string) (int, error) {
	path, err := exec.LookPath(name)
	if err != nil {
		return 0, fmt.Errorf("%s not found on PATH; install the PostgreSQL client tools", name)
	}
	out, err := exec.Command(path, "--version").Output()
	if err != nil {
		return 0, fmt.Errorf("%s --version: %w", name, err)
	}
	m := versionRe.FindStringSubmatch(string(out))
	if m == nil {
		return 0, fmt.Errorf("can't parse %s version from %q", name, out)
	}
	return strconv.Atoi(m[1])
}

// serverMajor returns the server's major version (e.g. 16).
func serverMajor(ctx context.Context, conn *pgx.Conn) (int, error) {
	var num int
	if err := conn.QueryRow(ctx, `SELECT current_setting('server_version_num')::int`).Scan(&num); err != nil {
		return 0, err
	}
	return num / 10000, nil
}

// checkToolVersion fails early with a clear message when a client tool is
// older than the server; pg_dump refuses to dump newer servers anyway.
func checkToolVersion(ctx context.Context, conn *pgx.Conn, tool string) error {
	toolV, err := toolMajor(tool)
	if err != nil {
		return err
	}
	serverV, err := serverMajor(ctx, conn)
	if err != nil {
		return err
	}
	if toolV < serverV {
		return fmt.Errorf("%s is version %d but the server is version %d; install PostgreSQL %d client tools", tool, toolV, serverV, serverV)
	}
	return nil
}

func databaseExists(ctx context.Context, conn *pgx.Conn, name string) (bool, error) {
	var ok bool
	err := conn.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM pg_database WHERE datname = $1)`, name).Scan(&ok)
	return ok, err
}

func databaseSize(ctx context.Context, conn *pgx.Conn, name string) (string, error) {
	var size string
	err := conn.QueryRow(ctx, `SELECT pg_size_pretty(pg_database_size($1))`, name).Scan(&size)
	return size, err
}

func ident(name string) string { return pgx.Identifier{name}.Sanitize() }

// dropDatabase drops name, disconnecting any sessions using it.
func dropDatabase(ctx context.Context, conn *pgx.Conn, name string) error {
	v, err := serverMajor(ctx, conn)
	if err != nil {
		return err
	}
	if v >= 13 {
		_, err = conn.Exec(ctx, `DROP DATABASE IF EXISTS `+ident(name)+` WITH (FORCE)`)
		return err
	}
	// Before PostgreSQL 13: block new connections, end existing ones, drop.
	if _, err := conn.Exec(ctx, `UPDATE pg_database SET datallowconn = false WHERE datname = $1`, name); err != nil {
		return err
	}
	if _, err := conn.Exec(ctx, `SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname = $1 AND pid <> pg_backend_pid()`, name); err != nil {
		return err
	}
	_, err = conn.Exec(ctx, `DROP DATABASE IF EXISTS `+ident(name))
	return err
}

// listTables returns "schema.table" for every user table in the database.
func listTables(ctx context.Context, u *url.URL) ([]string, error) {
	conn, err := pgx.Connect(ctx, u.String())
	if err != nil {
		return nil, err
	}
	defer conn.Close(ctx)
	rows, err := conn.Query(ctx, `
		SELECT n.nspname || '.' || c.relname
		FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
		WHERE c.relkind IN ('r', 'p') AND NOT c.relispartition
		  AND n.nspname NOT IN ('pg_catalog', 'information_schema')
		  AND n.nspname NOT LIKE 'pg_toast%'
		ORDER BY 1`)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowTo[string])
}

// tempName returns the name used to restore into before swapping, kept
// within PostgreSQL's 63-byte identifier limit.
func tempName(name string) string {
	const suffix = "_dbcopy_tmp"
	if len(name)+len(suffix) > 63 {
		name = name[:63-len(suffix)]
	}
	return name + suffix
}

var errAborted = errors.New("aborted: confirmation did not match")
