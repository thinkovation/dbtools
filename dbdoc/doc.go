package main

import (
	"bytes"
	"sort"
	"strings"
	"time"

	"github.com/thinkovation/dbtools/dbinspect"
)

// Doc is the view model the templates render. It flattens the schema into
// what a reader needs per table, including inbound references, which the
// schema only records on the referencing side.
type Doc struct {
	Title       string
	Generated   time.Time
	Dialect     string
	Schema      string
	Tables      []*TableDoc
	Diagram     string // Mermaid source; empty when disabled
	Findings    []dbinspect.Finding
	TableCount  int
	ViewCount   int
	RelCount    int
	ColumnCount int
}

type TableDoc struct {
	*dbinspect.Table
	Anchor       string
	Columns      []ColumnDoc
	Indexes      []*dbinspect.Index // non-primary only
	References   []Ref
	ReferencedBy []Ref
	Findings     []dbinspect.Finding
}

type ColumnDoc struct {
	*dbinspect.Column
	Keys       string // "PK", "FK", "PK, FK", "UQ"
	References string // "table.column" for FK columns
	RefAnchor  string
}

// Ref is one side of a foreign-key relationship as seen from a table.
type Ref struct {
	Table      string
	Anchor     string // empty when the table isn't documented here
	Columns    string // this table's columns
	RefColumns string // other table's columns
	Name       string
	OnDelete   string
}

func buildDoc(s *dbinspect.Schema, title string, diagram bool, now time.Time) (*Doc, error) {
	report := dbinspect.NewReport(s)
	d := &Doc{
		Title: title, Generated: now, Dialect: s.Dialect, Schema: s.Name,
		Findings: report.Findings, RelCount: len(report.Relationships),
	}
	if d.Title == "" {
		d.Title = s.Name + " database"
	}
	if diagram && len(report.Relationships) > 0 {
		var buf bytes.Buffer
		if err := report.WriteMermaid(&buf); err != nil {
			return nil, err
		}
		d.Diagram = buf.String()
	}

	anchors := map[string]string{}
	for _, t := range s.Tables {
		anchors[t.Name] = anchor(t.Name)
	}
	findings := map[string][]dbinspect.Finding{}
	for _, f := range report.Findings {
		findings[f.Table] = append(findings[f.Table], f)
	}
	inbound := map[string][]Ref{}
	for _, t := range s.Tables {
		for _, fk := range t.ForeignKeys {
			if fk.RefSchema != "" {
				continue
			}
			inbound[fk.RefTable] = append(inbound[fk.RefTable], Ref{
				Table: t.Name, Anchor: anchors[t.Name], Name: fk.Name, OnDelete: fk.OnDelete,
				Columns: strings.Join(fk.RefColumns, ", "), RefColumns: strings.Join(fk.Columns, ", "),
			})
		}
	}

	for _, t := range s.Tables {
		if t.Kind == "view" || t.Kind == "materialized view" {
			d.ViewCount++
		} else {
			d.TableCount++
		}
		d.ColumnCount += len(t.Columns)
		td := &TableDoc{
			Table: t, Anchor: anchors[t.Name],
			ReferencedBy: inbound[t.Name], Findings: findings[t.Name],
		}
		sort.Slice(td.ReferencedBy, func(i, j int) bool { return td.ReferencedBy[i].Table < td.ReferencedBy[j].Table })

		keys := map[string][]string{}
		for _, c := range t.PrimaryKey {
			keys[c] = append(keys[c], "PK")
		}
		fkTarget := map[string][2]string{}
		for _, fk := range t.ForeignKeys {
			target := fk.RefTable
			if fk.RefSchema != "" {
				target = fk.RefSchema + "." + fk.RefTable
			}
			for i, c := range fk.Columns {
				keys[c] = append(keys[c], "FK")
				fkTarget[c] = [2]string{target + "." + fk.RefColumns[i], anchors[fk.RefTable]}
			}
			td.References = append(td.References, Ref{
				Table: target, Anchor: anchors[fk.RefTable], Name: fk.Name, OnDelete: fk.OnDelete,
				Columns: strings.Join(fk.Columns, ", "), RefColumns: strings.Join(fk.RefColumns, ", "),
			})
		}
		for _, idx := range t.Indexes {
			if idx.Primary {
				continue
			}
			td.Indexes = append(td.Indexes, idx)
			if idx.Unique && len(idx.Columns) == 1 && len(keys[idx.Columns[0]]) == 0 {
				keys[idx.Columns[0]] = append(keys[idx.Columns[0]], "UQ")
			}
		}
		for _, c := range t.Columns {
			cd := ColumnDoc{Column: c, Keys: strings.Join(dedupe(keys[c.Name]), ", ")}
			if tgt, ok := fkTarget[c.Name]; ok {
				cd.References, cd.RefAnchor = tgt[0], tgt[1]
			}
			td.Columns = append(td.Columns, cd)
		}
		d.Tables = append(d.Tables, td)
	}
	return d, nil
}

// anchor makes a stable, URL-safe fragment id for a table heading.
func anchor(name string) string {
	var b strings.Builder
	b.WriteString("table-")
	for _, r := range strings.ToLower(name) {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '_' || r == '-' {
			b.WriteRune(r)
		} else {
			b.WriteRune('-')
		}
	}
	return b.String()
}

func dedupe(xs []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, x := range xs {
		if !seen[x] {
			seen[x] = true
			out = append(out, x)
		}
	}
	return out
}
