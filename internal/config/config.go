// Package config implements cuefs YAML configuration (see docs/DESIGN.md §5).
package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Duration wraps time.Duration so YAML accepts Go duration strings ("168h").
type Duration struct{ time.Duration }

// Split holds the splitting engine configuration (DESIGN §4.3).
type Split struct {
	// Extensions lists the lowercase source extensions eligible for splitting.
	// A source file only becomes tracks when it has a sibling cue file of the
	// same basename (or an embedded FLAC cuesheet).
	Extensions []string `yaml:"extensions"`
	// ApeConvertFormat is the container produced when ape_mode == "convert":
	// "flac" (default) or "wav". APE is the only source that gets transcoded;
	// every other listed format splits into the source's own extension.
	ApeConvertFormat string `yaml:"ape_convert_format"`
	// ApeMode selects what to do with Monkey's Audio sources:
	// "convert" (default, transcode to FLAC in the cache), "hide" (drop from
	// the virtual view) or "passthrough" (expose the source as-is).
	ApeMode string `yaml:"ape_mode"`
	// CompressionLevel is the FLAC compression level handed to ffmpeg
	// (0-8); higher levels produce smaller files and cost more CPU.
	CompressionLevel int `yaml:"compression_level"`
}

// Cue holds cue-sheet parsing configuration (DESIGN §4.1, §4.2).
type Cue struct {
	// Encoding forces a specific encoding when not "auto". One of the names
	// known to internal/cue (e.g. "gb18030", "big5", "utf-8").
	Encoding string `yaml:"encoding"`
	// EncodingList is the ordered list of encodings tried during detection
	// (DESIGN D11). When empty, or when every candidate fails, the built-in
	// order is used instead.
	EncodingList []string `yaml:"encoding_list"`
	// CueOverrides maps a directory path to a per-directory encoding override,
	// for libraries where automatic detection is unreliable.
	CueOverrides map[string]CueOverride `yaml:"cue_overrides"`
	// IgnoreTags is a list of tag-name glob patterns that must not be copied
	// from the album file to the generated tracks.
	IgnoreTags []string `yaml:"ignore_tags"`
}

// CueOverride is a per-directory encoding override.
type CueOverride struct {
	Encoding string `yaml:"encoding"`
}

// Config is the root configuration object.
type Config struct {
	Src          string   `yaml:"src"`
	Mount        string   `yaml:"mount"`
	Cache        string   `yaml:"cache"`
	CacheMaxGB   int      `yaml:"cache_max_gb"`
	CacheTTL     Duration `yaml:"cache_ttl"`
	FfmpegBin    string   `yaml:"ffmpeg_bin"`
	FfprobeBin   string   `yaml:"ffprobe_bin"`
	Workers      int      `yaml:"workers"`
	NameTemplate string   `yaml:"name_template"`
	TitleMaxLen  int      `yaml:"title_max_len"`
	KeepAlbum    bool     `yaml:"keep_album"`
	Split        Split    `yaml:"split"`
	Cue          Cue      `yaml:"cue"`
	LogLevel     string   `yaml:"log_level"`
}

// Defaults returns the built-in configuration documented in DESIGN §5.
func Defaults() *Config {
	return &Config{
		Cache:        "~/.local/share/cuefs/cache",
		CacheMaxGB:   20,
		CacheTTL:     Duration{168 * time.Hour},
		FfmpegBin:    "ffmpeg",
		FfprobeBin:   "ffprobe",
		Workers:      4,
		NameTemplate: "{num:02d} - {title}{ext}",
		TitleMaxLen:  60,
		KeepAlbum:    false,
		Split: Split{
			Extensions:       []string{".flac", ".wav", ".ape"},
			ApeConvertFormat: "flac",
			ApeMode:          "convert",
			CompressionLevel: 5,
		},
		Cue: Cue{
			Encoding:     "auto",
			EncodingList: nil,
			IgnoreTags:   []string{"CUE_TRACK*", "COMMENT"},
		},
		LogLevel: "info",
	}
}

// DefaultPaths lists the config file locations searched when --config and
// $CUEFS_CONFIG are not given, in priority order.
func DefaultPaths() []string {
	home, err := os.UserHomeDir()
	if err != nil {
		home = ""
	}
	paths := []string{"cuefs.yaml"}
	if home != "" {
		paths = append(paths, filepath.Join(home, ".config", "cuefs", "cuefs.yaml"))
	}
	return paths
}

// Load reads the configuration. path == "" means "search DefaultPaths()".
// A missing file on the search path is not an error: defaults are returned.
func Load(path string) (*Config, string, error) {
	cfg := Defaults()

	var file string
	if path != "" {
		b, err := os.ReadFile(path)
		if err != nil {
			return nil, "", fmt.Errorf("read config %s: %w", path, err)
		}
		if err := applyYAML(cfg, b); err != nil {
			return nil, path, err
		}
		file = path
		return cfg, file, nil
	}

	// Search order: $CUEFS_CONFIG, then DefaultPaths().
	candidates := DefaultPaths()
	if env := os.Getenv("CUEFS_CONFIG"); env != "" {
		candidates = append([]string{env}, candidates...)
	}
	for _, p := range candidates {
		b, err := os.ReadFile(p)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, p, fmt.Errorf("read config %s: %w", p, err)
		}
		if err := applyYAML(cfg, b); err != nil {
			return nil, p, err
		}
		return cfg, p, nil
	}
	return cfg, "", nil
}

func applyYAML(cfg *Config, b []byte) error {
	// Decode into a mirror of cfg with zero values so that a partially
	// specified file does not reset defaults for absent keys.
	var raw struct {
		Src          *string   `yaml:"src"`
		Mount        *string   `yaml:"mount"`
		Cache        *string   `yaml:"cache"`
		CacheMaxGB   *int      `yaml:"cache_max_gb"`
		CacheTTL     *Duration `yaml:"cache_ttl"`
		FfmpegBin    *string   `yaml:"ffmpeg_bin"`
		FfprobeBin   *string   `yaml:"ffprobe_bin"`
		Workers      *int      `yaml:"workers"`
		NameTemplate *string   `yaml:"name_template"`
		TitleMaxLen  *int      `yaml:"title_max_len"`
		KeepAlbum    *bool     `yaml:"keep_album"`
		LogLevel     *string   `yaml:"log_level"`
		Split        *struct {
			Extensions       *[]string `yaml:"extensions"`
			ApeConvertFormat *string   `yaml:"ape_convert_format"`
			ApeMode          *string   `yaml:"ape_mode"`
			CompressionLevel *int      `yaml:"compression_level"`
		} `yaml:"split"`
		Cue *struct {
			Encoding     *string                `yaml:"encoding"`
			EncodingList *[]string              `yaml:"encoding_list"`
			CueOverrides map[string]CueOverride `yaml:"cue_overrides"`
			IgnoreTags   *[]string              `yaml:"ignore_tags"`
		} `yaml:"cue"`
	}
	if err := yaml.Unmarshal(b, &raw); err != nil {
		return fmt.Errorf("parse config: %w", err)
	}

	set := func(dst *string, v *string) {
		if v != nil {
			*dst = *v
		}
	}
	setInt := func(dst *int, v *int) {
		if v != nil {
			*dst = *v
		}
	}

	set(&cfg.Src, raw.Src)
	set(&cfg.Mount, raw.Mount)
	set(&cfg.Cache, raw.Cache)
	setInt(&cfg.CacheMaxGB, raw.CacheMaxGB)
	if raw.CacheTTL != nil {
		cfg.CacheTTL = *raw.CacheTTL
	}
	set(&cfg.FfmpegBin, raw.FfmpegBin)
	set(&cfg.FfprobeBin, raw.FfprobeBin)
	setInt(&cfg.Workers, raw.Workers)
	set(&cfg.NameTemplate, raw.NameTemplate)
	setInt(&cfg.TitleMaxLen, raw.TitleMaxLen)
	if raw.KeepAlbum != nil {
		cfg.KeepAlbum = *raw.KeepAlbum
	}
	set(&cfg.LogLevel, raw.LogLevel)

	if raw.Split != nil {
		if raw.Split.Extensions != nil {
			cfg.Split.Extensions = normalizeExts(*raw.Split.Extensions)
		}
		set(&cfg.Split.ApeConvertFormat, raw.Split.ApeConvertFormat)
		set(&cfg.Split.ApeMode, raw.Split.ApeMode)
		setInt(&cfg.Split.CompressionLevel, raw.Split.CompressionLevel)
	}
	if raw.Cue != nil {
		set(&cfg.Cue.Encoding, raw.Cue.Encoding)
		if raw.Cue.EncodingList != nil {
			cfg.Cue.EncodingList = normalizeEncodings(*raw.Cue.EncodingList)
		}
		if raw.Cue.CueOverrides != nil {
			cfg.Cue.CueOverrides = raw.Cue.CueOverrides
		}
		if raw.Cue.IgnoreTags != nil {
			cfg.Cue.IgnoreTags = *raw.Cue.IgnoreTags
		}
	}
	return cfg.Validate()
}

// UnmarshalYAML lets cache_ttl be written as a plain string, e.g. `cache_ttl: 168h`.
func (d *Duration) UnmarshalYAML(value *yaml.Node) error {
	var s string
	if err := value.Decode(&s); err != nil {
		return fmt.Errorf("duration must be a string such as 168h: %w", err)
	}
	parsed, err := time.ParseDuration(s)
	if err != nil {
		return fmt.Errorf("invalid duration %q: %w", s, err)
	}
	d.Duration = parsed
	return nil
}

// MarshalYAML renders the duration back as a Go duration string.
func (d Duration) MarshalYAML() (any, error) { return d.String(), nil }

func normalizeExts(exts []string) []string {
	out := make([]string, 0, len(exts))
	for _, e := range exts {
		e = strings.ToLower(strings.TrimSpace(e))
		if e == "" {
			continue
		}
		if !strings.HasPrefix(e, ".") {
			e = "." + e
		}
		out = append(out, e)
	}
	return out
}

func normalizeEncodings(names []string) []string {
	out := make([]string, 0, len(names))
	for _, n := range names {
		n = strings.ToLower(strings.TrimSpace(n))
		if n != "" {
			out = append(out, n)
		}
	}
	return out
}

// ExpandHome replaces a leading "~" with the user home directory.
func ExpandHome(p string) string {
	if !strings.HasPrefix(p, "~") {
		return p
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return p
	}
	return filepath.Join(home, strings.TrimPrefix(p, "~"))
}
