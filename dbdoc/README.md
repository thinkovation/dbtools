# dbdoc

Generates documentation for a PostgreSQL or MySQL/MariaDB database, built on
[`dbinspect`](../dbinspect). Each table gets its columns, keys, indexes and
links to related tables. The output also includes an entity-relationship
diagram and a list of `dbinspect` findings.

## Install

```bash
go install github.com/thinkovation/dbtools/dbdoc@latest
```

## Usage

```bash
export DATABASE_URL='postgres://user@localhost:5432/app?sslmode=disable'
dbdoc -out SCHEMA.md
dbdoc -out schema.html
dbdoc -no-timestamp -out docs/SCHEMA.md   # reproducible, for committing
```

Prefer `DATABASE_URL` (or a `~/.pgpass` file for Postgres) over putting a
password in `-dsn`: command-line arguments end up in shell history and are
visible to other users in process listings.

| Flag            | Default           | Meaning                                                 |
|-----------------|-------------------|---------------------------------------------------------|
| `-dsn`          | `$DATABASE_URL`   | `postgres://…` or `mysql://…` URL                        |
| `-schema`       | current schema/db | Postgres schema or MySQL database to document            |
| `-out`          | stdout            | Output file                                             |
| `-format`       | from `-out`       | `markdown` or `html` (`.html`/`.htm` select HTML)        |
| `-title`        | `<schema> database` | Document title                                        |
| `-no-diagram`   | off               | Omit the ER diagram (very large schemas)                 |
| `-no-timestamp` | off               | Omit the generated-at line so reruns diff cleanly        |
| `-timeout`      | `60s`             | Overall timeout                                         |

**Markdown:** one file with a contents table, a Mermaid ER diagram and a
section per table. GitHub, GitLab and most wikis render the diagram natively.

**HTML:** a single self-contained page with a filterable table list, light and
dark themes, and the diagram. The diagram is drawn by Mermaid, loaded from
jsDelivr, so viewing it needs internet access. Everything else works offline.

Table and column comments from the database are used as descriptions, so the
best way to improve the docs is `COMMENT ON TABLE …` / `COMMENT ON COLUMN …`
(Postgres) or `COMMENT '…'` clauses (MySQL).

## Development

The repository root has a `go.work` file, so changes to `../dbinspect` are
picked up immediately. After pushing changes to `dbinspect`, update the
published dependency with:

```bash
go get github.com/thinkovation/dbtools/dbinspect@main && go mod tidy
```
