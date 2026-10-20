package main

import (
	"regexp"
	"strings"
)

// Converter turns Notion-flavored Markdown (GET /pages/{id}/markdown) into plain
// CommonMark/GFM. Notion emits one block per line with no blank lines, tab-indented
// nesting, <callout>, <table>/<tr>/<td>, <mention-page> and <page> tags, and
// backslash-escapes on many punctuation characters.
type Converter struct {
	// Link resolves a Notion page id (dashed) to a markdown link target relative to
	// the document being rendered, plus the page title. ok=false if unknown.
	Link func(id string) (href, title string, ok bool)
}

var (
	reMentionPage  = regexp.MustCompile(`<mention-page url="https?://[^"]*?([0-9a-f]{32})[^"]*"\s*(?:/>|>(.*?)</mention-page>)`)
	rePageTag      = regexp.MustCompile(`^<page url="https?://[^"]*?([0-9a-f]{32})[^"]*">(.*?)</page>\s*$`)
	reMentionOther = regexp.MustCompile(`<mention-(?:user|date|database|data-source|block)\b[^>]*?(?:/>|>(.*?)</mention-[a-z-]+>)`)
	reCalloutOpen  = regexp.MustCompile(`^<callout(?:\s+icon="([^"]*)")?[^>]*>\s*$`)
	reTableOpen    = regexp.MustCompile(`^<table\b([^>]*)>\s*$`)
	reHeading      = regexp.MustCompile(`^#{1,6}\s`)
	reList         = regexp.MustCompile(`^(?:[-*+]\s|\d+\.\s)`)
	reFence        = regexp.MustCompile("^```(.*)$")
	reHR           = regexp.MustCompile(`^(?:---|\*\*\*|___)\s*$`)
	reCodeSpan     = regexp.MustCompile("`+[^`]*`+")
	reEscTable     = regexp.MustCompile(`^\\\|.*\\\|\s*$`)
)

var calloutKinds = map[string]string{
	"ℹ️": "info", "ℹ": "info", "📝": "note", "💡": "tip", "⚠️": "warning", "⚠": "warning",
	"🛑": "danger", "❗": "danger", "🚨": "danger", "✅": "success", "❓": "question", "❌": "failure",
	"🔥": "danger", "📌": "note", "🧭": "info", "🐛": "bug", "💬": "quote", "📖": "example",
}

// Convert renders the whole body. Output has a single trailing newline, or is empty.
func (c *Converter) Convert(src string) string {
	lines := strings.Split(strings.ReplaceAll(src, "\r\n", "\n"), "\n")
	out := c.convertLines(lines)
	// trim trailing blanks
	for len(out) > 0 && strings.TrimSpace(out[len(out)-1]) == "" {
		out = out[:len(out)-1]
	}
	if len(out) == 0 {
		return ""
	}
	return strings.Join(out, "\n") + "\n"
}

type blockKind int

const (
	kNone blockKind = iota
	kPara
	kList
	kHeading
	kQuote
	kFence
	kTable
	kCallout
	kHR
	kPageList
)

// convertLines converts a sequence of Notion lines at one nesting context.
func (c *Converter) convertLines(lines []string) []string { return c.convertCtx(lines, false) }

// convertCtx is convertLines with joinProse: inside a callout Notion keeps one block
// per hard-wrapped source line, so consecutive prose lines stay one paragraph.
func (c *Converter) convertCtx(lines []string, joinProse bool) []string {
	var out []string
	last := kNone
	emit := func(kind blockKind, ls ...string) {
		if len(out) > 0 {
			sep := true
			switch {
			case kind == kList && last == kList:
				sep = false
			case kind == kPageList && last == kPageList:
				sep = false
			case joinProse && kind == kPara && last == kPara:
				sep = false
			case kind == kTable && last == kTable:
				sep = false
			}
			if sep {
				out = append(out, "")
			}
		}
		out = append(out, ls...)
		last = kind
	}

	for i := 0; i < len(lines); i++ {
		raw := lines[i]
		line := strings.TrimRight(raw, " \t")
		if strings.TrimSpace(line) == "" {
			continue
		}
		indent, body := splitIndent(line)

		// fenced code: copy verbatim until closing fence
		if m := reFence.FindStringSubmatch(body); m != nil {
			lang := strings.TrimSpace(m[1])
			if lang == "plain text" || lang == "plain_text" {
				lang = ""
			}
			block := []string{indent + "```" + lang}
			j := i + 1
			for ; j < len(lines); j++ {
				_, b := splitIndent(strings.TrimRight(lines[j], " \t"))
				if strings.HasPrefix(b, "```") {
					break
				}
				block = append(block, reindent(lines[j]))
			}
			block = append(block, indent+"```")
			emit(kFence, block...)
			i = j
			continue
		}

		// callout
		if m := reCalloutOpen.FindStringSubmatch(body); m != nil {
			j := i + 1
			var inner []string
			for ; j < len(lines); j++ {
				if strings.TrimSpace(lines[j]) == "</callout>" {
					break
				}
				inner = append(inner, stripOneTab(lines[j]))
			}
			emit(kCallout, prefixAll(indent, c.renderCallout(m[1], inner))...)
			i = j
			continue
		}

		// table
		if m := reTableOpen.FindStringSubmatch(body); m != nil {
			headerRow := strings.Contains(m[1], `header-row="true"`)
			rows, j := parseTable(lines, i+1)
			emit(kTable, prefixAll(indent, c.renderTable(rows, headerRow))...)
			i = j
			continue
		}

		// child page tag → list item link
		if m := rePageTag.FindStringSubmatch(body); m != nil {
			id := dashed(m[1])
			title := c.inline(m[2])
			href := "https://www.notion.so/" + m[1]
			if h, t, ok := c.Link(id); ok {
				href = h
				if title == "" {
					title = t
				}
			}
			emit(kPageList, indent+"- ["+escapeLinkText(title)+"]("+href+")")
			continue
		}

		switch {
		case reHR.MatchString(body):
			emit(kHR, indent+"---")
		case reHeading.MatchString(body):
			emit(kHeading, indent+c.inline(body))
		case strings.HasPrefix(body, "> "), body == ">":
			text := strings.TrimPrefix(strings.TrimPrefix(body, ">"), " ")
			parts := strings.Split(c.inline(text), "<br>")
			var ls []string
			for _, p := range parts {
				ls = append(ls, indent+"> "+strings.TrimSpace(p))
			}
			emit(kQuote, ls...)
		case reList.MatchString(body):
			parts := strings.Split(c.inline(body), "<br>")
			marker := reList.FindString(body)
			cont := strings.Repeat(" ", len(marker))
			ls := []string{indent + parts[0]}
			for _, p := range parts[1:] {
				ls = append(ls, indent+cont+strings.TrimSpace(p))
			}
			emit(kList, ls...)
		default:
			parts := strings.Split(c.inline(body), "<br>")
			var ls []string
			for _, p := range parts {
				ls = append(ls, indent+strings.TrimSpace(p))
			}
			if joinProse && strings.HasPrefix(ls[0], "|") {
				// a pipe-table row that Notion flattened to text inside a callout
				emit(kTable, ls...)
				continue
			}
			if last == kList && indent != "" {
				// tab-indented continuation under a list item stays attached
				out = append(out, "")
				out = append(out, ls...)
				last = kList
				continue
			}
			emit(kPara, ls...)
		}
	}
	return out
}

func (c *Converter) renderCallout(icon string, inner []string) []string {
	kind := calloutKinds[icon]
	if kind == "" {
		kind = "note"
	}
	// unescape pipe tables that Notion flattened to text inside the callout
	for i, l := range inner {
		_, b := splitIndent(l)
		if reEscTable.MatchString(b) {
			inner[i] = strings.ReplaceAll(l, `\|`, "|")
		}
	}
	title := ""
	if len(inner) > 0 {
		first := strings.ReplaceAll(strings.TrimSpace(inner[0]), "****", "")
		if strings.HasPrefix(first, "**") && strings.HasSuffix(first, "**") && !strings.Contains(first, "<br>") && strings.Count(first, "**") == 2 {
			title = c.inline(strings.TrimSuffix(strings.TrimPrefix(first, "**"), "**"))
			inner = inner[1:]
		} else if strings.HasPrefix(first, "**") && strings.Contains(first, "<br>") {
			// "**Title<br>body**" — bold title joined with its body on one line
			parts := strings.SplitN(first, "<br>", 2)
			t := strings.TrimPrefix(parts[0], "**")
			rest := strings.TrimSuffix(parts[1], "**")
			if strings.Count(t, "**")%2 == 0 {
				title = c.inline(strings.TrimSpace(t))
				inner = append([]string{rest}, inner[1:]...)
			}
		}
	}
	bodyLines := c.convertCtx(inner, true)
	out := []string{"> [!" + kind + "]" + func() string {
		if title != "" {
			return " " + title
		}
		return ""
	}()}
	for _, l := range bodyLines {
		if l == "" {
			out = append(out, ">")
		} else {
			out = append(out, "> "+l)
		}
	}
	return out
}

// parseTable consumes <tr>/<td> lines starting at index i until </table>.
// Returns rows of cells and the index of the closing tag.
func parseTable(lines []string, i int) ([][]string, int) {
	var rows [][]string
	var row []string
	var cell strings.Builder
	inCell := false
	for ; i < len(lines); i++ {
		l := strings.TrimSpace(lines[i])
		if inCell {
			if idx := strings.Index(l, "</td>"); idx >= 0 {
				cell.WriteString("<br>" + l[:idx])
				row = append(row, cell.String())
				inCell = false
			} else {
				cell.WriteString("<br>" + l)
			}
			continue
		}
		switch {
		case l == "</table>":
			if row != nil {
				rows = append(rows, row)
			}
			return rows, i
		case l == "<tr>":
			row = []string{}
		case l == "</tr>":
			rows = append(rows, row)
			row = nil
		case strings.HasPrefix(l, "<td>"):
			rest := strings.TrimPrefix(l, "<td>")
			if idx := strings.Index(rest, "</td>"); idx >= 0 {
				row = append(row, rest[:idx])
			} else {
				cell.Reset()
				cell.WriteString(rest)
				inCell = true
			}
		}
	}
	if row != nil {
		rows = append(rows, row)
	}
	return rows, i
}

func (c *Converter) renderTable(rows [][]string, headerRow bool) []string {
	if len(rows) == 0 {
		return nil
	}
	width := 0
	for _, r := range rows {
		if len(r) > width {
			width = len(r)
		}
	}
	if width == 0 {
		return nil
	}
	line := func(cells []string) string {
		var b strings.Builder
		b.WriteString("|")
		for i := 0; i < width; i++ {
			v := ""
			if i < len(cells) {
				v = c.inlineCell(cells[i])
			}
			b.WriteString(" " + v + " |")
		}
		return b.String()
	}
	var out []string
	if headerRow {
		out = append(out, line(rows[0]))
		rows = rows[1:]
	} else {
		out = append(out, line(make([]string, width)))
	}
	out = append(out, "|"+strings.Repeat(" --- |", width))
	for _, r := range rows {
		out = append(out, line(r))
	}
	return out
}

// inlineCell converts cell text; pipes stay escaped and <br> stays for line breaks.
func (c *Converter) inlineCell(s string) string {
	s = c.inlineKeepPipes(strings.TrimSpace(s))
	s = strings.ReplaceAll(s, "\n", "<br>")
	return s
}

// inline converts inline syntax outside code spans: mentions → links, unescape.
func (c *Converter) inline(s string) string {
	return strings.ReplaceAll(c.inlineKeepPipes(s), `\|`, "|")
}

func (c *Converter) inlineKeepPipes(s string) string {
	var b strings.Builder
	last := 0
	for _, m := range reCodeSpan.FindAllStringIndex(s, -1) {
		b.WriteString(c.inlineText(s[last:m[0]]))
		b.WriteString(s[m[0]:m[1]])
		last = m[1]
	}
	b.WriteString(c.inlineText(s[last:]))
	return b.String()
}

func (c *Converter) inlineText(s string) string {
	s = reMentionPage.ReplaceAllStringFunc(s, func(m string) string {
		sm := reMentionPage.FindStringSubmatch(m)
		id := dashed(sm[1])
		text := strings.TrimSpace(sm[2])
		href, title, ok := c.Link(id)
		if !ok {
			href = "https://www.notion.so/" + sm[1]
			title = "Notion page"
		}
		if text == "" {
			text = title
		}
		return "[" + escapeLinkText(text) + "](" + href + ")"
	})
	s = reMentionOther.ReplaceAllStringFunc(s, func(m string) string {
		sm := reMentionOther.FindStringSubmatch(m)
		if sm[1] != "" {
			return sm[1]
		}
		return ""
	})
	// Notion serializes bold spanning inline code as "**text ****`code`**": drop the "****"
	s = strings.ReplaceAll(s, "****", "")
	// unescape punctuation that carries no markdown meaning in prose
	r := strings.NewReplacer(`\$`, "$", `\~`, "~", `\"`, `"`, `\{`, "{", `\}`, "}", `\^`, "^", `\%`, "%", `\:`, ":", `\,`, ",")
	s = r.Replace(s)
	s = unescapeAngle(s)
	return s
}

// unescapeAngle drops the backslash before > and < where the result cannot start
// a blockquote or an HTML tag.
func unescapeAngle(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+1 < len(s) && (s[i+1] == '>' || s[i+1] == '<') {
			next := byte(' ')
			if i+2 < len(s) {
				next = s[i+2]
			}
			atStart := strings.TrimSpace(b.String()) == ""
			if s[i+1] == '>' && !atStart {
				b.WriteByte('>')
				i++
				continue
			}
			if s[i+1] == '<' && (!isAlpha(next) && next != '/' && next != '!' && next != '?') {
				b.WriteByte('<')
				i++
				continue
			}
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

func isAlpha(c byte) bool { return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') }

func escapeLinkText(s string) string {
	return strings.NewReplacer("[", `\[`, "]", `\]`).Replace(s)
}

// splitIndent separates leading tabs (Notion nesting) from the line, as 4-space units.
func splitIndent(l string) (indent, body string) {
	n := 0
	for n < len(l) && l[n] == '\t' {
		n++
	}
	return strings.Repeat("    ", n), l[n:]
}

func reindent(l string) string {
	ind, body := splitIndent(l)
	return ind + body
}

func stripOneTab(l string) string {
	return strings.TrimPrefix(l, "\t")
}

func prefixAll(prefix string, ls []string) []string {
	if prefix == "" {
		return ls
	}
	out := make([]string, len(ls))
	for i, l := range ls {
		if l == "" {
			out[i] = ""
		} else {
			out[i] = prefix + l
		}
	}
	return out
}
