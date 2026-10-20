package main

import (
	"strings"
	"testing"
)

func testConverter() *Converter {
	return &Converter{Link: func(id string) (string, string, bool) {
		if id == "3fe27a0a-d688-8163-8aec-d3ac2e6670a9" {
			return "./xyra/build.md", "Xyra — Build", true
		}
		return "", "", false
	}}
}

func TestParagraphsAndLists(t *testing.T) {
	in := "First block.\nSecond block.\n- a\n- b\n\t- nested\nAfter list.\n## Heading\n1. one\n2. two"
	want := "First block.\n\nSecond block.\n\n- a\n- b\n    - nested\n\nAfter list.\n\n## Heading\n\n1. one\n2. two\n"
	if got := testConverter().Convert(in); got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestTable(t *testing.T) {
	in := "<table fit-page-width=\"true\" header-row=\"true\">\n<tr>\n<td>A</td>\n<td>B \\| C</td>\n</tr>\n<tr>\n<td>1</td>\n<td></td>\n</tr>\n</table>\nnext"
	want := "| A | B \\| C |\n| --- | --- |\n| 1 |  |\n\nnext\n"
	if got := testConverter().Convert(in); got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestCallout(t *testing.T) {
	in := "<callout icon=\"⚠️\">\n\t**Revert when done**\n\tline one\n\tline two\n\t\\| a \\| b \\|\n\t\\|---\\|---\\|\n\t\\| 1 \\| 2 \\|\n</callout>\ntail"
	want := "> [!warning] Revert when done\n> line one\n> line two\n>\n> | a | b |\n> |---|---|\n> | 1 | 2 |\n\ntail\n"
	if got := testConverter().Convert(in); got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestCalloutTitleJoinedWithBr(t *testing.T) {
	in := "<callout icon=\"📝\">\n\t**Why ****`confidence: medium`****<br>Not verified.**\n</callout>"
	want := "> [!note] Why `confidence: medium`\n> Not verified.\n"
	if got := testConverter().Convert(in); got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestMentionsAndEscapes(t *testing.T) {
	in := "See <mention-page url=\"https://app.notion.com/p/3fe27a0ad68881638aecd3ac2e6670a9\"/> and <mention-page url=\"https://app.notion.com/p/3fe27a0ad68881638aecd3ac2e6670a9\">alias</mention-page> and <mention-page url=\"https://app.notion.com/p/00000000000000000000000000000000\"/> cost \\$5 \\~ok `\\$keep` x \\> y"
	want := "See [Xyra — Build](./xyra/build.md) and [alias](./xyra/build.md) and [Notion page](https://www.notion.so/00000000000000000000000000000000) cost $5 ~ok `\\$keep` x > y\n"
	if got := testConverter().Convert(in); got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestFenceAndQuote(t *testing.T) {
	in := "```plain text\n<callout> \\$ raw\n```\n> quoted<br>continued\n<page url=\"https://app.notion.com/p/3fe27a0ad68881638aecd3ac2e6670a9\">Xyra — Build</page>"
	want := "```\n<callout> \\$ raw\n```\n\n> quoted\n> continued\n\n- [Xyra — Build](./xyra/build.md)\n"
	if got := testConverter().Convert(in); got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestRelLink(t *testing.T) {
	cases := map[[2]string]string{
		{"projects/a.md", "projects/b.md"}:                 "./b.md",
		{"projects/x/a.md", "sources/s.md"}:                "../../sources/s.md",
		{"characters/xyra.md", "characters/xyra/build.md"}: "./xyra/build.md",
		{"index.md", "projects/a.md"}:                      "./projects/a.md",
	}
	for in, want := range cases {
		if got := relLink(in[0], in[1]); got != want {
			t.Errorf("relLink(%q, %q) = %q, want %q", in[0], in[1], got, want)
		}
	}
}

func TestValidateCatchesBasics(t *testing.T) {
	files := map[string]string{
		"index.md":     "---\nokf_version: \"0.2\"\n---\n\n# A\n\n* [x](x.md)\n",
		"x.md":         "---\ntype: Note\ngenerated: { at: 2026-01-01T00:00:00Z }\nstale_after: 2026-01-01\n---\nbody [[wiki]]\n",
		"log.md":       "# Log\n\n## 2026-01-01\n\n* a\n\n## 2026-02-01\n\n* b\n",
		"sub/index.md": "---\ntype: X\n---\n# S\n\n* [y](y.md)\n",
	}
	got := map[string]bool{}
	for _, f := range ValidateFiles(files) {
		got[f.Rule] = true
	}
	for _, rule := range []string{"frontmatter-provenance", "no-wiki-links", "log-shape", "frontmatter-reserved-index"} {
		if !got[rule] {
			t.Errorf("expected finding %s; got %v", rule, got)
		}
	}
	if strings.Contains(strings.Join(func() []string {
		var o []string
		for _, f := range ValidateFiles(map[string]string{"ok.md": "---\ntype: Note\ntitle: T\n---\nfine\n"}) {
			o = append(o, f.String())
		}
		return o
	}(), ","), "ok.md") {
		t.Error("clean file produced findings")
	}
}
