// Command dbinspect connects to a database and prints its data model.
//
//	dbinspect -dsn postgres://user:pass@localhost:5432/app?sslmode=disable
//	dbinspect -dsn mysql://user:pass@localhost:3306/app -format mermaid > schema.mmd
//
// The DSN may also be supplied via the DATABASE_URL environment variable.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/thinkovation/dbtools/dbinspect"
)

func main() {
	var (
		dsn     = flag.String("dsn", os.Getenv("DATABASE_URL"), "database URL (postgres://… or mysql://…); defaults to $DATABASE_URL")
		schema  = flag.String("schema", "", "schema/database to inspect (default: current schema)")
		format  = flag.String("format", "text", "output format: text, json, mermaid")
		timeout = flag.Duration("timeout", 60*time.Second, "overall timeout")
	)
	flag.Parse()

	if err := run(*dsn, *schema, *format, *timeout); err != nil {
		fmt.Fprintln(os.Stderr, "dbinspect:", err)
		os.Exit(1)
	}
}

func run(dsn, schema, format string, timeout time.Duration) error {
	if dsn == "" {
		return fmt.Errorf("no DSN: pass -dsn or set DATABASE_URL")
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	db, insp, err := dbinspect.Open(ctx, dsn)
	if err != nil {
		return err
	}
	defer db.Close()

	s, err := dbinspect.Inspect(ctx, db, insp, schema)
	if err != nil {
		return err
	}
	report := dbinspect.NewReport(s)

	switch format {
	case "text":
		return report.WriteText(os.Stdout)
	case "json":
		return report.WriteJSON(os.Stdout)
	case "mermaid":
		return report.WriteMermaid(os.Stdout)
	default:
		return fmt.Errorf("unknown format %q", format)
	}
}
