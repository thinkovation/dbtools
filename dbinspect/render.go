package dbinspect

import (
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"strings"
	"text/tabwriter"
)

// Report bundles the schema with its analysis for serialisation.
type Report struct {
	Schema        *Schema        `json:"schema"`
	Relationships []Relationship `json:"relationships"`
	Findings      []Finding      `json:"findings"`
}

// NewReport inspects relationships and runs Analyze over s.
func NewReport(s *Schema) *Report {
	return &Report{Schema: s, Relationships: s.Relationships(), Findings: Analyze(s)}
}

// WriteJSON writes the report as indented JSON.
func (r *Report) WriteJSON(w io.Writer) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(r)
}

// WriteText writes a human-readable summary of every table followed by the
// findings.
func (r *Report) WriteText(w io.Writer) error {
	s := r.Schema
	fmt.Fprintf(w, "%s schema %q: %d tables/views, %d relationships\n\n",
		s.Dialect, s.Name, len(s.Tables), len(r.Relationships))

	for _, t := range s.Tables {
		fmt.Fprintf(w, "== %s (%s, ~%d rows)\n", t.Name, t.Kind, t.RowEstimate)
		if t.Comment != "" {
			fmt.Fprintf(w, "   %s\n", t.Comment)
		}
		pk := map[string]bool{}
		for _, c := range t.PrimaryKey {
			pk[c] = true
		}
		fkTarget := map[string]string{}
		for _, fk := range t.ForeignKeys {
			for i, c := range fk.Columns {
				fkTarget[c] = fk.RefTable + "." + fk.RefColumns[i]
			}
		}

		tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
		for _, c := range t.Columns {
			var notes []string
			if pk[c.Name] {
				notes = append(notes, "PK")
			}
			if tgt, ok := fkTarget[c.Name]; ok {
				notes = append(notes, "FK → "+tgt)
			}
			if !c.Nullable {
				notes = append(notes, "NOT NULL")
			}
			if c.Extra != "" {
				notes = append(notes, c.Extra)
			}
			if c.Default != nil {
				notes = append(notes, "default "+*c.Default)
			}
			fmt.Fprintf(tw, "   %s\t%s\t%s\n", c.Name, c.Type, strings.Join(notes, ", "))
		}
		tw.Flush()

		for _, idx := range t.Indexes {
			if idx.Primary {
				continue
			}
			kind := "index"
			if idx.Unique {
				kind = "unique"
			}
			fmt.Fprintf(w, "   %s %s (%s)\n", kind, idx.Name, strings.Join(idx.Columns, ", "))
		}
		fmt.Fprintln(w)
	}

	if len(r.Findings) == 0 {
		fmt.Fprintln(w, "No findings.")
		return nil
	}
	fmt.Fprintf(w, "Findings (%d):\n", len(r.Findings))
	for _, f := range r.Findings {
		fmt.Fprintf(w, "  [%s] %s: %s (%s)\n", f.Severity, f.Table, f.Message, f.Check)
	}
	return nil
}

var mermaidUnsafe = regexp.MustCompile(`[^A-Za-z0-9_-]+`)

func mermaidIdent(s string) string {
	return strings.Trim(mermaidUnsafe.ReplaceAllString(s, "_"), "_")
}

// WriteMermaid writes an entity-relationship diagram in Mermaid syntax, which
// renders directly in GitHub, GitLab, Confluence and most Markdown viewers.
func (r *Report) WriteMermaid(w io.Writer) error {
	fmt.Fprintln(w, "erDiagram")
	for _, t := range r.Schema.Tables {
		pk := map[string]bool{}
		for _, c := range t.PrimaryKey {
			pk[c] = true
		}
		fk := map[string]bool{}
		for _, f := range t.ForeignKeys {
			for _, c := range f.Columns {
				fk[c] = true
			}
		}
		fmt.Fprintf(w, "  %s {\n", mermaidIdent(t.Name))
		for _, c := range t.Columns {
			var keys []string
			if pk[c.Name] {
				keys = append(keys, "PK")
			}
			if fk[c.Name] {
				keys = append(keys, "FK")
			}
			fmt.Fprintf(w, "    %s %s", mermaidIdent(c.Type), mermaidIdent(c.Name))
			if len(keys) > 0 {
				fmt.Fprintf(w, " %s", strings.Join(keys, ","))
			}
			fmt.Fprintln(w)
		}
		fmt.Fprintln(w, "  }")
	}
	for _, rel := range r.Relationships {
		// Parent side: exactly one, or zero-or-one if the FK is nullable.
		left := "||"
		if rel.Optional {
			left = "|o"
		}
		// Child side: zero-or-one if the FK is unique, else zero-or-many.
		right := "o{"
		if rel.OneToOne {
			right = "o|"
		}
		fmt.Fprintf(w, "  %s %s--%s %s : %q\n",
			mermaidIdent(rel.To), left, right, mermaidIdent(rel.From), strings.Join(rel.FromCols, ", "))
	}
	return nil
}
