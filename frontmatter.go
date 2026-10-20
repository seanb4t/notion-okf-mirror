package main

import (
	"regexp"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// KV is one ordered frontmatter entry. Val may be string, []string, []KV (mapping),
// [][]KV (list of mappings), or nil to skip.
type KV struct {
	Key string
	Val any
}

var reAmbiguous = regexp.MustCompile(`^(?i)(true|false|yes|no|on|off|null|~|[-+]?[0-9][0-9_.,:eE+-]*|0x[0-9a-f]+|\d{4}-\d{2}-\d{2}.*)$`)

func scalar(s string) *yaml.Node {
	n := &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: s}
	switch {
	case s == "", reAmbiguous.MatchString(s),
		strings.ContainsAny(s, "\n#:`'\"{}[]|>&*!%@,?\\"),
		strings.HasPrefix(s, "-"), strings.HasPrefix(s, " "), strings.HasSuffix(s, " "):
		n.Style = yaml.DoubleQuotedStyle
	}
	return n
}

// ts emits an ISO 8601 timestamp as a plain scalar (parses as !!timestamp or string).
func ts(s string) *yaml.Node {
	return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: s}
}

type tsString string

func toNode(v any) *yaml.Node {
	switch x := v.(type) {
	case tsString:
		return ts(string(x))
	case string:
		return scalar(x)
	case int:
		return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!int", Value: strconv.Itoa(x)}
	case float64:
		if x == float64(int64(x)) {
			return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!int", Value: strconv.Itoa(int(x))}
		}
		return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!float", Value: strconv.FormatFloat(x, 'f', -1, 64)}
	case bool:
		if x {
			return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!bool", Value: "true"}
		}
		return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!bool", Value: "false"}
	case []string:
		n := &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq", Style: yaml.FlowStyle}
		for _, s := range x {
			n.Content = append(n.Content, scalar(s))
		}
		return n
	case []KV:
		return mapping(x, true)
	case [][]KV:
		n := &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
		for _, m := range x {
			n.Content = append(n.Content, mapping(m, false))
		}
		return n
	}
	return scalar("")
}

func mapping(kvs []KV, flow bool) *yaml.Node {
	n := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	if flow {
		n.Style = yaml.FlowStyle
	}
	for _, kv := range kvs {
		if kv.Val == nil {
			continue
		}
		if s, ok := kv.Val.(string); ok && s == "" {
			continue
		}
		if l, ok := kv.Val.([]string); ok && len(l) == 0 {
			continue
		}
		if t, ok := kv.Val.(tsString); ok && t == "" {
			continue
		}
		if l, ok := kv.Val.([][]KV); ok && len(l) == 0 {
			continue
		}
		n.Content = append(n.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: kv.Key}, toNode(kv.Val))
	}
	return n
}

func renderFrontmatter(kvs []KV) string {
	doc := &yaml.Node{Kind: yaml.DocumentNode, Content: []*yaml.Node{mapping(kvs, false)}}
	var b strings.Builder
	enc := yaml.NewEncoder(&b)
	enc.SetIndent(2)
	_ = enc.Encode(doc)
	enc.Close()
	return "---\n" + b.String() + "---\n"
}

// okfTime normalizes a Notion date/datetime into an ISO 8601 datetime with offset.
// A bare date becomes midnight UTC. Returns "" for empty input.
func okfTime(s string) tsString {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	if len(s) == 10 {
		return tsString(s + "T00:00:00Z")
	}
	if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
		return tsString(t.UTC().Format("2006-01-02T15:04:05Z07:00"))
	}
	return tsString(s)
}
