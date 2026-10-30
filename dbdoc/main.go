// Command dbdoc connects to a database and writes human-readable
// documentation of its data model as Markdown or a self-contained HTML page.
//
//	dbdoc -dsn postgres://user:pass@localhost:5432/app?sslmode=disable -out SCHEMA.md
//	dbdoc -dsn mysql://user:pass@localhost:3306/app -format html -out schema.html
//
// The DSN may also be supplied via the DATABASE_URL environment variable.
package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/thinkovation/dbtools/dbinspect"
)

func main() {
	var (
		dsn       = flag.String("dsn", os.Getenv("DATABASE_URL"), "database URL (postgres://… or mysql://…); defaults to $DATABASE_URL")
		schema    = flag.String("schema", "", "schema/database to document (default: current schema)")
		format    = flag.String("format", "", "output format: markdown or html (default: from -out extension, else markdown)")
		out       = flag.String("out", "", "output file (default: stdout)")
		title     = flag.String("title", "", "document title (default: \"<schema> database\")")
		noDiagram = flag.Bool("no-diagram", false, "omit the entity-relationship diagram (useful for very large schemas)")
		noTime    = flag.Bool("no-timestamp", false, "omit the generation timestamp so output is reproducible (for committing to git)")
		timeout   = flag.Duration("timeout", 60*time.Second, "overall timeout")
	)
	flag.Parse()

	if err := run(*dsn, *schema, *format, *out, *title, !*noDiagram, !*noTime, *timeout); err != nil {
		fmt.Fprintln(os.Stderr, "dbdoc:", err)
		os.Exit(1)
	}
}

func run(dsn, schema, format, out, title string, diagram, timestamp bool, timeout time.Duration) error {
	if dsn == "" {
		return fmt.Errorf("no DSN: pass -dsn or set DATABASE_URL")
	}
	if format == "" {
		format = formatFromPath(out)
	}
	render := map[string]func(io.Writer, *Doc) error{
		"markdown": renderMarkdown, "md": renderMarkdown, "html": renderHTML,
	}[format]
	if render == nil {
		return fmt.Errorf("unknown format %q (want markdown or html)", format)
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

	var now time.Time
	if timestamp {
		now = time.Now().UTC()
	}
	d, err := buildDoc(s, title, diagram, now)
	if err != nil {
		return err
	}

	if out == "" {
		return render(os.Stdout, d)
	}
	f, err := os.Create(out)
	if err != nil {
		return err
	}
	w := bufio.NewWriter(f)
	if err := render(w, d); err != nil {
		f.Close()
		return err
	}
	if err := w.Flush(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "dbdoc: wrote %d tables to %s\n", len(d.Tables), out)
	return nil
}

func formatFromPath(path string) string {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".html", ".htm":
		return "html"
	default:
		return "markdown"
	}
}
