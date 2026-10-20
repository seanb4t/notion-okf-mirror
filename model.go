package main

import (
	"context"
	"fmt"
	"log"
	"path"
	"sort"
	"strings"
	"time"
)

// dataSources and databases map a configured kind to its Notion ids; topDirs is the configured kinds
// in layout order. All three are set from the config by SetDatabases before Fetch runs.
var (
	dataSources = map[string]string{}
	databases   = map[string]string{}
	topDirs     []string
)

// SetDatabases installs the configured databases. It returns the error from Config.Kinds.
func SetDatabases(cfg *Config) error {
	kinds, err := cfg.Kinds()
	if err != nil {
		return err
	}
	dataSources, databases = map[string]string{}, map[string]string{}
	for _, k := range kinds {
		dataSources[k] = cfg.Databases[k].DataSource
		databases[k] = cfg.Databases[k].Database
	}
	topDirs = kinds
	return nil
}

// Doc is one Notion page headed for one bundle file.
type Doc struct {
	ID      string // dashed page id
	Kind    string // projects | notes | sources | journal | tasks | briefs | characters | character-notes
	Title   string
	Type    string // OKF type
	Dir     string // bundle-relative directory ("" = root)
	File    string // bundle-relative path, e.g. projects/finwright.md
	Desc    string
	Group   string // index section
	SortKey string // descending sort inside a section
	Edited  time.Time
	Created time.Time
	HasBody bool // fetch /markdown

	page     *Page
	parent   *Doc                // character hub for a character note
	children []*Doc              // character notes
	relIDs   map[string][]string // relation property → page ids (fully paginated)
	body     string              // raw Notion markdown (sources: synthesized)
}

// Snapshot is everything one run read from Notion.
type Snapshot struct {
	Docs    []*Doc
	ByID    map[string]*Doc
	DBs     map[string]Database
	Fetched int
}

func parseTime(s string) time.Time {
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return time.Time{}
	}
	return t
}

// Fetch reads every page of the configured sources and their bodies (via the cache).
func Fetch(ctx context.Context, c *Client, cache *Cache, full bool) (*Snapshot, error) {
	snap := &Snapshot{ByID: map[string]*Doc{}, DBs: map[string]Database{}}
	for _, k := range topDirs {
		d, err := c.GetDatabase(ctx, databases[k])
		if err != nil {
			return nil, fmt.Errorf("database %s: %w", k, err)
		}
		snap.DBs[k] = d
	}
	add := func(d *Doc) {
		snap.Docs = append(snap.Docs, d)
		snap.ByID[d.ID] = d
	}
	for _, k := range topDirs {
		pages, err := c.QueryAll(ctx, dataSources[k])
		if err != nil {
			return nil, fmt.Errorf("query %s: %w", k, err)
		}
		log.Printf("%-10s %d pages", k, len(pages))
		for _, pg := range pages {
			d := &Doc{
				ID: pg.ID, Kind: k, Title: pg.Title(), Edited: parseTime(pg.LastEditedTime),
				Created: parseTime(pg.CreatedTime), page: pg, HasBody: k != "sources", relIDs: map[string][]string{},
			}
			if d.Title == "" {
				d.Title = "Untitled"
			}
			// relations: complete any truncated list
			for name, p := range pg.Properties {
				if p.Type != "relation" {
					continue
				}
				ids := make([]string, 0, len(p.Relation))
				for _, r := range p.Relation {
					ids = append(ids, r.ID)
				}
				if p.HasMore {
					all, err := c.RelationAll(ctx, pg.ID, p.ID)
					if err != nil {
						return nil, fmt.Errorf("relation %s.%s: %w", d.Title, name, err)
					}
					ids = all
				}
				d.relIDs[name] = ids
			}
			add(d)
			if k == "characters" {
				kids, err := c.ChildPages(ctx, pg.ID)
				if err != nil {
					return nil, fmt.Errorf("children of %s: %w", d.Title, err)
				}
				for _, kid := range kids {
					kp, err := c.GetPage(ctx, kid.ID)
					if err != nil {
						return nil, fmt.Errorf("child page %s: %w", kid.Title, err)
					}
					cd := &Doc{
						ID: kp.ID, Kind: "character-notes", Title: kid.Title, Edited: parseTime(kp.LastEditedTime),
						Created: parseTime(kp.CreatedTime), page: kp, parent: d, HasBody: true, relIDs: map[string][]string{},
					}
					d.children = append(d.children, cd)
					add(cd)
				}
			}
		}
	}
	// bodies
	for _, d := range snap.Docs {
		if !d.HasBody {
			continue
		}
		if !full {
			if body, ok := cache.Get(d.ID, d.Edited); ok {
				d.body = body
				continue
			}
		}
		body, err := c.Markdown(ctx, d.ID)
		if err != nil {
			return nil, fmt.Errorf("markdown %s: %w", d.Title, err)
		}
		d.body = body
		cache.Put(d.ID, d.Edited, body)
		snap.Fetched++
	}
	return snap, nil
}

// Plan assigns every doc a bundle path. Deterministic for a given snapshot.
func (s *Snapshot) Plan() {
	// projects first: their slugs name the note directories
	projSlug := map[string]string{}
	taken := map[string]map[string]bool{}
	claim := func(dir, stem, id string) string {
		if taken[dir] == nil {
			taken[dir] = map[string]bool{}
		}
		name := stem
		if name == "index" || name == "log" { // reserved filenames (OKF §3.1)
			name = stem + "-note"
		}
		if taken[dir][name] {
			name = stem + "-" + strings.ReplaceAll(id, "-", "")[:6]
		}
		taken[dir][name] = true
		return name
	}
	sorted := append([]*Doc(nil), s.Docs...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].ID < sorted[j].ID })
	for _, d := range sorted {
		if d.Kind == "projects" {
			d.Dir = "projects"
			stem := claim(d.Dir, slug(d.Title), d.ID)
			projSlug[d.ID] = stem
			d.File = d.Dir + "/" + stem + ".md"
		}
	}
	for _, d := range sorted {
		switch d.Kind {
		case "projects":
		case "notes":
			d.Dir = "notes"
			if ids := d.relIDs["project"]; len(ids) > 0 {
				if ps, ok := projSlug[ids[0]]; ok {
					d.Dir = "projects/" + ps
				}
			}
			d.File = d.Dir + "/" + claim(d.Dir, slug(d.Title), d.ID) + ".md"
		case "journal":
			d.Dir = "journal"
			stem := d.Title
			if ds := d.page.P("date").DateStart(); len(ds) >= 10 {
				stem = ds[:10]
			}
			d.File = d.Dir + "/" + claim(d.Dir, slug(stem), d.ID) + ".md"
		case "characters":
			d.Dir = "characters"
			stem := claim(d.Dir, slug(d.Title), d.ID)
			d.File = d.Dir + "/" + stem + ".md"
			for _, kid := range d.children {
				kid.Dir = d.Dir + "/" + stem
				t := kid.Title
				if i := strings.Index(t, " — "); i >= 0 {
					t = t[i+len(" — "):]
				}
				kid.File = kid.Dir + "/" + claim(kid.Dir, slug(t), kid.ID) + ".md"
			}
		case "character-notes":
		default:
			d.Dir = d.Kind
			d.File = d.Dir + "/" + claim(d.Dir, slug(d.Title), d.ID) + ".md"
		}
	}
}

// relLink returns a relative markdown path from one bundle file to another.
func relLink(from, to string) string {
	fromDir := path.Dir(from)
	if fromDir == "." {
		fromDir = ""
	}
	fp := strings.Split(fromDir, "/")
	if fromDir == "" {
		fp = nil
	}
	tp := strings.Split(to, "/")
	i := 0
	for i < len(fp) && i < len(tp)-1 && fp[i] == tp[i] {
		i++
	}
	parts := make([]string, 0, len(fp)-i+len(tp)-i)
	for j := i; j < len(fp); j++ {
		parts = append(parts, "..")
	}
	parts = append(parts, tp[i:]...)
	r := strings.Join(parts, "/")
	if !strings.HasPrefix(r, ".") {
		r = "./" + r
	}
	return r
}

func (s *Snapshot) linker(from *Doc) func(id string) (string, string, bool) {
	return func(id string) (string, string, bool) {
		t, ok := s.ByID[dashed(id)]
		if !ok {
			return "", "", false
		}
		return relLink(from.File, t.File), t.Title, true
	}
}

func (s *Snapshot) titles(ids []string) []string {
	var out []string
	for _, id := range ids {
		if d, ok := s.ByID[id]; ok {
			out = append(out, d.Title)
		}
	}
	return out
}

func (s *Snapshot) links(from *Doc, ids []string) []string {
	var out []string
	for _, id := range ids {
		if d, ok := s.ByID[id]; ok {
			out = append(out, "["+escapeLinkText(d.Title)+"]("+relLink(from.File, d.File)+")")
		}
	}
	return out
}

var lifecycle = map[string]string{
	"draft": "draft", "stable": "stable", "deprecated": "deprecated",
	// Projects / Characters statuses
	"active": "stable", "concept": "draft", "shelved": "deprecated", "archived": "deprecated", "retired": "deprecated",
	// Tasks statuses
	"Inbox": "stable", "Next": "stable", "Doing": "stable", "Waiting": "stable", "Done": "deprecated", "Dropped": "deprecated",
}

// Render produces the final file content for a doc.
func (s *Snapshot) Render(d *Doc) string {
	pg := d.page
	conv := &Converter{Link: s.linker(d)}
	var fm []KV
	var extra []string // body sections appended after the converted body

	// generated / timestamp: `generated` needs `by`; without it the legacy v0.1
	// `timestamp` carries the date (never invent an author).
	provenance := func(by, at string) {
		if at == "" {
			return
		}
		if by != "" {
			fm = append(fm, KV{"generated", []KV{{"by", by}, {"at", okfTime(at)}}})
		} else {
			fm = append(fm, KV{"timestamp", okfTime(at)})
		}
	}

	switch d.Kind {
	case "projects":
		fm = append(fm, KV{"type", "Project"}, KV{"title", d.Title}, KV{"description", pg.P("description").Text()},
			KV{"resource", pg.P("resource").Str()}, KV{"tags", pg.P("tags").Multi()})
		st := pg.P("status").Sel()
		fm = append(fm, KV{"status", lifecycle[st]}, KV{"project_status", st}, KV{"source_path", pg.P("source_path").Text()})
		d.Desc, d.Group, d.SortKey = pg.P("description").Text(), titleCase(st), d.Title
		if tasks := s.links(d, d.relIDs["tasks"]); len(tasks) > 0 {
			extra = append(extra, "# Tasks\n\n"+bullets(tasks))
		}

	case "notes":
		typ := pg.P("type").Sel()
		if typ == "" {
			typ = "Document"
		}
		fm = append(fm, KV{"type", typ}, KV{"title", d.Title}, KV{"description", pg.P("description").Text()},
			KV{"resource", pg.P("resource").Str()}, KV{"tags", pg.P("tags").Multi()}, KV{"status", pg.P("status").Sel()})
		provenance(pg.P("generated_by").Text(), pg.P("generated_at").DateStart())
		if vb, va := pg.P("verified_by").Text(), pg.P("verified_at").DateStart(); vb != "" && va != "" {
			fm = append(fm, KV{"verified", []KV{{"by", vb}, {"at", okfTime(va)}}})
		} else if va != "" {
			fm = append(fm, KV{"verified_at", okfTime(va)})
		}
		fm = append(fm, KV{"stale_after", okfTime(pg.P("stale_after").DateStart())})
		fm = append(fm, KV{"sources", s.sourceEntries(d, d.relIDs["sources"])})
		fm = append(fm, KV{"verified_against", pg.P("verified_against").Text()},
			KV{"project", first(s.titles(d.relIDs["project"]))}, KV{"source_path", pg.P("source_path").Text()})
		d.Desc, d.Group, d.SortKey = pg.P("description").Text(), typ, pg.P("generated_at").DateStart()

	case "sources":
		kind := pg.P("kind").Sel()
		fm = append(fm, KV{"type", "Source"}, KV{"title", d.Title}, KV{"resource", pg.P("resource").Str()},
			KV{"kind", kind}, KV{"author", pg.P("author").Text()}, KV{"last_modified", okfTime(pg.P("last_modified").DateStart())},
			KV{"source_id", pg.P("source_id").Text()})
		d.Desc = pg.P("resource").Str()
		if kind == "local-run" {
			d.Group = "Local runs"
		} else {
			d.Group = titleCase(kind)
		}
		d.SortKey = d.Title
		if n := pg.P("notes").Text(); n != "" {
			extra = append(extra, n)
		}
		if cited := s.links(d, d.relIDs["cited_by"]); len(cited) > 0 {
			extra = append(extra, "# Cited by\n\n"+bullets(cited))
		}

	case "journal":
		date := pg.P("date").DateStart()
		fm = append(fm, KV{"type", "Journal Entry"}, KV{"title", d.Title}, KV{"date", date}, KV{"tags", pg.P("tags").Multi()})
		provenance("", date)
		fm = append(fm, KV{"source_path", pg.P("source_path").Text()})
		if len(date) >= 7 {
			d.Group = date[:7]
		}
		d.SortKey = date
		if fu := s.links(d, d.relIDs["follow_ups"]); len(fu) > 0 {
			extra = append(extra, "# Follow-ups\n\n"+bullets(fu))
		}

	case "tasks":
		st := pg.P("status").Sel()
		fm = append(fm, KV{"type", "Task"}, KV{"title", d.Title}, KV{"status", lifecycle[st]}, KV{"task_status", st},
			KV{"due", pg.P("due").DateStart()}, KV{"project", first(s.titles(d.relIDs["project"]))},
			KV{"captured_on", first(s.titles(d.relIDs["captured_on"]))})
		d.Group, d.SortKey = st, pg.P("due").DateStart()+" "+d.Title

	case "briefs":
		typ := titleCase(pg.P("type").Sel())
		if typ == "" {
			typ = "Brief"
		}
		fm = append(fm, KV{"type", typ}, KV{"title", d.Title}, KV{"resource", pg.P("resource").Str()},
			KV{"tags", pg.P("tags").Multi()})
		provenance(pg.P("generated_by").Text(), pg.P("period").DateStart())
		fm = append(fm, KV{"period", pg.P("period").DateStart()}, KV{"source", pg.P("source").Sel()},
			KV{"cadence", pg.P("cadence").Sel()}, KV{"areas", pg.P("areas").Multi()}, KV{"show", pg.P("show").Text()},
			KV{"episode_guid", pg.P("episode_guid").Text()}, KV{"source_path", pg.P("source_path").Text()})
		d.Group, d.SortKey = typ, pg.P("period").DateStart()

	case "characters":
		st := pg.P("status").Sel()
		fm = append(fm, KV{"type", "Character"}, KV{"title", d.Title}, KV{"status", lifecycle[st]}, KV{"character_status", st},
			KV{"player", pg.P("player").Text()}, KV{"class", pg.P("class").Text()}, KV{"archetype", pg.P("archetype").Text()},
			KV{"ancestry", pg.P("ancestry").Text()}, KV{"heritage", pg.P("heritage").Text()}, KV{"background", pg.P("background").Text()},
			KV{"level", numberOrNil(pg.P("level"))}, KV{"system", pg.P("system").Sel()},
			KV{"campaign", first(s.titles(d.relIDs["campaign"]))},
			KV{"foundry_world", pg.P("foundry_world").Text()}, KV{"foundry_actor", pg.P("foundry_actor").Text()},
			KV{"foundry_familiar", pg.P("foundry_familiar").Text()}, KV{"synced", okfTime(pg.P("synced").DateStart())},
			KV{"pathbuilder_id", pg.P("pathbuilder_id").Text()}, KV{"pathbuilder_url", pg.P("pathbuilder_url").Str()},
			KV{"source_path", pg.P("source_path").Text()})
		d.Desc = strings.TrimSpace(pg.P("build").Text() + " · " + pg.P("origin").Text())
		d.Desc = strings.Trim(d.Desc, " ·")
		d.Group = first(s.titles(d.relIDs["campaign"]))
		if d.Group == "" {
			d.Group = "No campaign"
		}
		d.SortKey = d.Title

	case "character-notes":
		section := d.Title
		if i := strings.Index(section, " — "); i >= 0 {
			section = section[i+len(" — "):]
		}
		fm = append(fm, KV{"type", "Character Note"}, KV{"title", d.Title}, KV{"character", d.parent.Title}, KV{"section", section})
		d.Group, d.SortKey = d.parent.Title, section
	}
	fm = append(fm, KV{"notion_id", d.ID})

	body := conv.Convert(d.body)
	if len(extra) > 0 {
		body = strings.TrimRight(body, "\n") + "\n\n" + strings.Join(extra, "\n\n") + "\n"
		body = strings.TrimLeft(body, "\n")
	}
	if body != "" {
		body = "\n" + body
	}
	return renderFrontmatter(fm) + body
}

// sourceEntries builds OKF `sources` entries from the Sources relation. Rows without a
// URL point at their own Source concept in the bundle, so `resource` is always set.
func (s *Snapshot) sourceEntries(from *Doc, ids []string) [][]KV {
	var out [][]KV
	for _, id := range ids {
		src, ok := s.ByID[id]
		if !ok {
			continue
		}
		pg := src.page
		sid := pg.P("source_id").Text()
		if sid == "" {
			sid = slug(src.Title)
		}
		res := pg.P("resource").Str()
		e := []KV{{"id", sid}}
		if res != "" {
			e = append(e, KV{"resource", res})
			if src.Title != res {
				e = append(e, KV{"title", src.Title})
			}
		} else {
			e = append(e, KV{"resource", relLink(from.File, src.File)}, KV{"title", src.Title})
		}
		e = append(e, KV{"author", pg.P("author").Text()}, KV{"last_modified", okfTime(pg.P("last_modified").DateStart())})
		out = append(out, e)
	}
	return out
}

func numberOrNil(p *Prop) any {
	if p == nil || p.Number == nil {
		return nil
	}
	return *p.Number
}

func first(xs []string) string {
	if len(xs) == 0 {
		return ""
	}
	return xs[0]
}

func bullets(items []string) string {
	var b strings.Builder
	for _, it := range items {
		b.WriteString("* " + it + "\n")
	}
	return strings.TrimRight(b.String(), "\n")
}
