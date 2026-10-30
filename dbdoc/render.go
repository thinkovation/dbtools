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
		"md":     escapeMarkdownCell,
		"mdtext": escapeMarkdownText,
		"code":   markdownCode,
		"join":   strings.Join,
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

var lineBreaks = strings.NewReplacer("\r\n", " ", "\r", " ", "\n", " ")

// escapeMarkdownCell makes text safe inside a Markdown table cell: pipes
// would end the cell and line breaks would end the row.
func escapeMarkdownCell(s string) string {
	return strings.ReplaceAll(lineBreaks.Replace(s), "|", `\|`)
}

// escapeMarkdownText makes free text (e.g. a table comment) render as a
// single paragraph, so a comment starting with "#", ">", "-" or "1." can't
// become a heading, quote or list.
func escapeMarkdownText(s string) string {
	s = strings.TrimSpace(lineBreaks.Replace(s))
	if s == "" {
		return s
	}
	if strings.ContainsRune("#>-+*=_|~`", rune(s[0])) {
		return `\` + s
	}
	if i := strings.IndexFunc(s, func(r rune) bool { return r < '0' || r > '9' }); i > 0 && (s[i] == '.' || s[i] == ')') {
		return s[:i] + `\` + s[i:]
	}
	return s
}

// markdownCode wraps s in a code span whose fence is longer than any run of
// backticks inside it, so names containing backticks render intact.
func markdownCode(s string) string {
	longest, run := 0, 0
	for _, r := range s {
		if r == '`' {
			run++
			longest = max(longest, run)
		} else {
			run = 0
		}
	}
	fence := strings.Repeat("`", longest+1)
	if strings.HasPrefix(s, "`") || strings.HasSuffix(s, "`") {
		s = " " + s + " "
	}
	return fence + s + fence
}
