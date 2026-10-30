package main

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/thinkovation/dbtools/dbinspect"
)

func fixture() *dbinspect.Schema {
	def := "now()"
	return &dbinspect.Schema{Dialect: "postgres", Name: "app", Tables: []*dbinspect.Table{
		{
			Name: "customers", Kind: "table", Comment: "Paying | accounts", PrimaryKey: []string{"id"},
			Columns: []*dbinspect.Column{
				{Name: "id", Type: "bigint"},
				{Name: "email", Type: "text"},
				{Name: "created_at", Type: "timestamptz", Default: &def},
			},
			Indexes: []*dbinspect.Index{
				{Name: "customers_pkey", Columns: []string{"id"}, Unique: true, Primary: true},
				{Name: "customers_email_key", Columns: []string{"email"}, Unique: true},
			},
		},
		{
			Name: "orders", Kind: "table", PrimaryKey: []string{"id"},
			Columns: []*dbinspect.Column{
				{Name: "id", Type: "bigint"},
				{Name: "customer_id", Type: "bigint", Comment: "line one\nline two"},
			},
			ForeignKeys: []*dbinspect.ForeignKey{{
				Name: "orders_customer_fk", Columns: []string{"customer_id"},
				RefTable: "customers", RefColumns: []string{"id"}, OnDelete: "CASCADE",
			}},
			Indexes: []*dbinspect.Index{{Name: "orders_pkey", Columns: []string{"id"}, Unique: true, Primary: true}},
		},
	}}
}

func TestBuildDoc(t *testing.T) {
	d, err := buildDoc(fixture(), "", true, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if d.Title != "app database" || d.TableCount != 2 || d.RelCount != 1 || d.Diagram == "" {
		t.Fatalf("unexpected summary: %+v", d)
	}
	customers := d.Tables[0]
	if len(customers.ReferencedBy) != 1 || customers.ReferencedBy[0].Table != "orders" {
		t.Errorf("customers should be referenced by orders: %+v", customers.ReferencedBy)
	}
	if customers.Columns[1].Keys != "UQ" {
		t.Errorf("email should be marked UQ, got %q", customers.Columns[1].Keys)
	}
	if len(customers.Indexes) != 1 {
		t.Errorf("primary index should be omitted from index list: %+v", customers.Indexes)
	}
	cid := d.Tables[1].Columns[1]
	if cid.Keys != "FK" || len(cid.References) != 1 || cid.References[0] != (ColumnRef{"customers.id", "table-customers"}) {
		t.Errorf("customer_id FK not resolved: %+v", cid)
	}
}

func TestRenderMarkdown(t *testing.T) {
	d, _ := buildDoc(fixture(), "Shop", true, time.Time{})
	var buf bytes.Buffer
	if err := renderMarkdown(&buf, d); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	for _, want := range []string{
		"# Shop",
		"```mermaid\nerDiagram",
		`Paying \| accounts`, // pipe escaped in table cell
		"| `customer_id` | `bigint` | no |  | FK → [customers.id](#table-customers) | line one line two |",
		"- [orders](#table-orders) (customer_id), on delete CASCADE",
		"unindexed-foreign-key",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("markdown missing %q\n%s", want, out)
		}
	}
	if strings.Contains(out, "Generated") {
		t.Error("zero timestamp should omit the generated line")
	}
}

func TestRenderHTML(t *testing.T) {
	s := fixture()
	s.Tables[0].Comment = "<script>alert(1)</script>"
	d, _ := buildDoc(s, "", false, time.Time{})
	var buf bytes.Buffer
	if err := renderHTML(&buf, d); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	if strings.Contains(out, "<script>alert(1)") {
		t.Error("table comment was not HTML-escaped")
	}
	if strings.Contains(out, "mermaid") {
		t.Error("diagram disabled but mermaid still included")
	}
	if !strings.Contains(out, `<a href="#table-customers">customers.id</a>`) {
		t.Error("FK link missing from HTML output")
	}
}

func TestFormatFromPath(t *testing.T) {
	for path, want := range map[string]string{"": "markdown", "x.md": "markdown", "x.HTML": "html", "d/x.htm": "html"} {
		if got := formatFromPath(path); got != want {
			t.Errorf("formatFromPath(%q) = %q, want %q", path, got, want)
		}
	}
}

func TestBuildDocReferences(t *testing.T) {
	s := fixture()
	orders := s.Table("orders")
	// Same column in two FKs, one to a same-named table in another schema.
	orders.ForeignKeys = append(orders.ForeignKeys, &dbinspect.ForeignKey{
		Name: "orders_billing_fk", Columns: []string{"customer_id"},
		RefSchema: "billing", RefTable: "customers", RefColumns: []string{"id"},
	})
	// "a b" and "a-b" would otherwise share an anchor.
	s.Tables = append(s.Tables,
		&dbinspect.Table{Name: "a b", Kind: "table"},
		&dbinspect.Table{Name: "a-b", Kind: "table"})

	d, err := buildDoc(s, "", false, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	refs := d.Tables[1].Columns[1].References
	want := []ColumnRef{{"customers.id", "table-customers"}, {"billing.customers.id", ""}}
	if len(refs) != 2 || refs[0] != want[0] || refs[1] != want[1] {
		t.Errorf("column references = %+v, want %+v", refs, want)
	}
	if ext := d.Tables[1].References[1]; ext.Anchor != "" {
		t.Errorf("cross-schema reference should not link locally: %+v", ext)
	}
	if a, b := d.Tables[2].Anchor, d.Tables[3].Anchor; a == b {
		t.Errorf("tables %q and %q share anchor %q", d.Tables[2].Name, d.Tables[3].Name, a)
	}
}

func TestMarkdownEscaping(t *testing.T) {
	for in, want := range map[string]string{
		"a|b":       `a\|b`,
		"x\ry\r\nz": "x y z",
	} {
		if got := escapeMarkdownCell(in); got != want {
			t.Errorf("escapeMarkdownCell(%q) = %q, want %q", in, got, want)
		}
	}
	for in, want := range map[string]string{
		"## Findings": `\## Findings`,
		"> quoted":    `\> quoted`,
		"1. first":    `1\. first`,
		"plain\n# x":  "plain # x",
	} {
		if got := escapeMarkdownText(in); got != want {
			t.Errorf("escapeMarkdownText(%q) = %q, want %q", in, got, want)
		}
	}
	for in, want := range map[string]string{
		"id":       "`id`",
		"part`id":  "``part`id``",
		"`quoted`": "`` `quoted` ``",
	} {
		if got := markdownCode(in); got != want {
			t.Errorf("markdownCode(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestWriteFileAtomicKeepsOldFileOnError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "out.md")
	if err := os.WriteFile(path, []byte("previous"), 0o644); err != nil {
		t.Fatal(err)
	}
	err := writeFileAtomic(path, func(w io.Writer) error {
		io.WriteString(w, "partial")
		return errors.New("boom")
	})
	if err == nil {
		t.Fatal("expected error")
	}
	if b, _ := os.ReadFile(path); string(b) != "previous" {
		t.Errorf("existing file was modified: %q", b)
	}
	if entries, _ := os.ReadDir(filepath.Dir(path)); len(entries) != 1 {
		t.Errorf("temporary file left behind: %v", entries)
	}
	if err := writeFileAtomic(path, func(w io.Writer) error { _, err := io.WriteString(w, "new"); return err }); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(path); string(b) != "new" {
		t.Errorf("file not replaced: %q", b)
	}
}
