// Package dbinspect connects to a relational database and extracts its data
// model: tables, columns, primary keys, foreign keys and indexes.
package dbinspect

// Schema is the data model of a single database schema (Postgres) or
// database (MySQL).
type Schema struct {
	Dialect string   `json:"dialect"`
	Name    string   `json:"name"`
	Tables  []*Table `json:"tables"`
}

// Table describes a table, view or materialized view.
type Table struct {
	Name        string        `json:"name"`
	Kind        string        `json:"kind"` // "table", "view", "materialized view", "partitioned table"
	Comment     string        `json:"comment,omitempty"`
	RowEstimate int64         `json:"row_estimate"` // from planner statistics; approximate
	Columns     []*Column     `json:"columns"`
	PrimaryKey  []string      `json:"primary_key,omitempty"`
	ForeignKeys []*ForeignKey `json:"foreign_keys,omitempty"`
	Indexes     []*Index      `json:"indexes,omitempty"`
}

// Column describes a single table column.
type Column struct {
	Name     string  `json:"name"`
	Type     string  `json:"type"`
	Nullable bool    `json:"nullable"`
	Default  *string `json:"default,omitempty"`
	Comment  string  `json:"comment,omitempty"`
	Extra    string  `json:"extra,omitempty"` // e.g. "auto_increment" on MySQL
}

// ForeignKey describes a reference from this table to another.
type ForeignKey struct {
	Name       string   `json:"name"`
	Columns    []string `json:"columns"`
	RefSchema  string   `json:"ref_schema,omitempty"`
	RefTable   string   `json:"ref_table"`
	RefColumns []string `json:"ref_columns"`
	OnUpdate   string   `json:"on_update,omitempty"`
	OnDelete   string   `json:"on_delete,omitempty"`
}

// Index describes an index. Columns may contain expressions for functional
// indexes.
type Index struct {
	Name    string   `json:"name"`
	Columns []string `json:"columns"`
	Unique  bool     `json:"unique"`
	Primary bool     `json:"primary"`
}

// Table returns the table with the given name, or nil.
func (s *Schema) Table(name string) *Table {
	for _, t := range s.Tables {
		if t.Name == name {
			return t
		}
	}
	return nil
}

// Column returns the column with the given name, or nil.
func (t *Table) Column(name string) *Column {
	for _, c := range t.Columns {
		if c.Name == name {
			return c
		}
	}
	return nil
}
