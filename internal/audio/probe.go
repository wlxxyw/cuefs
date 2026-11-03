package audio

import (
	"fmt"
)

// ProbeAuto resolves an audio file's stream facts, preferring pure Go
// (FLAC STREAMINFO, RIFF fmt) and falling back to ffprobe only when that
// binary is configured. This is what makes ffprobe an optional dependency
// (DESIGN §4.3.1, M4).
func ProbeAuto(path, ffprobeBin string) (*Format, error) {
	f, err := ProbeNative(path)
	if err == nil {
		return f, nil
	}
	if ffprobeBin == "" {
		return nil, fmt.Errorf("%s: %w (ffprobe_bin is not configured)", path, err)
	}
	return Probe(ffprobeBin, path)
}

// ProbeNative probes FLAC and WAV files without spawning anything.
func ProbeNative(path string) (*Format, error) {
	blocks, si, _, err := ReadMetaBlocks(path)
	if err == nil {
		hasVideo := false
		for _, b := range blocks {
			if b.Kind == BlockPicture {
				hasVideo = true
			}
		}
		return &Format{
			Codec:         "flac",
			SampleRate:    si.SampleRate,
			Channels:      si.Channels,
			BitsPerSample: si.BitsPerSample,
			HasVideo:      hasVideo,
		}, nil
	}
	w, err := OpenWav(path)
	if err != nil {
		return nil, fmt.Errorf("%s is neither FLAC nor WAV: %w", path, err)
	}
	return &Format{
		Codec:         "wav",
		SampleRate:    w.Info.SampleRate,
		Channels:      w.Info.Channels,
		BitsPerSample: w.Info.Bits,
	}, nil
}
