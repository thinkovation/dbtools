package dbinspect

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
)

// Postgres inspects PostgreSQL (11+) via the pg_catalog tables.
type Postgres struct{}

func (Postgres) Dialect() string { return "postgres" }

func (Postgres) DefaultSchema(ctx context.Context, db *sql.DB) (string, error) {
	var s string
	err := db.QueryRowContext(ctx, `SELECT current_schema()`).Scan(&s)
	return s, err
}

func (p Postgres) Inspect(ctx context.Context, db *sql.DB, schema string) (*Schema, error) {
	s := &Schema{Dialect: p.Dialect(), Name: schema}
	byName := map[string]*Table{}

	steps := []struct {
		name string
		fn   func(context.Context, *sql.DB, string, *Schema, map[string]*Table) error
	}{
		{"tables", pgTables},
		{"columns", pgColumns},
		{"constraints", pgConstraints},
		{"indexes", pgIndexes},
	}
	for _, st := range steps {
		if err := st.fn(ctx, db, schema, s, byName); err != nil {
			return nil, fmt.Errorf("postgres %s: %w", st.name, err)
		}
	}
	return s, nil
}

func pgTables(ctx context.Context, db *sql.DB, schema string, s *Schema, byName map[string]*Table) error {
	rows, err := db.QueryContext(ctx, `
		SELECT c.relname,
		       CASE c.relkind WHEN 'r' THEN 'table' WHEN 'p' THEN 'partitioned table'
		                      WHEN 'v' THEN 'view' WHEN 'm' THEN 'materialized view' END,
		       COALESCE(obj_description(c.oid, 'pg_class'), ''),
		       GREATEST(c.reltuples, 0)::bigint
		FROM pg_class c
		JOIN pg_namespace n ON n.oid = c.relnamespace
		WHERE n.nspname = $1 AND c.relkind IN ('r', 'p', 'v', 'm') AND NOT c.relispartition`, schema)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		t := &Table{}
		if err := rows.Scan(&t.Name, &t.Kind, &t.Comment, &t.RowEstimate); err != nil {
			return err
		}
		s.Tables = append(s.Tables, t)
		byName[t.Name] = t
	}
	return rows.Err()
}

func pgColumns(ctx context.Context, db *sql.DB, schema string, _ *Schema, byName map[string]*Table) error {
	rows, err := db.QueryContext(ctx, `
		SELECT c.relname, a.attname,
		       format_type(a.atttypid, a.atttypmod),
		       NOT a.attnotnull,
		       pg_get_expr(d.adbin, d.adrelid),
		       COALESCE(col_description(c.oid, a.attnum), ''),
		       CASE WHEN a.attidentity <> '' THEN 'identity'
		            WHEN a.attgenerated <> '' THEN 'generated' ELSE '' END
		FROM pg_attribute a
		JOIN pg_class c ON c.oid = a.attrelid
		JOIN pg_namespace n ON n.oid = c.relnamespace
		LEFT JOIN pg_attrdef d ON d.adrelid = a.attrelid AND d.adnum = a.attnum
		WHERE n.nspname = $1 AND c.relkind IN ('r', 'p', 'v', 'm')
		  AND a.attnum > 0 AND NOT a.attisdropped
		ORDER BY c.relname, a.attnum`, schema)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var table string
		col := &Column{}
		var def sql.NullString
		if err := rows.Scan(&table, &col.Name, &col.Type, &col.Nullable, &def, &col.Comment, &col.Extra); err != nil {
			return err
		}
		if def.Valid {
			col.Default = &def.String
		}
		if t := byName[table]; t != nil {
			t.Columns = append(t.Columns, col)
		}
	}
	return rows.Err()
}

var pgFKAction = map[string]string{
	"a": "NO ACTION", "r": "RESTRICT", "c": "CASCADE", "n": "SET NULL", "d": "SET DEFAULT",
}

func pgConstraints(ctx context.Context, db *sql.DB, schema string, _ *Schema, byName map[string]*Table) error {
	// Column lists are returned as JSON arrays to preserve key order without
	// relying on driver-specific array scanning.
	rows, err := db.QueryContext(ctx, `
		SELECT con.conname, con.contype, cl.relname,
		       to_json(ARRAY(SELECT att.attname
		                     FROM unnest(con.conkey) WITH ORDINALITY k(attnum, ord)
		                     JOIN pg_attribute att ON att.attrelid = con.conrelid AND att.attnum = k.attnum
		                     ORDER BY k.ord)),
		       COALESCE(fn.nspname, ''), COALESCE(fcl.relname, ''),
		       to_json(ARRAY(SELECT att.attname
		                     FROM unnest(con.confkey) WITH ORDINALITY k(attnum, ord)
		                     JOIN pg_attribute att ON att.attrelid = con.confrelid AND att.attnum = k.attnum
		                     ORDER BY k.ord)),
		       con.confupdtype::text, con.confdeltype::text
		FROM pg_constraint con
		JOIN pg_class cl ON cl.oid = con.conrelid
		JOIN pg_namespace n ON n.oid = cl.relnamespace
		LEFT JOIN pg_class fcl ON fcl.oid = con.confrelid
		LEFT JOIN pg_namespace fn ON fn.oid = fcl.relnamespace
		WHERE n.nspname = $1 AND con.contype IN ('p', 'f')`, schema)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var (
			name, typ, table, refSchema, refTable, onUpd, onDel string
			cols, refCols                                       []byte
		)
		if err := rows.Scan(&name, &typ, &table, &cols, &refSchema, &refTable, &refCols, &onUpd, &onDel); err != nil {
			return err
		}
		t := byName[table]
		if t == nil {
			continue
		}
		var colNames, refNames []string
		if err := json.Unmarshal(cols, &colNames); err != nil {
			return err
		}
		if err := json.Unmarshal(refCols, &refNames); err != nil {
			return err
		}
		switch typ {
		case "p":
			t.PrimaryKey = colNames
		case "f":
			fk := &ForeignKey{
				Name: name, Columns: colNames, RefTable: refTable, RefColumns: refNames,
				OnUpdate: pgFKAction[onUpd], OnDelete: pgFKAction[onDel],
			}
			if refSchema != schema {
				fk.RefSchema = refSchema
			}
			t.ForeignKeys = append(t.ForeignKeys, fk)
		}
	}
	return rows.Err()
}

func pgIndexes(ctx context.Context, db *sql.DB, schema string, _ *Schema, byName map[string]*Table) error {
	rows, err := db.QueryContext(ctx, `
		SELECT t.relname, i.relname, ix.indisunique, ix.indisprimary,
		       to_json(ARRAY(SELECT pg_get_indexdef(ix.indexrelid, k, true)
		                     FROM generate_series(1, ix.indnkeyatts) k ORDER BY k))
		FROM pg_index ix
		JOIN pg_class i ON i.oid = ix.indexrelid
		JOIN pg_class t ON t.oid = ix.indrelid
		JOIN pg_namespace n ON n.oid = t.relnamespace
		WHERE n.nspname = $1`, schema)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var table string
		var cols []byte
		idx := &Index{}
		if err := rows.Scan(&table, &idx.Name, &idx.Unique, &idx.Primary, &cols); err != nil {
			return err
		}
		if err := json.Unmarshal(cols, &idx.Columns); err != nil {
			return err
		}
		if t := byName[table]; t != nil {
			t.Indexes = append(t.Indexes, idx)
		}
	}
	return rows.Err()
}
