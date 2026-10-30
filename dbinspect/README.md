# dbinspect

Connects to a PostgreSQL or MySQL/MariaDB database and extracts its data model
(tables, views, columns, primary keys, foreign keys, indexes), then runs
checks for common modelling problems.

## CLI

```bash
go run ./cmd/dbinspect -dsn 'postgres://user:pass@localhost:5432/app?sslmode=disable'
go run ./cmd/dbinspect -dsn 'mysql://user:pass@localhost:3306/app' -format json
go run ./cmd/dbinspect -format mermaid > schema.mmd   # DSN from $DATABASE_URL
```

| Flag       | Default            | Meaning                                    |
|------------|--------------------|--------------------------------------------|
| `-dsn`     | `$DATABASE_URL`    | `postgres://…` or `mysql://…` URL           |
| `-schema`  | current schema/db  | Postgres schema or MySQL database to read   |
| `-format`  | `text`             | `text`, `json`, or `mermaid` (ER diagram)   |
| `-timeout` | `60s`              | Overall timeout                            |

The tool only reads catalog metadata. Use a read-only database user.

## Library

```go
db, insp, err := dbinspect.Open(ctx, dsn)
defer db.Close()
schema, err := dbinspect.Inspect(ctx, db, insp, "") // "" = current schema
for _, f := range dbinspect.Analyze(schema) { fmt.Println(f.Table, f.Message) }
```

## Checks

| Check                   | Severity | What it flags                                        |
|-------------------------|----------|------------------------------------------------------|
| `missing-primary-key`   | warning  | Tables with no primary key                           |
| `unindexed-foreign-key` | warning  | FKs with no index starting on their columns          |
| `duplicate-index`       | warning  | Two indexes on the same column list                  |
| `dangling-foreign-key`  | warning  | FKs pointing at a table not found in the schema      |
| `implicit-relationship` | info     | `*_id` columns without an FK constraint              |
| `isolated-table`        | info     | Tables with no FK links in either direction          |

## Adding a database

Implement the `Inspector` interface (`Dialect`, `DefaultSchema`, `Inspect`)
and add its URL scheme to `Open` in `inspect.go`.

## Tests

```bash
go test ./...                                                     # unit tests
DBINSPECT_PG_DSN='postgres://postgres@localhost:5432/postgres?sslmode=disable' go test ./...
```

The integration test creates and drops a `dbinspect_test` schema.
