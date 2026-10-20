package main

import (
	"regexp"
	"strings"
	"unicode"

	"golang.org/x/text/unicode/norm"
)

var reSlugJunk = regexp.MustCompile(`[^a-z0-9]+`)

// slug makes a lowercase ASCII filename stem: accents stripped, punctuation → '-'.
func slug(s string) string {
	s = norm.NFKD.String(s)
	var b strings.Builder
	for _, r := range s {
		if unicode.Is(unicode.Mn, r) {
			continue
		}
		b.WriteRune(unicode.ToLower(r))
	}
	out := strings.Trim(reSlugJunk.ReplaceAllString(b.String(), "-"), "-")
	if out == "" {
		out = "untitled"
	}
	if len(out) > 80 {
		out = strings.Trim(out[:80], "-")
	}
	return out
}

func titleCase(s string) string {
	if s == "" {
		return s
	}
	r := []rune(s)
	r[0] = unicode.ToUpper(r[0])
	return string(r)
}
