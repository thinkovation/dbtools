package dbinspect

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"sort"
	"strings"

	"github.com/go-sql-driver/mysql"
	_ "github.com/jackc/pgx/v5/stdlib" // registers the "pgx" driver
)

// Inspector reads the data model from a database using a dialect-specific
// catalog.
type Inspector interface {
	// Dialect is the name of the database flavour, e.g. "postgres".
	Dialect() string
	// DefaultSchema returns the schema to inspect when none is given.
	DefaultSchema(ctx context.Context, db *sql.DB) (string, error)
	// Inspect loads every table in the schema.
	Inspect(ctx context.Context, db *sql.DB, schema string) (*Schema, error)
}

// Open connects to the database described by a URL DSN and returns the
// connection together with the matching Inspector. Supported schemes:
//
//	postgres://user:pass@host:5432/dbname?sslmode=disable  (also postgresql://)
//	mysql://user:pass@host:3306/dbname
func Open(ctx context.Context, dsn string) (*sql.DB, Inspector, error) {
	u, err := url.Parse(dsn)
	if err != nil {
		return nil, nil, fmt.Errorf("parse dsn: %w", err)
	}

	var (
		driver, driverDSN string
		insp              Inspector
	)
	switch u.Scheme {
	case "postgres", "postgresql":
		driver, driverDSN, insp = "pgx", dsn, Postgres{}
	case "mysql", "mariadb":
		driverDSN, err = mysqlDSN(u)
		if err != nil {
			return nil, nil, err
		}
		driver, insp = "mysql", MySQL{}
	default:
		return nil, nil, fmt.Errorf("unsupported scheme %q (want postgres:// or mysql://)", u.Scheme)
	}

	db, err := sql.Open(driver, driverDSN)
	if err != nil {
		return nil, nil, err
	}
	if err := db.PingContext(ctx); err != nil {
		db.Close()
		return nil, nil, fmt.Errorf("connect: %w", err)
	}
	return db, insp, nil
}

// Inspect is a convenience wrapper that resolves the default schema when
// schema is empty and sorts the result for stable output.
func Inspect(ctx context.Context, db *sql.DB, insp Inspector, schema string) (*Schema, error) {
	if schema == "" {
		var err error
		if schema, err = insp.DefaultSchema(ctx, db); err != nil {
			return nil, fmt.Errorf("resolve default schema: %w", err)
		}
	}
	s, err := insp.Inspect(ctx, db, schema)
	if err != nil {
		return nil, err
	}
	sort.Slice(s.Tables, func(i, j int) bool { return s.Tables[i].Name < s.Tables[j].Name })
	for _, t := range s.Tables {
		sort.Slice(t.ForeignKeys, func(i, j int) bool { return t.ForeignKeys[i].Name < t.ForeignKeys[j].Name })
		sort.Slice(t.Indexes, func(i, j int) bool { return t.Indexes[i].Name < t.Indexes[j].Name })
	}
	return s, nil
}

// mysqlDSN converts mysql://user:pass@host:port/db?param=v into the
// go-sql-driver DSN format.
func mysqlDSN(u *url.URL) (string, error) {
	cfg := mysql.NewConfig()
	cfg.User = u.User.Username()
	cfg.Passwd, _ = u.User.Password()
	cfg.Net = "tcp"
	cfg.Addr = u.Host
	if u.Port() == "" {
		cfg.Addr = u.Hostname() + ":3306"
	}
	cfg.DBName = strings.TrimPrefix(u.Path, "/")
	if len(u.Query()) > 0 {
		cfg.Params = map[string]string{}
		for k, v := range u.Query() {
			cfg.Params[k] = v[0]
		}
	}
	return cfg.FormatDSN(), nil
}
