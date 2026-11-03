// Package audio inspects an audio file's stream properties via ffprobe.
package audio

import (
	"encoding/json"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
)

// Format is the subset of stream facts the splitter needs.
type Format struct {
	Codec         string
	SampleRate    int
	Channels      int
	BitsPerSample int
	HasVideo      bool // usually an embedded cover art stream
}

const probeEntries = "stream=codec_name,codec_type,sample_rate,channels,bits_per_raw_sample,bits_per_sample"

// Probe runs ffprobe once and returns the first audio stream plus whether any
// video stream (embedded picture) exists.
func Probe(ffprobeBin, path string) (*Format, error) {
	if strings.TrimSpace(ffprobeBin) == "" {
		return nil, fmt.Errorf("ffprobe_bin is not configured")
	}
	cmd := exec.Command(ffprobeBin, "-v", "error", "-print_format", "json",
		"-show_entries", probeEntries, "--", path)
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("ffprobe %s: %w", path, err)
	}
	var doc struct {
		Streams []struct {
			CodecName     string `json:"codec_name"`
			CodecType     string `json:"codec_type"`
			SampleRate    string `json:"sample_rate"`
			Channels      int    `json:"channels"`
			BitsPerSample int    `json:"bits_per_sample"`
			BitsPerRaw    string `json:"bits_per_raw_sample"`
		} `json:"streams"`
	}
	if err := json.Unmarshal(out, &doc); err != nil {
		return nil, fmt.Errorf("ffprobe %s: %w", path, err)
	}
	var f Format
	for _, s := range doc.Streams {
		switch s.CodecType {
		case "video":
			f.HasVideo = true
		case "audio":
			if f.SampleRate != 0 {
				continue // only the first audio stream
			}
			f.Codec = s.CodecName
			f.Channels = s.Channels
			f.BitsPerSample = s.BitsPerSample
			if n, err := strconv.Atoi(s.SampleRate); err == nil {
				f.SampleRate = n
			}
			if n, err := strconv.Atoi(s.BitsPerRaw); err == nil && n > 0 {
				f.BitsPerSample = n
			}
		}
	}
	if f.SampleRate <= 0 {
		return nil, fmt.Errorf("ffprobe %s: no audio stream found", path)
	}
	return &f, nil
}
