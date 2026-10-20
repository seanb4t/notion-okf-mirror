package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Cache keeps the raw Notion markdown per page so unchanged pages are not refetched.
type Cache struct {
	dir   string
	index map[string]string // page id → last_edited_time (RFC3339)
	dirty bool
}

func OpenCache(dir string) *Cache {
	c := &Cache{dir: dir, index: map[string]string{}}
	if b, err := os.ReadFile(filepath.Join(dir, "index.json")); err == nil {
		_ = json.Unmarshal(b, &c.index)
	}
	return c
}

func (c *Cache) Get(id string, edited time.Time) (string, bool) {
	if c.index[id] != edited.UTC().Format(time.RFC3339Nano) {
		return "", false
	}
	b, err := os.ReadFile(filepath.Join(c.dir, id+".md"))
	if err != nil {
		return "", false
	}
	return string(b), true
}

func (c *Cache) Put(id string, edited time.Time, body string) {
	if err := os.MkdirAll(c.dir, 0o755); err != nil {
		return
	}
	if err := os.WriteFile(filepath.Join(c.dir, id+".md"), []byte(body), 0o644); err != nil {
		return
	}
	c.index[id] = edited.UTC().Format(time.RFC3339Nano)
	c.dirty = true
}

func (c *Cache) Save() error {
	if !c.dirty {
		return nil
	}
	b, _ := json.MarshalIndent(c.index, "", " ")
	return os.WriteFile(filepath.Join(c.dir, "index.json"), b, 0o644)
}

// State remembers what the mirror wrote last time, for the log and for removals.
type State struct {
	Pages   map[string]StatePage `json:"pages"`
	Log     []LogEntry           `json:"log"`
	LastRun string               `json:"last_run"`
}

type StatePage struct {
	Edited string `json:"edited"`
	File   string `json:"file"`
	Title  string `json:"title"`
}

type LogEntry struct {
	Date   string `json:"date"`
	Action string `json:"action"` // Creation | Update | Removal
	ID     string `json:"id"`
	Title  string `json:"title"`
	File   string `json:"file"`
}

func LoadState(p string) *State {
	s := &State{Pages: map[string]StatePage{}}
	if b, err := os.ReadFile(p); err == nil {
		_ = json.Unmarshal(b, s)
		if s.Pages == nil {
			s.Pages = map[string]StatePage{}
		}
	}
	return s
}

func (s *State) Save(p string) error {
	b, err := json.MarshalIndent(s, "", " ")
	if err != nil {
		return err
	}
	return os.WriteFile(p, b, 0o644)
}

// Bundle is the full set of files the mirror intends to have under okf/.
type Bundle struct {
	Files map[string]string // bundle-relative path → content
}

func dirTitle(k string, dbs map[string]Database) string {
	if d, ok := dbs[k]; ok && d.Title != "" {
		return d.Title
	}
	return titleCase(k)
}

// Build renders every doc, index.md per directory and the root log.md.
func (s *Snapshot) Build(state *State, now time.Time) (*Bundle, []LogEntry) {
	b := &Bundle{Files: map[string]string{}}
	for _, d := range s.Docs {
		b.Files[d.File] = s.Render(d)
	}

	// index.md per directory
	byDir := map[string][]*Doc{}
	subdirs := map[string]map[string]bool{}
	for _, d := range s.Docs {
		byDir[d.Dir] = append(byDir[d.Dir], d)
		parent := path.Dir(d.Dir)
		if parent == "." {
			parent = ""
		}
		if subdirs[parent] == nil {
			subdirs[parent] = map[string]bool{}
		}
		subdirs[parent][d.Dir] = true
	}
	for _, k := range topDirs {
		if subdirs[""] == nil {
			subdirs[""] = map[string]bool{}
		}
		subdirs[""][k] = true
	}
	allDirs := map[string]bool{}
	for d := range byDir {
		allDirs[d] = true
	}
	for p, subs := range subdirs {
		allDirs[p] = true
		for sd := range subs {
			allDirs[sd] = true
		}
	}
	for dir := range allDirs {
		if dir == "" {
			continue
		}
		b.Files[dir+"/index.md"] = s.renderIndex(dir, byDir[dir], subdirs[dir])
	}
	b.Files["index.md"] = s.renderRootIndex()

	// log from edit times, carried forward in state
	entries := s.logEntries(state, now)
	b.Files["log.md"] = renderLog(entries)
	return b, entries
}

var taskOrder = map[string]int{"Inbox": 0, "Next": 1, "Doing": 2, "Waiting": 3, "Done": 4, "Dropped": 5}

func (s *Snapshot) renderIndex(dir string, docs []*Doc, subs map[string]bool) string {
	var out strings.Builder
	kind := strings.SplitN(dir, "/", 2)[0]
	// group → docs
	groups := map[string][]*Doc{}
	for _, d := range docs {
		g := d.Group
		if g == "" {
			g = "Other"
		}
		groups[g] = append(groups[g], d)
	}
	names := make([]string, 0, len(groups))
	for g := range groups {
		names = append(names, g)
	}
	sort.Slice(names, func(i, j int) bool {
		a, bb := names[i], names[j]
		switch kind {
		case "tasks":
			return taskOrder[a] < taskOrder[bb]
		case "journal":
			return a > bb // newest month first
		case "sources":
			if (a == "Local runs") != (bb == "Local runs") {
				return bb == "Local runs"
			}
		}
		return a < bb
	})
	for _, g := range names {
		ds := groups[g]
		sort.Slice(ds, func(i, j int) bool {
			if ds[i].SortKey != ds[j].SortKey {
				switch kind {
				case "sources", "characters", "tasks", "projects":
					return ds[i].SortKey < ds[j].SortKey
				}
				return ds[i].SortKey > ds[j].SortKey // newest first (generated_at, date, period)
			}
			return ds[i].Title < ds[j].Title
		})
		fmt.Fprintf(&out, "# %s\n\n", escapeHeading(g))
		for _, d := range ds {
			fmt.Fprintf(&out, "* [%s](%s)%s\n", escapeLinkText(d.Title), path.Base(d.File), descSuffix(d.Desc))
		}
		out.WriteString("\n")
	}
	if len(subs) > 0 {
		keys := make([]string, 0, len(subs))
		for k := range subs {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		out.WriteString("# Directories\n\n")
		for _, sd := range keys {
			title := dirTitle(path.Base(sd), s.DBs)
			desc := ""
			if owner := s.ownerOf(sd); owner != nil {
				title = owner.Title
				desc = fmt.Sprintf("%d notes", len(byDirCount(s, sd)))
				if owner.Kind == "characters" {
					desc = "companion notes"
				}
			} else if db, ok := s.DBs[path.Base(sd)]; ok {
				desc = db.Description
			}
			fmt.Fprintf(&out, "* [%s](%s/)%s\n", escapeLinkText(title), path.Base(sd), descSuffix(desc))
		}
		out.WriteString("\n")
	}
	return strings.TrimRight(out.String(), "\n") + "\n"
}

func byDirCount(s *Snapshot, dir string) []*Doc {
	var out []*Doc
	for _, d := range s.Docs {
		if d.Dir == dir {
			out = append(out, d)
		}
	}
	return out
}

// ownerOf returns the project or character whose file sits beside directory dir.
func (s *Snapshot) ownerOf(dir string) *Doc {
	for _, d := range s.Docs {
		if (d.Kind == "projects" || d.Kind == "characters") && strings.TrimSuffix(d.File, ".md") == dir {
			return d
		}
	}
	return nil
}

func (s *Snapshot) renderRootIndex() string {
	var out strings.Builder
	out.WriteString("---\nokf_version: \"0.2\"\n---\n\n# Knowledge\n\n")
	for _, k := range topDirs {
		db := s.DBs[k]
		n := len(byDirCount(s, k))
		for _, d := range s.Docs {
			if d.Dir != k && strings.HasPrefix(d.Dir, k+"/") {
				n++
			}
		}
		desc := db.Description
		if desc == "" {
			desc = fmt.Sprintf("%d pages", n)
		}
		fmt.Fprintf(&out, "* [%s](%s/)%s\n", escapeLinkText(dirTitle(k, s.DBs)), k, descSuffix(desc))
	}
	out.WriteString("\n# History\n\n* [Update log](log.md) - pages created, edited or removed, by Notion edit date\n")
	return out.String()
}

func descSuffix(d string) string {
	d = strings.TrimSpace(strings.ReplaceAll(d, "\n", " "))
	if d == "" {
		return ""
	}
	return " - " + d
}

func escapeHeading(s string) string { return strings.TrimSpace(s) }

func (s *Snapshot) logEntries(state *State, now time.Time) []LogEntry {
	seen := map[string]bool{}
	var entries []LogEntry
	for _, e := range state.Log {
		k := e.Date + "|" + e.Action + "|" + e.ID
		if !seen[k] {
			seen[k] = true
			entries = append(entries, e)
		}
	}
	add := func(e LogEntry) {
		k := e.Date + "|" + e.Action + "|" + e.ID
		if !seen[k] {
			seen[k] = true
			entries = append(entries, e)
		}
	}
	current := map[string]bool{}
	for _, d := range s.Docs {
		current[d.ID] = true
		prev, known := state.Pages[d.ID]
		edited := d.Edited.In(time.Local).Format("2006-01-02")
		switch {
		case !known:
			action := "Update"
			if d.Created.In(time.Local).Format("2006-01-02") == edited {
				action = "Creation"
			}
			add(LogEntry{Date: edited, Action: action, ID: d.ID, Title: d.Title, File: d.File})
		case prev.Edited != d.Edited.UTC().Format(time.RFC3339Nano):
			add(LogEntry{Date: edited, Action: "Update", ID: d.ID, Title: d.Title, File: d.File})
		case prev.File != d.File:
			add(LogEntry{Date: now.In(time.Local).Format("2006-01-02"), Action: "Move", ID: d.ID, Title: d.Title, File: d.File})
		}
	}
	for id, p := range state.Pages {
		if !current[id] {
			add(LogEntry{Date: now.In(time.Local).Format("2006-01-02"), Action: "Removal", ID: id, Title: p.Title, File: p.File})
		}
	}
	sort.SliceStable(entries, func(i, j int) bool {
		if entries[i].Date != entries[j].Date {
			return entries[i].Date > entries[j].Date
		}
		return entries[i].Title < entries[j].Title
	})
	if len(entries) > 5000 {
		entries = entries[:5000]
	}
	return entries
}

func renderLog(entries []LogEntry) string {
	var out strings.Builder
	out.WriteString("# Update log\n\nNewest first. Dates are Notion edit dates (local time); the mirror only sees each page's latest edit.\n")
	last := ""
	for _, e := range entries {
		if e.Date != last {
			fmt.Fprintf(&out, "\n## %s\n\n", e.Date)
			last = e.Date
		}
		fmt.Fprintf(&out, "* **%s**: [%s](/%s)\n", e.Action, escapeLinkText(e.Title), e.File)
	}
	return out.String()
}

// Apply writes the bundle under root, removing stray .md files. dryRun only reports.
func (b *Bundle) Apply(root string, dryRun bool) (created, updated, removed int, err error) {
	existing := map[string]bool{}
	if _, statErr := os.Stat(root); statErr == nil {
		walkErr := filepath.WalkDir(root, func(p string, d fs.DirEntry, werr error) error {
			if werr != nil {
				return werr
			}
			if d.IsDir() || !strings.HasSuffix(p, ".md") {
				return nil
			}
			rel, _ := filepath.Rel(root, p)
			existing[filepath.ToSlash(rel)] = true
			return nil
		})
		if walkErr != nil {
			return 0, 0, 0, walkErr
		}
	}
	paths := make([]string, 0, len(b.Files))
	for p := range b.Files {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	for _, p := range paths {
		content := b.Files[p]
		full := filepath.Join(root, filepath.FromSlash(p))
		old, readErr := os.ReadFile(full)
		switch {
		case errors.Is(readErr, fs.ErrNotExist):
			created++
			if dryRun {
				log.Printf("would create %s", p)
				continue
			}
		case readErr != nil:
			return created, updated, removed, readErr
		case string(old) == content:
			continue
		default:
			updated++
			if dryRun {
				log.Printf("would update %s", p)
				continue
			}
		}
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			return created, updated, removed, err
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			return created, updated, removed, err
		}
	}
	for p := range existing {
		if _, ok := b.Files[p]; ok {
			continue
		}
		removed++
		if dryRun {
			log.Printf("would remove %s", p)
			continue
		}
		if err := os.Remove(filepath.Join(root, filepath.FromSlash(p))); err != nil {
			return created, updated, removed, err
		}
	}
	if !dryRun {
		pruneEmptyDirs(root)
	}
	return created, updated, removed, nil
}

func pruneEmptyDirs(root string) {
	var dirs []string
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err == nil && d.IsDir() && p != root {
			dirs = append(dirs, p)
		}
		return nil
	})
	sort.Sort(sort.Reverse(sort.StringSlice(dirs)))
	for _, d := range dirs {
		if entries, err := os.ReadDir(d); err == nil && len(entries) == 0 {
			_ = os.Remove(d)
		}
	}
}
