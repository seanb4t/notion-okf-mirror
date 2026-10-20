package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/knadh/koanf/parsers/yaml"
	"github.com/knadh/koanf/providers/confmap"
	"github.com/knadh/koanf/providers/env/v2"
	"github.com/knadh/koanf/providers/file"
	"github.com/knadh/koanf/v2"
)

// Config is the resolved configuration: defaults, then the YAML file, then OKF_MIRROR_* environment
// variables, then command-line flags that were set.
type Config struct {
	Out       string                 `koanf:"out"`
	TokenFile string                 `koanf:"token_file"`
	Interval  time.Duration          `koanf:"interval"`
	Validate  bool                   `koanf:"validate"`
	Databases map[string]DatabaseRef `koanf:"databases"`
}

// DatabaseRef names one Notion database (for its title and description) and its data source (for
// querying pages). Both ids are in the Notion URL / the integration's share dialog.
type DatabaseRef struct {
	Database   string `koanf:"database"`
	DataSource string `koanf:"data_source"`
}

// kindOrder is every kind the mirror knows how to lay out, in root-index order. A config may omit
// kinds it does not have; it may not add others, because each kind carries its own index grouping
// and frontmatter rules (bundle.go, frontmatter.go).
var kindOrder = []string{"projects", "notes", "sources", "briefs", "journal", "tasks", "characters"}

const envPrefix = "OKF_MIRROR_"

func defaultConfigPath() string {
	if v := os.Getenv(envPrefix + "CONFIG"); v != "" {
		return v
	}
	dir := os.Getenv("XDG_CONFIG_HOME")
	if dir == "" {
		home, _ := os.UserHomeDir()
		dir = filepath.Join(home, ".config")
	}
	return filepath.Join(dir, "okf-mirror", "config.yaml")
}

func defaults() map[string]any {
	home, _ := os.UserHomeDir()
	return map[string]any{
		"out":        filepath.Join(home, "okf-mirror"),
		"token_file": "",
		"interval":   "24h",
		"validate":   true,
	}
}

// LoadConfig layers defaults, the YAML file at path (missing is fine unless explicit), OKF_MIRROR_*
// variables (OKF_MIRROR_OUT, OKF_MIRROR_TOKEN_FILE, OKF_MIRROR_INTERVAL, OKF_MIRROR_VALIDATE), and
// the flags in fs that the user actually set (keyed by flagKeys).
func LoadConfig(path string, explicit bool, fs *flag.FlagSet, flagKeys map[string]string) (*Config, error) {
	k := koanf.New(".")
	if err := k.Load(confmap.Provider(defaults(), "."), nil); err != nil {
		return nil, err
	}
	if err := k.Load(file.Provider(path), yaml.Parser()); err != nil {
		if explicit || !errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("config %s: %w", path, err)
		}
	}
	if err := k.Load(env.Provider(".", env.Opt{
		Prefix: envPrefix,
		TransformFunc: func(key, v string) (string, any) {
			key = strings.ToLower(strings.TrimPrefix(key, envPrefix))
			if key == "config" || strings.Contains(key, ".") {
				return "", nil // OKF_MIRROR_CONFIG is the file path, not a setting
			}
			return key, v
		},
	}), nil); err != nil {
		return nil, err
	}
	if fs != nil {
		var ferr error
		fs.Visit(func(f *flag.Flag) {
			key, ok := flagKeys[f.Name]
			if !ok {
				return
			}
			var v any = f.Value.String()
			if g, ok := f.Value.(flag.Getter); ok {
				v = g.Get()
			}
			if f.Name == "no-validate" {
				v = !v.(bool)
			}
			if err := k.Set(key, v); err != nil {
				ferr = err
			}
		})
		if ferr != nil {
			return nil, ferr
		}
	}
	var cfg Config
	if err := k.Unmarshal("", &cfg); err != nil {
		return nil, fmt.Errorf("config: %w", err)
	}
	cfg.Out = expandHome(cfg.Out)
	cfg.TokenFile = expandHome(cfg.TokenFile)
	return &cfg, nil
}

// Kinds returns the configured kinds in layout order, or an error naming unknown kinds or missing ids.
func (c *Config) Kinds() ([]string, error) {
	if len(c.Databases) == 0 {
		return nil, fmt.Errorf("no databases configured; run `notion-okf-mirror config init` and fill in %s", defaultConfigPath())
	}
	known := map[string]bool{}
	for _, k := range kindOrder {
		known[k] = true
	}
	var unknown, incomplete []string
	for name, ref := range c.Databases {
		if !known[name] {
			unknown = append(unknown, name)
			continue
		}
		if ref.Database == "" || ref.DataSource == "" {
			incomplete = append(incomplete, name)
		}
	}
	sort.Strings(unknown)
	sort.Strings(incomplete)
	if len(unknown) > 0 {
		return nil, fmt.Errorf("unknown database kind(s) %s; known kinds are %s", strings.Join(unknown, ", "), strings.Join(kindOrder, ", "))
	}
	if len(incomplete) > 0 {
		return nil, fmt.Errorf("database(s) %s need both `database` and `data_source` ids", strings.Join(incomplete, ", "))
	}
	var kinds []string
	for _, k := range kindOrder {
		if _, ok := c.Databases[k]; ok {
			kinds = append(kinds, k)
		}
	}
	return kinds, nil
}

func expandHome(p string) string {
	if p == "~" || strings.HasPrefix(p, "~/") {
		home, err := os.UserHomeDir()
		if err == nil {
			return filepath.Join(home, p[1:])
		}
	}
	return p
}

// exampleConfig is what `config init` writes.
const exampleConfig = `# notion-okf-mirror configuration. Every key is optional except databases.
# Environment overrides: OKF_MIRROR_OUT, OKF_MIRROR_TOKEN_FILE, OKF_MIRROR_INTERVAL, OKF_MIRROR_VALIDATE;
# OKF_MIRROR_CONFIG or --config points at a different file. Flags override everything.

# Target directory; the bundle is written to <out>/okf.
out: ~/okf-mirror

# File holding a NOTION_TOKEN=... line. The NOTION_TOKEN environment variable wins when it is set.
token_file: ~/.config/okf-mirror/env

# Loop interval when not run with --once.
interval: 24h

# Validate the bundle against OKF 0.2 before writing it.
validate: true

# The Notion databases to mirror, by kind. Omit a kind you do not have. Each needs the database id
# (GET /databases/{id}: title and description) and its data source id (POST /data_sources/{id}/query).
databases:
  projects:
    database: ""
    data_source: ""
  notes:
    database: ""
    data_source: ""
  sources:
    database: ""
    data_source: ""
  briefs:
    database: ""
    data_source: ""
  journal:
    database: ""
    data_source: ""
  tasks:
    database: ""
    data_source: ""
  characters:
    database: ""
    data_source: ""
`

// WriteExampleConfig writes exampleConfig to path, refusing to overwrite an existing file.
func WriteExampleConfig(path string) error {
	if _, err := os.Stat(path); err == nil {
		return fmt.Errorf("%s already exists; not overwriting", path)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(exampleConfig), 0o600)
}
