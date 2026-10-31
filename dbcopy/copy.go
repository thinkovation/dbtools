package main

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"golang.org/x/term"
)

// Options controls backup and restore behaviour.
type Options struct {
	Yes           bool   // replace an existing target without asking
	MaintenanceDB string // database used for DROP/CREATE on the target server
	Jobs          int    // parallel pg_restore jobs
	KeepOwners    bool   // restore original owners and privileges
	Verbose       bool   // stream pg_dump/pg_restore output

	Log     io.Writer // progress messages
	Confirm func(prompt, want string) bool

	confirmed bool // replacement already confirmed (clone asks before dumping)
}

func (o *Options) logf(format string, args ...any) {
	fmt.Fprintf(o.Log, format+"\n", args...)
}

// Backup dumps the source database to path in pg_dump's custom format. The
// file is written beside path and renamed into place only on success.
func Backup(ctx context.Context, src *url.URL, path string, o *Options) error {
	conn, err := pgx.Connect(ctx, src.String())
	if err != nil {
		return fmt.Errorf("connect to source: %w", err)
	}
	err = checkToolVersion(ctx, conn, "pg_dump")
	conn.Close(ctx)
	if err != nil {
		return err
	}

	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".tmp*")
	if err != nil {
		return err
	}
	tmp.Close()
	defer os.Remove(tmp.Name()) // no-op after a successful rename

	o.logf("dumping %s → %s", redact(src), path)
	start := time.Now()
	connStr, env := libpqArgs(src)
	if err := runTool(ctx, o.Verbose, env, "pg_dump",
		"--format=custom", "--file="+tmp.Name(), "--dbname="+connStr); err != nil {
		return err
	}
	if err := os.Chmod(tmp.Name(), 0o600); err != nil { // dumps hold all the data
		return err
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return err
	}
	if fi, err := os.Stat(path); err == nil {
		o.logf("dump complete: %s in %s", humanBytes(fi.Size()), time.Since(start).Round(time.Millisecond))
	}
	return nil
}

// Restore loads a dump into the target database, replacing it if it exists.
// It restores into a temporary database first and swaps it in only after the
// restore succeeds, so a failed restore leaves the existing target untouched.
func Restore(ctx context.Context, dumpPath string, dst *url.URL, o *Options) error {
	if err := checkTargetName(dst, o); err != nil {
		return err
	}
	if _, err := os.Stat(dumpPath); err != nil {
		return err
	}
	admin, exists, err := confirmTarget(ctx, dst, o)
	if err != nil {
		return err
	}
	defer admin.Close(ctx)
	name := dbName(dst)

	tmp := tempName(name)
	if err := dropDatabase(ctx, admin, tmp); err != nil { // leftover from an earlier failed run
		return fmt.Errorf("drop leftover %s: %w", tmp, err)
	}
	if _, err := admin.Exec(ctx, `CREATE DATABASE `+ident(tmp)+` TEMPLATE template0`); err != nil {
		return fmt.Errorf("create %s: %w", tmp, err)
	}

	o.logf("restoring %s → %s (via %s)", dumpPath, name, tmp)
	start := time.Now()
	args := []string{"--exit-on-error", "--jobs=" + strconv.Itoa(max(o.Jobs, 1))}
	if !o.KeepOwners {
		// Test servers rarely have the source's roles.
		args = append(args, "--no-owner", "--no-privileges")
	}
	connStr, env := libpqArgs(withDB(dst, tmp))
	args = append(args, "--dbname="+connStr, dumpPath)
	if err := runTool(ctx, o.Verbose, env, "pg_restore", args...); err != nil {
		if dropErr := dropDatabase(context.WithoutCancel(ctx), admin, tmp); dropErr != nil {
			o.logf("warning: could not drop %s after failed restore: %v", tmp, dropErr)
		}
		if exists {
			return fmt.Errorf("%w\nexisting database %q was left unchanged", err, name)
		}
		return err
	}

	if exists {
		if err := dropDatabase(ctx, admin, name); err != nil {
			return fmt.Errorf("drop %s: %w (restored copy is in %s)", name, err, tmp)
		}
	}
	if _, err := admin.Exec(ctx, `ALTER DATABASE `+ident(tmp)+` RENAME TO `+ident(name)); err != nil {
		return fmt.Errorf("rename %s to %s: %w", tmp, name, err)
	}
	o.logf("restore complete in %s", time.Since(start).Round(time.Millisecond))
	return nil
}

func checkTargetName(dst *url.URL, o *Options) error {
	if name := dbName(dst); systemDatabases[name] || name == o.MaintenanceDB {
		return fmt.Errorf("refusing to restore into %q: it is a system or maintenance database", name)
	}
	return nil
}

// confirmTarget validates the target, connects to its server and, if the
// target already exists, gets confirmation to replace it. The caller closes
// the returned connection.
func confirmTarget(ctx context.Context, dst *url.URL, o *Options) (*pgx.Conn, bool, error) {
	if err := checkTargetName(dst, o); err != nil {
		return nil, false, err
	}
	name := dbName(dst)
	admin, err := pgx.Connect(ctx, withDB(dst, o.MaintenanceDB).String())
	if err != nil {
		return nil, false, fmt.Errorf("connect to target server (maintenance database %q): %w", o.MaintenanceDB, err)
	}
	exists, err := func() (bool, error) {
		if err := checkToolVersion(ctx, admin, "pg_restore"); err != nil {
			return false, err
		}
		exists, err := databaseExists(ctx, admin, name)
		if err != nil || !exists || o.confirmed {
			return exists, err
		}
		size, _ := databaseSize(ctx, admin, name)
		o.logf("target database %q on %s already exists (%s) and will be DROPPED and replaced", name, serverKey(dst), size)
		if !o.Yes && !o.Confirm(fmt.Sprintf("Type the database name %q to confirm: ", name), name) {
			return true, errAborted
		}
		return true, nil
	}()
	if err != nil {
		admin.Close(ctx)
		return nil, false, err
	}
	return admin, exists, nil
}

// Clone backs up src to dumpPath, restores it into dst, and checks that the
// target has the same set of tables as the source.
func Clone(ctx context.Context, src, dst *url.URL, dumpPath string, o *Options) error {
	if sameDatabase(src, dst) {
		return fmt.Errorf("source and target are the same database (%s/%s)", serverKey(src), dbName(src))
	}
	// Confirm before dumping, so a large dump isn't wasted on a "no".
	admin, _, err := confirmTarget(ctx, dst, o)
	if err != nil {
		return err
	}
	admin.Close(ctx)
	ro := *o
	ro.confirmed = true

	if err := Backup(ctx, src, dumpPath, o); err != nil {
		return err
	}
	if err := Restore(ctx, dumpPath, dst, &ro); err != nil {
		return err
	}
	return verifyTables(ctx, src, dst, o)
}

func verifyTables(ctx context.Context, src, dst *url.URL, o *Options) error {
	want, err := listTables(ctx, src)
	if err != nil {
		return fmt.Errorf("verify: list source tables: %w", err)
	}
	got, err := listTables(ctx, dst)
	if err != nil {
		return fmt.Errorf("verify: list target tables: %w", err)
	}
	have := map[string]bool{}
	for _, t := range got {
		have[t] = true
	}
	var missing []string
	for _, t := range want {
		if !have[t] {
			missing = append(missing, t)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("verify: %d table(s) missing from target: %s", len(missing), strings.Join(missing, ", "))
	}
	o.logf("verified: all %d tables present in target", len(want))
	return nil
}

// confirmFromStdin asks the user to type want. It refuses when stdin isn't a
// terminal, so scripts must pass -yes explicitly.
func confirmFromStdin(prompt, want string) bool {
	if !term.IsTerminal(int(os.Stdin.Fd())) {
		fmt.Fprintln(os.Stderr, "dbcopy: stdin is not a terminal; pass -yes to replace an existing database")
		return false
	}
	fmt.Fprint(os.Stderr, prompt)
	line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
	return strings.TrimSpace(line) == want
}

func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}
