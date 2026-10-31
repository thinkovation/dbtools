# dbcopy

Backs up a PostgreSQL database and restores it into another database,
replacing the target. Use it to take a one-off backup, or to populate a test
database from production or staging.

It wraps PostgreSQL's own `pg_dump` and `pg_restore`, so the copy includes
everything they handle: schemas, data, sequences, indexes, constraints,
functions, extensions and comments.

## Install

```bash
go install github.com/thinkovation/dbtools/dbcopy@latest
```

You also need the PostgreSQL client tools (`pg_dump`, `pg_restore`) on your
`PATH`. Their version must be the same as or newer than the server's. dbcopy
checks this before it starts.

## Usage

```bash
export DBCOPY_SOURCE='postgres://user@prod-db:5432/app?sslmode=require'
export DBCOPY_TARGET='postgres://user@localhost:5432/app_test'

dbcopy backup -out app.dump               # one-off backup
dbcopy restore -in app.dump               # load a backup into $DBCOPY_TARGET
dbcopy clone                              # backup + restore + verify
dbcopy clone -out backups/app.dump -yes   # non-interactive, e.g. in CI
```

`clone` keeps the dump it makes, so one run gives you a backup and a fresh
test database. Without `-out`, the dump is named `<dbname>-<UTC timestamp>.dump`.

Keep passwords out of your shell history: use the environment variables, or a
`~/.pgpass` file. dbcopy passes passwords to `pg_dump` and `pg_restore` through
`PGPASSWORD`, never on their command lines.

### Flags

| Flag              | Commands         | Default            | Meaning |
|-------------------|------------------|--------------------|---------|
| `-source`         | backup, clone    | `$DBCOPY_SOURCE`   | Database to copy from |
| `-target`         | restore, clone   | `$DBCOPY_TARGET`   | Database to create or replace |
| `-out`            | backup, clone    | `<db>-<time>.dump` | Dump file to write |
| `-in`             | restore          | (required)         | Dump file to restore |
| `-yes`            | restore, clone   | off                | Replace an existing target without asking |
| `-maintenance-db` | restore, clone   | `postgres`         | Database used to run `DROP`/`CREATE DATABASE` on the target server |
| `-jobs`           | restore, clone   | CPUs, up to 4      | Parallel `pg_restore` jobs |
| `-keep-owners`    | restore, clone   | off                | Keep original owners and grants (the roles must exist on the target) |
| `-timeout`        | all              | none               | Overall time limit |
| `-v`              | all              | off                | Show `pg_dump`/`pg_restore` output |

## How the target is replaced

1. **Safety checks.**
   - It refuses if the target is the source database, or a system database
     (`postgres`, `template0`, `template1`).
   - If the target exists, it shows the target's name and size, and you must
     type the name to confirm.
   - `clone` asks for this confirmation before dumping, so a long dump is never
     wasted.
   - When input isn't from a terminal, `-yes` is required.
2. **Restore into a temporary database.** The dump goes into
   `<target>_dbcopy_tmp`. If the restore fails, that temporary database is
   dropped and **the existing target is left unchanged**.
3. **Swap.** Only after a successful restore is the old target dropped and the
   temporary database renamed into its place. On PostgreSQL 13+ this uses
   `DROP DATABASE … WITH (FORCE)`. On older servers, open sessions are
   terminated first. Either way, applications connected to the old test
   database are disconnected.
4. **Verify** (`clone` only). It checks that every table in the source exists
   in the target.

Restores use `--no-owner --no-privileges` by default. That's because a test
server usually doesn't have the production roles, and everything ends up owned
by the user dbcopy connects as. Pass `-keep-owners` for a faithful copy onto a
server that has the same roles.

The user connecting to the target server needs the `CREATEDB` privilege. It
also needs to own any existing target database, or be a superuser.

## Notes

- Dump files hold all of the source's data. They're created with mode `0600`;
  store and delete them with the same care as the database itself.
- The dump uses `pg_dump`'s custom format, so `pg_restore` can restore it in
  parallel or restore selected objects. To restore it by hand:
  `pg_restore --no-owner -d <db> app.dump`.
- The target database is created from `template0` with the server's default
  encoding and locale.

## Tests

```bash
go test ./...
DBCOPY_PG_DSN='postgres://postgres@localhost:5432/postgres?sslmode=disable' go test ./...
```

The integration test creates and drops the `dbcopy_test_src` and
`dbcopy_test_dst` databases.
