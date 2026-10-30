package dbinspect

import (
	"fmt"
	"sort"
	"strings"
)

// Severity ranks a Finding.
type Severity string

const (
	Warning Severity = "warning"
	Info    Severity = "info"
)

// Finding is an observation about the data model worth reviewing.
type Finding struct {
	Severity Severity `json:"severity"`
	Check    string   `json:"check"`
	Table    string   `json:"table"`
	Message  string   `json:"message"`
}

// Relationship is a foreign-key edge between two tables.
type Relationship struct {
	From       string   `json:"from"`
	FromCols   []string `json:"from_columns"`
	To         string   `json:"to"`
	ToCols     []string `json:"to_columns"`
	Name       string   `json:"name"`
	Optional   bool     `json:"optional"`    // any FK column is nullable
	OneToOne   bool     `json:"one_to_one"`  // FK columns are themselves unique
	ToExternal bool     `json:"to_external"` // target lives in another schema
}

// Relationships returns every foreign-key edge in the schema.
func (s *Schema) Relationships() []Relationship {
	var out []Relationship
	for _, t := range s.Tables {
		for _, fk := range t.ForeignKeys {
			r := Relationship{
				From: t.Name, FromCols: fk.Columns, To: fk.RefTable, ToCols: fk.RefColumns,
				Name: fk.Name, ToExternal: fk.RefSchema != "",
			}
			for _, c := range fk.Columns {
				if col := t.Column(c); col != nil && col.Nullable {
					r.Optional = true
				}
			}
			r.OneToOne = t.hasUniqueOn(fk.Columns)
			out = append(out, r)
		}
	}
	return out
}

// Analyze runs a set of heuristic checks over the schema and returns any
// findings, sorted by severity then table.
func Analyze(s *Schema) []Finding {
	var out []Finding
	add := func(sev Severity, check, table, format string, args ...any) {
		out = append(out, Finding{sev, check, table, fmt.Sprintf(format, args...)})
	}

	referenced := map[string]bool{}
	for _, r := range s.Relationships() {
		if !r.ToExternal {
			referenced[r.To] = true
		}
	}

	for _, t := range s.Tables {
		if t.Kind != "table" && t.Kind != "partitioned table" {
			continue
		}

		if len(t.PrimaryKey) == 0 {
			add(Warning, "missing-primary-key", t.Name, "table has no primary key")
		}

		for _, fk := range t.ForeignKeys {
			if !t.hasIndexPrefix(fk.Columns) {
				add(Warning, "unindexed-foreign-key", t.Name,
					"foreign key %s (%s) has no supporting index; joins and cascading deletes will scan",
					fk.Name, strings.Join(fk.Columns, ", "))
			}
			if fk.RefSchema == "" && s.Table(fk.RefTable) == nil {
				add(Warning, "dangling-foreign-key", t.Name,
					"foreign key %s references unknown table %s", fk.Name, fk.RefTable)
			}
		}

		if len(t.ForeignKeys) == 0 && !referenced[t.Name] {
			add(Info, "isolated-table", t.Name, "table has no foreign-key relationships")
		}

		// Columns named like FKs that aren't declared as such often mean
		// relationships enforced only in application code.
		fkCols := map[string]bool{}
		for _, fk := range t.ForeignKeys {
			for _, c := range fk.Columns {
				fkCols[c] = true
			}
		}
		for _, c := range t.Columns {
			if fkCols[c.Name] || !strings.HasSuffix(strings.ToLower(c.Name), "_id") {
				continue
			}
			if len(t.PrimaryKey) == 1 && t.PrimaryKey[0] == c.Name {
				continue
			}
			add(Info, "implicit-relationship", t.Name,
				"column %s looks like a reference but has no foreign-key constraint", c.Name)
		}

		seen := map[string]string{}
		for _, idx := range t.Indexes {
			k := strings.Join(idx.Columns, "\x00")
			if other, ok := seen[k]; ok {
				add(Warning, "duplicate-index", t.Name,
					"indexes %s and %s cover the same columns (%s)", other, idx.Name, strings.Join(idx.Columns, ", "))
				continue
			}
			seen[k] = idx.Name
		}
	}

	rank := map[Severity]int{Warning: 0, Info: 1}
	sort.SliceStable(out, func(i, j int) bool {
		if rank[out[i].Severity] != rank[out[j].Severity] {
			return rank[out[i].Severity] < rank[out[j].Severity]
		}
		if out[i].Table != out[j].Table {
			return out[i].Table < out[j].Table
		}
		return out[i].Check < out[j].Check
	})
	return out
}

// hasIndexPrefix reports whether some index starts with exactly the given
// columns (in any order), which is what a database needs to use it for an FK
// lookup.
func (t *Table) hasIndexPrefix(cols []string) bool {
	want := map[string]bool{}
	for _, c := range cols {
		want[c] = true
	}
	for _, idx := range t.Indexes {
		if len(idx.Columns) < len(cols) {
			continue
		}
		ok := true
		for _, c := range idx.Columns[:len(cols)] {
			if !want[c] {
				ok = false
				break
			}
		}
		if ok {
			return true
		}
	}
	return false
}

// hasUniqueOn reports whether a unique index covers exactly these columns.
func (t *Table) hasUniqueOn(cols []string) bool {
	for _, idx := range t.Indexes {
		if idx.Unique && sameSet(idx.Columns, cols) {
			return true
		}
	}
	return false
}

func sameSet(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	in := map[string]bool{}
	for _, x := range a {
		in[x] = true
	}
	for _, x := range b {
		if !in[x] {
			return false
		}
	}
	return true
}
