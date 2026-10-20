# notion-okf-mirror

A read-only mirror of a Notion workspace as an [Open Knowledge Format 0.2](https://github.com/GoogleCloudPlatform/open-knowledge-format/blob/main/SPEC.md)
markdown bundle. It reads the knowledge databases (Projects, Notes, Sources, Briefs, Journal, Tasks,
Characters and the Characters sub-pages) through the Notion REST API with an integration token,
converts each page to plain markdown with OKF frontmatter, writes an `index.md` per directory and
a `log.md`, and validates the result before it replaces the previous bundle. It never writes to Notion.

It exists so that an LLM-maintained knowledge base kept in Notion is also available on disk, in a
format other tools can read, refreshed nightly.

## Install

With [mise](https://mise.jdx.dev) (installs the release binary for your platform):

```bash
mise use -g github:seanb4t/notion-okf-mirror@latest
```

Or with Go:

```bash
go install github.com/seanb4t/notion-okf-mirror@latest
```

Or download `notion-okf-mirror_<version>_<os>_<arch>.tar.gz` from the
[releases page](https://github.com/seanb4t/notion-okf-mirror/releases); `checksums.txt` is next to it.

## Configure

Settings come from a YAML file (loaded with [koanf](https://github.com/knadh/koanf)), then
`OKF_MIRROR_*` environment variables, then flags. Write the example and fill in your database ids:

```bash
notion-okf-mirror config init        # writes ~/.config/okf-mirror/config.yaml (or $XDG_CONFIG_HOME/okf-mirror/)
notion-okf-mirror config show        # prints the resolved settings and the databases it will read
```

```yaml
out: ~/okf-mirror                      # the bundle is written to <out>/okf
token_file: ~/.config/okf-mirror/env   # a NOTION_TOKEN=... line; env NOTION_TOKEN wins when set
interval: 24h                          # loop interval when not run with --once
validate: true                         # refuse to write a bundle that fails OKF 0.2 validation
databases:                             # by kind; omit kinds you do not have, do not add others
  projects:   {database: "<database id>", data_source: "<data source id>"}
  notes:      {database: "...", data_source: "..."}
  sources:    {database: "...", data_source: "..."}
  briefs:     {database: "...", data_source: "..."}
  journal:    {database: "...", data_source: "..."}
  tasks:      {database: "...", data_source: "..."}
  characters: {database: "...", data_source: "..."}
```

The kinds are fixed (`projects`, `notes`, `sources`, `briefs`, `journal`, `tasks`, `characters`)
because each one has its own index grouping and frontmatter rules; the ids are yours. The
`database` id is what `GET /databases/{id}` takes (title and description); the `data_source` id is
what `POST /data_sources/{id}/query` takes. Both show in Notion's share dialog for the database.

Environment: `OKF_MIRROR_CONFIG` (path of the file), `OKF_MIRROR_OUT`, `OKF_MIRROR_TOKEN_FILE`,
`OKF_MIRROR_INTERVAL`, `OKF_MIRROR_VALIDATE`, and `NOTION_TOKEN` for the token itself. A token file
is the safer home for the token:

```bash
mkdir -p ~/.config/okf-mirror && chmod 700 ~/.config/okf-mirror
printf 'NOTION_TOKEN=ntn_...\n' > ~/.config/okf-mirror/env && chmod 600 ~/.config/okf-mirror/env
```

## Run

```bash
notion-okf-mirror --once                        # one pass with the configured settings
notion-okf-mirror --once --dry-run              # report what would change, write nothing
notion-okf-mirror --once --out /tmp/okf         # flags override the config
notion-okf-mirror validate ~/okf-mirror/okf     # built-in OKF 0.2 check
notion-okf-mirror version
okflint validate --manifest ~/okf-mirror/okf-base.yaml   # external check (uv tool install okflint)
```

| Flag | Meaning |
|---|---|
| `--config FILE` | config file; default `$OKF_MIRROR_CONFIG`, else `~/.config/okf-mirror/config.yaml` |
| `--out DIR` | target directory; the bundle is `DIR/okf` (config `out`, default `~/okf-mirror`) |
| `--once` | one pass and exit; without it the program loops every `--interval` |
| `--interval 24h` | loop interval |
| `--dry-run` | fetch and render, report what would change, write nothing |
| `--full` | refetch every page body, ignoring the cache |
| `--token-file FILE` | a file with a `NOTION_TOKEN=...` line (config `token_file`); `NOTION_TOKEN` in the environment wins |
| `--no-validate` | skip the OKF validation of the result (config `validate: false`) |

A full pass is about 230 API calls (around 90 s at 3 requests/s); a cached pass is about 50 calls.

## Where it writes

| Path | What |
|---|---|
| `DIR/okf/` | the bundle; the mirror owns it entirely and deletes `.md` files it did not produce |
| `DIR/cache/` | raw Notion markdown per page, keyed by `last_edited_time`; unchanged pages are not refetched |
| `DIR/state.json` | page to file map, edit times, and the accumulated `log.md` entries |
| `DIR/okf-base.yaml` | manifest for `okflint` |

Make `DIR` a git repository if you want history beyond `log.md`.

## Bundle layout

```
okf/
  index.md              root; okf_version "0.2"; one entry per directory, descriptions from the databases
  log.md                newest-first, one ## YYYY-MM-DD per Notion edit date; Creation/Update/Move/Removal
  projects/<p>.md       type Project; projects/<p>/ holds that project's Notes
  projects/<p>/<n>.md   type = Notes.type (Reference, Design, Runbook, ...)
  notes/<n>.md          Notes with no project
  sources/<s>.md        type Source; body lists "Cited by"
  briefs/<b>.md         type Brief / Podcast / Digest
  journal/YYYY-MM-DD.md type Journal Entry
  tasks/<t>.md          type Task
  characters/<c>.md     type Character; characters/<c>/<section>.md are the sub-pages (type Character Note)
```

Every directory gets an `index.md` grouped the way the database's default view is grouped (Notes
by `type`, Projects by status, Journal by month newest first, Tasks in board order, Sources by
`kind`, Characters by campaign). The REST API has no views endpoint, so these groupings are coded,
not read live.

Filenames are slugs of the title; a clash gets a 6-character id suffix; a title that slugs to the
reserved `index` or `log` becomes `index-note` / `log-note`.

## Frontmatter

Flattened Notion properties are reassembled into OKF families; everything else is kept as
extension keys.

- `generated: { by, at }` only when `generated_by` is set, because OKF requires `by`. When only the
  date is known the legacy v0.1 `timestamp` carries it. Authors are never invented.
- `verified: { by, at }` only when both are set; a bare `verified_at` stays an extension.
- `sources:` from the Sources relation, fully paginated (query results truncate at 25). `id` is
  `source_id` or the title slug; `resource` is the URL, or a relative link to the row's own
  `sources/<s>.md` for free-text locators, so `resource` is never empty.
- `status` is the OKF vocabulary (draft / stable / deprecated); Project, Character and Task statuses
  are mapped and kept verbatim in `project_status` / `character_status` / `task_status`.
- Every timestamp is ISO 8601 with an offset; date-only properties become `T00:00:00Z`.
- `notion_id` on every page. System `created` / `updated` are never used as authoring dates; they
  only drive `log.md` and the cache.

## Body conversion

Notion-flavored Markdown becomes plain markdown: one block per line becomes blank-line-separated
blocks; tabs become 4-space nesting; `<callout>` becomes a `> [!info]`-style blockquote callout
(pipe tables Notion flattens inside callouts are restored); `<table>` becomes a pipe table;
`<mention-page>` becomes a relative link, or a notion.so link for pages outside the bundle;
`<page>` child tags become a link list; Notion's `**a ****`code`**` bold artifact is collapsed; stray
escapes are removed outside code. `(embed: file)` lines stay as text; attachments are not mirrored.

## Validation

Two gates:

- the built-in `notion-okf-mirror validate`: SPEC §11 conformance plus the OpenKnowledge okf plugin
  rules (timestamp shape, status vocabulary, index/log shape, reserved casing, no wiki links, no
  `.mdx`). The mirror runs it on every pass and refuses to write a non-conformant bundle.
- `okflint validate` (okf.md community linter) through the generated manifest.

## Running nightly on macOS

`launchd/house.fzymgc.okf-mirror.plist` is a launch agent template that runs `notion-okf-mirror
--once` at 03:30 through `mise exec`, so `mise up` upgrades the binary the job uses; paths and
databases come from the config file. The install steps are in the file's header comment.

## Development

```bash
mise install          # go, gofumpt, golangci-lint, goreleaser, hk at the pinned versions
hk install --mise     # git hooks from hk.pkl: gofumpt, golangci-lint, go vet on commit; go test on push
mise run check        # vet + lint + test, the same gate CI runs
mise run release:check   # validate .goreleaser.yaml and build a snapshot into dist/
```

`go test ./...` covers the converter (blocks, tables, callouts, mentions, escapes, fences),
relative links and the validator.

## Releasing

Tag a commit on `main` with `vX.Y.Z` and push the tag. The release workflow runs the checks and
`goreleaser release`, which publishes the archives and `checksums.txt` to a GitHub release.

## License

[MIT](LICENSE).
