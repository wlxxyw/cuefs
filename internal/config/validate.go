package config

import (
	"fmt"
	"strings"
)

// Validate checks enum-valued fields so that mistakes surface at startup
// instead of during the first split.
func (c *Config) Validate() error {
	switch strings.ToLower(c.Split.OutputFormat) {
	case "flac", "wav":
	default:
		return fmt.Errorf("split.output_format must be flac or wav, got %q", c.Split.OutputFormat)
	}
	if c.Split.CompressionLevel < 0 || c.Split.CompressionLevel > 8 {
		return fmt.Errorf("split.compression_level must be 0-8, got %d", c.Split.CompressionLevel)
	}
	switch strings.ToLower(c.Split.ApeMode) {
	case "convert", "hide", "passthrough":
	default:
		return fmt.Errorf("split.ape_mode must be convert, hide or passthrough, got %q", c.Split.ApeMode)
	}
	switch strings.ToLower(c.LogLevel) {
	case "debug", "info", "warn", "warning", "error":
	default:
		return fmt.Errorf("log_level must be one of debug/info/warn/error, got %q", c.LogLevel)
	}
	if c.CacheMaxGB < 0 {
		return fmt.Errorf("cache_max_gb must be >= 0, got %d", c.CacheMaxGB)
	}
	if c.Workers < 1 {
		return fmt.Errorf("workers must be >= 1, got %d", c.Workers)
	}
	if c.TitleMaxLen < 1 {
		return fmt.Errorf("title_max_len must be >= 1, got %d", c.TitleMaxLen)
	}
	// The template may hard-code an extension ("...{title}.flac") - that must
	// match the output format, or people end up with misnamed files.
	tpl := strings.ToLower(c.NameTemplate)
	ext := strings.ToLower(c.Split.OutputFormat)
	if !strings.Contains(tpl, "{ext}") {
		switch {
		case strings.Contains(tpl, ".flac") && ext != "flac":
			return fmt.Errorf("name_template hard-codes .flac but split.output_format is %q; use {ext} in the template", ext)
		case strings.Contains(tpl, ".wav") && ext != "wav":
			return fmt.Errorf("name_template hard-codes .wav but split.output_format is %q; use {ext} in the template", ext)
		}
	}
	if c.Split.Extensions == nil || len(c.Split.Extensions) == 0 {
		return fmt.Errorf("split.extensions must not be empty")
	}
	if c.Cue.Encoding == "" {
		return fmt.Errorf("cue.encoding must not be empty (use \"auto\")")
	}
	return nil
}

// IsSplitExt reports whether ext (lower-case, leading dot) is eligible for
// splitting per the configuration.
func (c *Config) IsSplitExt(ext string) bool {
	ext = strings.ToLower(ext)
	for _, e := range c.Split.Extensions {
		if e == ext {
			return true
		}
	}
	return false
}
