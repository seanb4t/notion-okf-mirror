package main

import (
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLoadConfigLayers(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	err := os.WriteFile(path, []byte(`
out: ~/from-file
interval: 6h
validate: false
databases:
  notes: {database: db-notes, data_source: ds-notes}
  projects: {database: db-proj, data_source: ds-proj}
`), 0o600)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("OKF_MIRROR_INTERVAL", "2h")
	t.Setenv("OKF_MIRROR_TOKEN_FILE", "/env/token")
	t.Setenv("OKF_MIRROR_CONFIG", "/ignored/by/loader")

	fs := flag.NewFlagSet("t", flag.ContinueOnError)
	fs.String("out", "", "")
	fs.Duration("interval", 24*time.Hour, "")
	fs.String("token-file", "", "")
	fs.Bool("no-validate", false, "")
	if err := fs.Parse([]string{"--out", "/from/flag"}); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfig(path, true, fs, flagKeys)
	if err != nil {
		t.Fatal(err)
	}
	home, _ := os.UserHomeDir()
	if cfg.Out != "/from/flag" {
		t.Errorf("out: flag should win, got %q", cfg.Out)
	}
	if cfg.Interval != 2*time.Hour {
		t.Errorf("interval: env should beat file, got %s", cfg.Interval)
	}
	if cfg.TokenFile != "/env/token" {
		t.Errorf("token_file from env, got %q", cfg.TokenFile)
	}
	if cfg.Validate {
		t.Error("validate: file set false")
	}
	kinds, err := cfg.Kinds()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(kinds, ",") != "projects,notes" {
		t.Errorf("kinds in layout order, got %v", kinds)
	}
	// defaults alone, no file
	cfg, err = LoadConfig(filepath.Join(dir, "missing.yaml"), false, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Out != filepath.Join(home, "okf-mirror") || cfg.Interval != 2*time.Hour || !cfg.Validate {
		t.Errorf("defaults: %+v", cfg)
	}
	if _, err := LoadConfig(filepath.Join(dir, "missing.yaml"), true, nil, nil); err == nil {
		t.Error("an explicit missing --config must fail")
	}
}

func TestKindsErrors(t *testing.T) {
	cases := map[string]Config{
		"no databases configured": {},
		"unknown database kind":   {Databases: map[string]DatabaseRef{"recipes": {Database: "a", DataSource: "b"}}},
		"need both":               {Databases: map[string]DatabaseRef{"notes": {Database: "a"}}},
	}
	for want, cfg := range cases {
		_, err := cfg.Kinds()
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("want error containing %q, got %v", want, err)
		}
	}
}

func TestSetDatabases(t *testing.T) {
	cfg := &Config{Databases: map[string]DatabaseRef{"tasks": {Database: "db", DataSource: "ds"}}}
	if err := SetDatabases(cfg); err != nil {
		t.Fatal(err)
	}
	if len(topDirs) != 1 || topDirs[0] != "tasks" || databases["tasks"] != "db" || dataSources["tasks"] != "ds" {
		t.Errorf("topDirs=%v databases=%v dataSources=%v", topDirs, databases, dataSources)
	}
}

func TestWriteExampleConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "config.yaml")
	if err := WriteExampleConfig(path); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfig(path, true, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cfg.Kinds(); err == nil || !strings.Contains(err.Error(), "need both") {
		t.Errorf("example has empty ids, want 'need both' error, got %v", err)
	}
	if err := WriteExampleConfig(path); err == nil {
		t.Error("must refuse to overwrite")
	}
}
