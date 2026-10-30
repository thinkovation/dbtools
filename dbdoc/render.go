package main

import (
	"embed"
	htmltemplate "html/template"
	"io"
	"strings"
	texttemplate "text/template"
)

//go:embed templates/*.tmpl
var templates embed.FS

var (
	mdTemplate = texttemplate.Must(texttemplate.New("doc.md.tmpl").Funcs(texttemplate.FuncMap{
		"md":   escapeMarkdownCell,
		"join": strings.Join,
	}).ParseFS(templates, "templates/doc.md.tmpl"))

	htmlTemplate = htmltemplate.Must(htmltemplate.New("doc.html.tmpl").Funcs(htmltemplate.FuncMap{
		"join": strings.Join,
		"split": func(s string) []string {
			return strings.FieldsFunc(s, func(r rune) bool { return r == ',' || r == ' ' })
		},
	}).ParseFS(templates, "templates/doc.html.tmpl"))
)

func renderMarkdown(w io.Writer, d *Doc) error { return mdTemplate.Execute(w, d) }
func renderHTML(w io.Writer, d *Doc) error     { return htmlTemplate.Execute(w, d) }

// escapeMarkdownCell makes text safe inside a Markdown table cell: pipes
// would end the cell and newlines would end the row.
func escapeMarkdownCell(s string) string {
	s = strings.ReplaceAll(s, "|", `\|`)
	s = strings.ReplaceAll(s, "\r\n", " ")
	return strings.ReplaceAll(s, "\n", " ")
}
