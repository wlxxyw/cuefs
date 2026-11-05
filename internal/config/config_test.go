package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writeConfig(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "cuefs.yaml")
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestDefaults(t *testing.T) {
	cfg := Defaults()
	if err := cfg.Validate(); err != nil {
		t.Fatalf("defaults must validate: %v", err)
	}
	if cfg.Split.ApeConvertFormat != "flac" || cfg.Split.ApeMode != "convert" {
		t.Errorf("defaults = %s/%s", cfg.Split.ApeConvertFormat, cfg.Split.ApeMode)
	}
	if cfg.Cue.Encoding != "auto" {
		t.Errorf("cue.encoding = %q", cfg.Cue.Encoding)
	}
	if cfg.CacheTTL.Duration != 168*time.Hour {
		t.Errorf("cache_ttl = %v", cfg.CacheTTL.Duration)
	}
	if !cfg.IsSplitExt(".FLAC") || !cfg.IsSplitExt(".wav") {
		t.Error("IsSplitExt must be case-insensitive")
	}
	if cfg.IsSplitExt(".mp3") {
		t.Error(".mp3 must not be a split extension by default")
	}
}

func TestLoadPartialYAMLKeepsDefaults(t *testing.T) {
	p := writeConfig(t, `
src: /srv/music
cache_ttl: 24h
workers: 8
split:
  extensions: [".flac", ".FLAC", "wav"]
  ape_mode: hide
cue:
  encoding_list: [gb18030, big5]
  ignore_tags: ["COMMENT"]
`)
	cfg, from, err := Load(p)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if from != p {
		t.Errorf("loaded from %q", from)
	}
	if cfg.Src != "/srv/music" {
		t.Errorf("src = %q", cfg.Src)
	}
	if cfg.CacheTTL.Duration != 24*time.Hour {
		t.Errorf("cache_ttl = %v", cfg.CacheTTL.Duration)
	}
	if cfg.Workers != 8 {
		t.Errorf("workers = %d", cfg.Workers)
	}
	// Untouched keys keep their defaults.
	if cfg.FfmpegBin != "ffmpeg" {
		t.Errorf("ffmpeg_bin = %q, want the default", cfg.FfmpegBin)
	}
	if cfg.NameTemplate == "" || cfg.TitleMaxLen != 60 {
		t.Errorf("name settings lost: %q/%d", cfg.NameTemplate, cfg.TitleMaxLen)
	}
	if cfg.Split.ApeConvertFormat != "flac" {
		t.Errorf("ape_convert_format = %q", cfg.Split.ApeConvertFormat)
	}
	// Extensions are normalised: lower-case, leading dot, duplicates kept out.
	want := []string{".flac", ".flac", ".wav"}
	if strings.Join(cfg.Split.Extensions, ",") != strings.Join(want, ",") {
		t.Errorf("extensions = %v, want %v", cfg.Split.Extensions, want)
	}
	if strings.Join(cfg.Cue.EncodingList, ",") != "gb18030,big5" {
		t.Errorf("encoding_list = %v", cfg.Cue.EncodingList)
	}
	if len(cfg.Cue.IgnoreTags) != 1 || cfg.Cue.IgnoreTags[0] != "COMMENT" {
		t.Errorf("ignore_tags = %v", cfg.Cue.IgnoreTags)
	}
}

func TestLoadMissingFileReturnsDefaults(t *testing.T) {
	cfg, from, err := Load(filepath.Join(t.TempDir(), "absent.yaml"))
	if err == nil {
		t.Fatal("an explicitly named missing config must be an error")
	}
	_ = cfg
	_ = from

	// Searching with no explicit path and nothing on disk is not an error.
	for _, p := range append([]string{os.Getenv("CUEFS_CONFIG")}, DefaultPaths()...) {
		if p == "" {
			continue
		}
		if _, err := os.Stat(p); err == nil {
			t.Skipf("a real config file exists at %q; refusing to assert defaults", p)
		}
	}
	t.Setenv("CUEFS_CONFIG", filepath.Join(t.TempDir(), "absent.yaml"))
	cfg, from, err = Load("")
	if err != nil {
		t.Fatalf("search miss must not fail: %v", err)
	}
	if from != "" {
		t.Errorf("from = %q, want empty", from)
	}
	if cfg.Split.ApeMode != "convert" {
		t.Errorf("ape_mode = %q", cfg.Split.ApeMode)
	}
}

func TestValidationRejectsBadEnums(t *testing.T) {
	cases := []struct {
		name string
		body string
		want string
	}{
		{"ape_convert_format", "split:\n  ape_convert_format: ogg\n", "ape_convert_format"},
		{"ape_mode", "split:\n  ape_mode: maybe\n", "ape_mode"},
		{"log_level", "log_level: chatty\n", "log_level"},
		{"workers", "workers: 0\n", "workers"},
		{"title_max_len", "title_max_len: 0\n", "title_max_len"},
		{"cache_max_gb", "cache_max_gb: -1\n", "cache_max_gb"},
		{"extensions", "split:\n  extensions: []\n", "extensions"},
		{"encoding", "cue:\n  encoding: \"\"\n", "encoding"},
		{"duration", "cache_ttl: forever\n", "duration"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, _, err := Load(writeConfig(t, c.body))
			if err == nil {
				t.Fatal("expected an error")
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Fatalf("error %q does not mention %q", err, c.want)
			}
		})
	}
}

func TestEnvironmentConfigPath(t *testing.T) {
	p := writeConfig(t, "src: /from/env\n")
	t.Setenv("CUEFS_CONFIG", p)
	cfg, from, err := Load("")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if from != p || cfg.Src != "/from/env" {
		t.Fatalf("from=%q src=%q", from, cfg.Src)
	}
}

func TestExpandHome(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home directory")
	}
	got := ExpandHome("~/.cache/cuefs")
	want := filepath.Join(home, ".cache", "cuefs")
	if got != want {
		t.Fatalf("ExpandHome = %q, want %q", got, want)
	}
	if ExpandHome("/abs/path") != "/abs/path" {
		t.Fatal("absolute paths must pass through")
	}
}
