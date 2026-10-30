package dbinspect

import (
	"context"
	"database/sql"
	"fmt"
)

// MySQL inspects MySQL 5.7+/8.x and MariaDB via information_schema.
type MySQL struct{}

func (MySQL) Dialect() string { return "mysql" }

func (MySQL) DefaultSchema(ctx context.Context, db *sql.DB) (string, error) {
	var s sql.NullString
	if err := db.QueryRowContext(ctx, `SELECT DATABASE()`).Scan(&s); err != nil {
		return "", err
	}
	if !s.Valid || s.String == "" {
		return "", fmt.Errorf("no database selected; put one in the DSN path or pass a schema")
	}
	return s.String, nil
}

func (m MySQL) Inspect(ctx context.Context, db *sql.DB, schema string) (*Schema, error) {
	s := &Schema{Dialect: m.Dialect(), Name: schema}
	byName := map[string]*Table{}

	steps := []struct {
		name string
		fn   func(context.Context, *sql.DB, string, *Schema, map[string]*Table) error
	}{
		{"tables", myTables},
		{"columns", myColumns},
		{"foreign keys", myForeignKeys},
		{"indexes", myIndexes},
	}
	for _, st := range steps {
		if err := st.fn(ctx, db, schema, s, byName); err != nil {
			return nil, fmt.Errorf("mysql %s: %w", st.name, err)
		}
	}
	return s, nil
}

func myTables(ctx context.Context, db *sql.DB, schema string, s *Schema, byName map[string]*Table) error {
	rows, err := db.QueryContext(ctx, `
		SELECT TABLE_NAME,
		       CASE TABLE_TYPE WHEN 'VIEW' THEN 'view' ELSE 'table' END,
		       COALESCE(TABLE_COMMENT, ''), COALESCE(TABLE_ROWS, 0)
		FROM information_schema.TABLES
		WHERE TABLE_SCHEMA = ?`, schema)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		t := &Table{}
		if err := rows.Scan(&t.Name, &t.Kind, &t.Comment, &t.RowEstimate); err != nil {
			return err
		}
		if t.Kind == "view" {
			t.Comment = "" // MySQL reports "VIEW" as the comment
		}
		s.Tables = append(s.Tables, t)
		byName[t.Name] = t
	}
	return rows.Err()
}

func myColumns(ctx context.Context, db *sql.DB, schema string, _ *Schema, byName map[string]*Table) error {
	rows, err := db.QueryContext(ctx, `
		SELECT TABLE_NAME, COLUMN_NAME, COLUMN_TYPE, IS_NULLABLE = 'YES',
		       COLUMN_DEFAULT, COALESCE(COLUMN_COMMENT, ''), COALESCE(EXTRA, '')
		FROM information_schema.COLUMNS
		WHERE TABLE_SCHEMA = ?
		ORDER BY TABLE_NAME, ORDINAL_POSITION`, schema)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var table string
		var def sql.NullString
		col := &Column{}
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

func myForeignKeys(ctx context.Context, db *sql.DB, schema string, _ *Schema, byName map[string]*Table) error {
	rows, err := db.QueryContext(ctx, `
		SELECT k.TABLE_NAME, k.CONSTRAINT_NAME, k.COLUMN_NAME,
		       k.REFERENCED_TABLE_SCHEMA, k.REFERENCED_TABLE_NAME, k.REFERENCED_COLUMN_NAME,
		       r.UPDATE_RULE, r.DELETE_RULE
		FROM information_schema.KEY_COLUMN_USAGE k
		JOIN information_schema.REFERENTIAL_CONSTRAINTS r
		  ON r.CONSTRAINT_SCHEMA = k.CONSTRAINT_SCHEMA
		 AND r.TABLE_NAME = k.TABLE_NAME
		 AND r.CONSTRAINT_NAME = k.CONSTRAINT_NAME
		WHERE k.TABLE_SCHEMA = ? AND k.REFERENCED_TABLE_NAME IS NOT NULL
		ORDER BY k.TABLE_NAME, k.CONSTRAINT_NAME, k.ORDINAL_POSITION`, schema)
	if err != nil {
		return err
	}
	defer rows.Close()
	type key struct{ table, name string }
	fks := map[key]*ForeignKey{}
	for rows.Next() {
		var table, name, col, refSchema, refTable, refCol, onUpd, onDel string
		if err := rows.Scan(&table, &name, &col, &refSchema, &refTable, &refCol, &onUpd, &onDel); err != nil {
			return err
		}
		t := byName[table]
		if t == nil {
			continue
		}
		fk := fks[key{table, name}]
		if fk == nil {
			fk = &ForeignKey{Name: name, RefTable: refTable, OnUpdate: onUpd, OnDelete: onDel}
			if refSchema != schema {
				fk.RefSchema = refSchema
			}
			fks[key{table, name}] = fk
			t.ForeignKeys = append(t.ForeignKeys, fk)
		}
		fk.Columns = append(fk.Columns, col)
		fk.RefColumns = append(fk.RefColumns, refCol)
	}
	return rows.Err()
}

func myIndexes(ctx context.Context, db *sql.DB, schema string, _ *Schema, byName map[string]*Table) error {
	// COLUMN_NAME is NULL for functional indexes (MySQL 8.0.13+).
	rows, err := db.QueryContext(ctx, `
		SELECT TABLE_NAME, INDEX_NAME, NON_UNIQUE = 0, COALESCE(COLUMN_NAME, '(expression)')
		FROM information_schema.STATISTICS
		WHERE TABLE_SCHEMA = ?
		ORDER BY TABLE_NAME, INDEX_NAME, SEQ_IN_INDEX`, schema)
	if err != nil {
		return err
	}
	defer rows.Close()
	type key struct{ table, name string }
	idxs := map[key]*Index{}
	for rows.Next() {
		var table, name, col string
		var unique bool
		if err := rows.Scan(&table, &name, &unique, &col); err != nil {
			return err
		}
		t := byName[table]
		if t == nil {
			continue
		}
		idx := idxs[key{table, name}]
		if idx == nil {
			idx = &Index{Name: name, Unique: unique, Primary: name == "PRIMARY"}
			idxs[key{table, name}] = idx
			t.Indexes = append(t.Indexes, idx)
		}
		idx.Columns = append(idx.Columns, col)
		if idx.Primary {
			t.PrimaryKey = append(t.PrimaryKey, col)
		}
	}
	return rows.Err()
}
