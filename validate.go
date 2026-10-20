package main

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// Validator checks a bundle against OKF 0.2 (SPEC §11) plus the OpenKnowledge okf
// plugin's advisory rules (timestamps with offsets, status vocabulary, index and
// log shape, reserved casing, no wiki links, no .mdx).
type Finding struct {
	File, Rule, Msg string
}

func (f Finding) String() string { return fmt.Sprintf("%s: %s: %s", f.File, f.Rule, f.Msg) }

var (
	reFrontmatter = regexp.MustCompile(`(?s)\A---\r?\n(.*?)\r?\n---\r?\n?`)
	reTimestamp   = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(\.\d+)?(Z|[+-]\d{2}:\d{2})$`)
	reLogDate     = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`)
	reWikiLink    = regexp.MustCompile(`\[\[[^\]]+\]\]`)
	reIndexEntry  = regexp.MustCompile(`^\s*[*+-]\s+(.*)$`)
	reMDLink      = regexp.MustCompile(`\[(?:\\.|[^\]\\])*\]\([^)]+\)`)
	reFenceLine   = regexp.MustCompile("^\\s*```")
)

// ValidateFiles validates an in-memory bundle (path → content).
func ValidateFiles(files map[string]string) []Finding {
	var out []Finding
	paths := make([]string, 0, len(files))
	for p := range files {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	for _, p := range paths {
		out = append(out, validateOne(p, files[p])...)
	}
	return out
}

// ValidateDir validates a bundle on disk.
func ValidateDir(root string) ([]Finding, error) {
	files := map[string]string{}
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, _ := filepath.Rel(root, p)
		rel = filepath.ToSlash(rel)
		if strings.HasSuffix(rel, ".mdx") {
			files[rel] = ""
			return nil
		}
		if !strings.HasSuffix(rel, ".md") {
			return nil
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		files[rel] = string(b)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return ValidateFiles(files), nil
}

func validateOne(p, content string) []Finding {
	var f []Finding
	add := func(rule, msg string) { f = append(f, Finding{p, rule, msg}) }
	base := filepath.Base(p)
	if strings.HasSuffix(p, ".mdx") {
		add("project-no-mdx", ".mdx files are invisible to OKF consumers")
		return f
	}
	if strings.EqualFold(base, "index.md") && base != "index.md" || strings.EqualFold(base, "log.md") && base != "log.md" {
		add("reserved-casing", "reserved filename must be lowercase")
	}
	fmText, body, hasFM := splitFrontmatter(content)
	switch base {
	case "index.md":
		if hasFM {
			if !strings.Contains(p, "/") {
				var m map[string]any
				if err := yaml.Unmarshal([]byte(fmText), &m); err != nil || len(m) != 1 || m["okf_version"] != "0.2" {
					add("frontmatter-root-index", `root index frontmatter must be exactly okf_version: "0.2"`)
				}
			} else {
				add("frontmatter-reserved-index", "index.md must not carry frontmatter")
			}
		}
		f = append(f, checkIndexShape(p, body)...)
		return f
	case "log.md":
		if hasFM {
			add("frontmatter-reserved-log", "log.md must not carry frontmatter")
		}
		f = append(f, checkLogShape(p, body)...)
		return f
	}
	if !hasFM {
		add("frontmatter-required", "frontmatter absent")
		return f
	}
	var node yaml.Node
	if err := yaml.Unmarshal([]byte(fmText), &node); err != nil || len(node.Content) == 0 || node.Content[0].Kind != yaml.MappingNode {
		add("frontmatter-required", "frontmatter is not a YAML mapping")
		return f
	}
	m := node.Content[0]
	get := func(key string) *yaml.Node {
		for i := 0; i+1 < len(m.Content); i += 2 {
			if m.Content[i].Value == key {
				return m.Content[i+1]
			}
		}
		return nil
	}
	isStr := func(n *yaml.Node) bool { return n != nil && n.Kind == yaml.ScalarNode && n.ShortTag() == "!!str" }
	nonEmptyStr := func(n *yaml.Node) bool { return isStr(n) && strings.TrimSpace(n.Value) != "" }
	isTime := func(n *yaml.Node) bool {
		return n != nil && n.Kind == yaml.ScalarNode && reTimestamp.MatchString(n.Value)
	}

	if t := get("type"); t == nil || !nonEmptyStr(t) {
		add("frontmatter-required", "`type` missing or not a non-empty string")
	}
	for _, k := range []string{"title", "description", "resource"} {
		if n := get(k); n != nil && !isStr(n) {
			add("frontmatter-recommended", "`"+k+"` must be a string")
		}
	}
	if n := get("tags"); n != nil {
		if n.Kind != yaml.SequenceNode {
			add("frontmatter-recommended", "`tags` must be a list")
		} else {
			for _, c := range n.Content {
				if !isStr(c) {
					add("frontmatter-recommended", "`tags` entries must be strings")
					break
				}
			}
		}
	}
	if n := get("status"); n != nil {
		switch n.Value {
		case "draft", "stable", "deprecated":
		default:
			add("frontmatter-provenance", "`status` must be draft, stable or deprecated")
		}
	}
	if n := get("stale_after"); n != nil && !isTime(n) {
		add("frontmatter-provenance", "`stale_after` must be an ISO 8601 datetime with offset")
	}
	if n := get("generated"); n != nil {
		if n.Kind != yaml.MappingNode {
			add("frontmatter-provenance", "`generated` must be a mapping")
		} else {
			by, at := mapGet(n, "by"), mapGet(n, "at")
			if !nonEmptyStr(by) {
				add("frontmatter-provenance", "`generated.by` is required")
			}
			if at != nil && !isTime(at) {
				add("frontmatter-provenance", "`generated.at` must be an ISO 8601 datetime with offset")
			}
		}
	}
	if n := get("verified"); n != nil {
		var items []*yaml.Node
		switch n.Kind {
		case yaml.MappingNode:
			items = []*yaml.Node{n}
		case yaml.SequenceNode:
			items = n.Content
		default:
			add("frontmatter-provenance", "`verified` must be a mapping or a list of mappings")
		}
		for _, it := range items {
			if it.Kind != yaml.MappingNode || !nonEmptyStr(mapGet(it, "by")) || !isTime(mapGet(it, "at")) {
				add("frontmatter-provenance", "each `verified` entry needs `by` and an ISO 8601 `at`")
			}
		}
	}
	if n := get("sources"); n != nil {
		if n.Kind != yaml.SequenceNode {
			add("frontmatter-provenance", "`sources` must be a list")
		} else {
			for i, it := range n.Content {
				if it.Kind != yaml.MappingNode {
					add("frontmatter-provenance", fmt.Sprintf("`sources[%d]` must be a mapping", i))
					continue
				}
				if !nonEmptyStr(mapGet(it, "resource")) {
					add("frontmatter-provenance", fmt.Sprintf("`sources[%d].resource` is required", i))
				}
				for _, k := range []string{"id", "title", "author"} {
					if v := mapGet(it, k); v != nil && !isStr(v) {
						add("frontmatter-provenance", fmt.Sprintf("`sources[%d].%s` must be a string", i, k))
					}
				}
				if v := mapGet(it, "last_modified"); v != nil && !isTime(v) {
					add("frontmatter-provenance", fmt.Sprintf("`sources[%d].last_modified` must be an ISO 8601 datetime with offset", i))
				}
				if w := mapGet(it, "usage_window"); w != nil {
					f = append(f, checkWindow(p, w)...)
				}
			}
		}
	}
	if w := get("usage_window"); w != nil {
		f = append(f, checkWindow(p, w)...)
	}
	if t := get("type"); t != nil && t.Value == "Attested Computation" && !nonEmptyStr(get("runtime")) {
		add("frontmatter-computation", "Attested Computation needs `runtime`")
	}
	// body: wiki links outside code
	inFence := false
	for i, line := range strings.Split(body, "\n") {
		if reFenceLine.MatchString(line) {
			inFence = !inFence
			continue
		}
		if inFence {
			continue
		}
		stripped := reCodeSpan.ReplaceAllString(line, "")
		if reWikiLink.MatchString(stripped) {
			add("no-wiki-links", fmt.Sprintf("line %d: [[wiki-link]] outside code", i+1))
		}
	}
	return f
}

func checkWindow(p string, w *yaml.Node) []Finding {
	if w.Kind != yaml.MappingNode {
		return []Finding{{p, "frontmatter-provenance", "`usage_window` must be a mapping"}}
	}
	var f []Finding
	for _, k := range []string{"from", "to"} {
		if v := mapGet(w, k); v == nil || !reTimestamp.MatchString(v.Value) {
			f = append(f, Finding{p, "frontmatter-provenance", "`usage_window." + k + "` must be an ISO 8601 datetime with offset"})
		}
	}
	return f
}

func mapGet(m *yaml.Node, key string) *yaml.Node {
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			return m.Content[i+1]
		}
	}
	return nil
}

func splitFrontmatter(content string) (fm, body string, ok bool) {
	m := reFrontmatter.FindStringSubmatchIndex(content)
	if m == nil {
		return "", content, false
	}
	return content[m[2]:m[3]], content[m[1]:], true
}

func checkIndexShape(p, body string) []Finding {
	var f []Finding
	heading := false
	entries := 0
	for i, line := range strings.Split(body, "\n") {
		t := strings.TrimSpace(line)
		switch {
		case t == "":
		case strings.HasPrefix(t, "#"):
			heading = true
		case reIndexEntry.MatchString(t):
			if !heading {
				f = append(f, Finding{p, "index-shape", fmt.Sprintf("line %d: entry before any section heading", i+1)})
			}
			if !reMDLink.MatchString(t) {
				f = append(f, Finding{p, "index-shape", fmt.Sprintf("line %d: entry has no link", i+1)})
			}
			entries++
		default:
			f = append(f, Finding{p, "index-shape", fmt.Sprintf("line %d: index holds only headings and link lists", i+1)})
		}
	}
	if entries == 0 {
		f = append(f, Finding{p, "index-shape", "index lists nothing"})
	}
	return f
}

func checkLogShape(p, body string) []Finding {
	var f []Finding
	last := ""
	for i, line := range strings.Split(body, "\n") {
		if !strings.HasPrefix(line, "## ") {
			continue
		}
		h := strings.TrimSpace(strings.TrimPrefix(line, "## "))
		date := h
		if j := strings.Index(h, ":"); j >= 0 {
			date = h[:j]
		}
		if !reLogDate.MatchString(date) {
			f = append(f, Finding{p, "log-shape", fmt.Sprintf("line %d: heading is not an ISO date", i+1)})
			continue
		}
		if last != "" && date > last {
			f = append(f, Finding{p, "log-shape", fmt.Sprintf("line %d: entries must be newest first", i+1)})
		}
		last = date
	}
	return f
}
