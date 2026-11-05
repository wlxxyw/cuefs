package config

import (
	"fmt"
	"strings"
)

// Validate checks enum-valued fields so that mistakes surface at startup
// instead of during the first split.
func (c *Config) Validate() error {
	switch strings.ToLower(c.Split.ApeConvertFormat) {
	case "", "flac", "wav":
	default:
		return fmt.Errorf("split.ape_convert_format must be flac or wav, got %q", c.Split.ApeConvertFormat)
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
	// flac and wav (and anything else listed in split.extensions) split into
	// the source's own extension, and APE is transcoded - so a template that
	// hard-codes one extension ("...{title}.flac") would misname every track
	// whose source is a different format. {ext} is required instead.
	tpl := strings.ToLower(c.NameTemplate)
	if !strings.Contains(tpl, "{ext}") {
		switch {
		case strings.Contains(tpl, ".flac"):
			return fmt.Errorf("name_template hard-codes .flac; use {ext} so the track extension follows the source format")
		case strings.Contains(tpl, ".wav"):
			return fmt.Errorf("name_template hard-codes .wav; use {ext} so the track extension follows the source format")
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

// ApeExt returns the track extension APE sources are transcoded into,
// following split.ape_convert_format ("flac" is also the answer for "").
func (c *Config) ApeExt() string {
	if strings.EqualFold(c.Split.ApeConvertFormat, "wav") {
		return ".wav"
	}
	return ".flac"
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
