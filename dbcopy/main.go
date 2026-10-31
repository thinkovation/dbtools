// Command dbcopy backs up a PostgreSQL database and restores it into another
// database, replacing the target. Typical uses are taking a one-off backup and
// populating a test database from production or staging.
//
//	dbcopy backup  -source $SRC -out app.dump
//	dbcopy restore -in app.dump -target $TEST
//	dbcopy clone   -source $SRC -target $TEST [-out app.dump]
//
// URLs may also come from DBCOPY_SOURCE and DBCOPY_TARGET, which keeps
// passwords out of shell history.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"runtime"
	"time"
)

const usage = `usage: dbcopy <command> [flags]

Commands:
  backup    dump a database to a file
  restore   restore a dump into a database, replacing it if it exists
  clone     backup, then restore into another database, then verify

Run "dbcopy <command> -h" for the flags of each command.
`

func main() {
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	if err := run(ctx, os.Args[1], os.Args[2:]); err != nil {
		fmt.Fprintln(os.Stderr, "dbcopy:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, command string, args []string) error {
	fs := flag.NewFlagSet("dbcopy "+command, flag.ExitOnError)
	o := &Options{Log: os.Stderr, Confirm: confirmFromStdin}
	var source, target, in, out *string

	addTarget := func() {
		target = fs.String("target", os.Getenv("DBCOPY_TARGET"), "target database URL; defaults to $DBCOPY_TARGET")
		fs.BoolVar(&o.Yes, "yes", false, "replace an existing target database without asking")
		fs.StringVar(&o.MaintenanceDB, "maintenance-db", "postgres", "database on the target server to connect to for DROP/CREATE DATABASE")
		fs.IntVar(&o.Jobs, "jobs", min(runtime.NumCPU(), 4), "parallel restore jobs")
		fs.BoolVar(&o.KeepOwners, "keep-owners", false, "restore original object owners and privileges (the roles must exist on the target)")
	}
	switch command {
	case "backup":
		source = fs.String("source", os.Getenv("DBCOPY_SOURCE"), "source database URL; defaults to $DBCOPY_SOURCE")
		out = fs.String("out", "", "dump file to write (default: <dbname>-<timestamp>.dump)")
	case "restore":
		in = fs.String("in", "", "dump file to restore (required)")
		addTarget()
	case "clone":
		source = fs.String("source", os.Getenv("DBCOPY_SOURCE"), "source database URL; defaults to $DBCOPY_SOURCE")
		out = fs.String("out", "", "where to keep the dump (default: <dbname>-<timestamp>.dump)")
		addTarget()
	case "-h", "-help", "--help", "help":
		fmt.Fprint(os.Stdout, usage)
		return nil
	default:
		return fmt.Errorf("unknown command %q\n\n%s", command, usage)
	}
	fs.BoolVar(&o.Verbose, "v", false, "show pg_dump/pg_restore output")
	timeout := fs.Duration("timeout", 0, "overall timeout (0 = none)")
	fs.Parse(args)
	if fs.NArg() > 0 {
		return fmt.Errorf("unexpected arguments: %v", fs.Args())
	}
	if *timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, *timeout)
		defer cancel()
	}

	switch command {
	case "backup":
		src, err := parsePGURL(*source, "source")
		if err != nil {
			return err
		}
		return Backup(ctx, src, dumpPath(*out, src.Path), o)
	case "restore":
		if *in == "" {
			return fmt.Errorf("-in is required")
		}
		dst, err := parsePGURL(*target, "target")
		if err != nil {
			return err
		}
		return Restore(ctx, *in, dst, o)
	default: // clone
		src, err := parsePGURL(*source, "source")
		if err != nil {
			return err
		}
		dst, err := parsePGURL(*target, "target")
		if err != nil {
			return err
		}
		return Clone(ctx, src, dst, dumpPath(*out, src.Path), o)
	}
}

func dumpPath(out, srcPath string) string {
	if out != "" {
		return out
	}
	return fmt.Sprintf("%s-%s.dump", srcPath[1:], time.Now().UTC().Format("20060102T150405Z"))
}
