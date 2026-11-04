package audio

import (
	"bufio"
	"fmt"
	"os"
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
	flacStreams, _ := CountFlacStreams(path)
	if flacStreams > 1 {
		return nil, fmt.Errorf("%s: %d fLaC marker(s); this is a multi-stream container that pure-Go slicing cannot handle",
			path, flacStreams)
	}
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

// CountFlacStreams reports whether a "FLAC" file actually holds more than one
// fLaC stream. Container formats unintentionally chain streams in one file
// (measured: two encoded albums in one file with only one into STREAMINFO).
func CountFlacStreams(path string) (int, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	rd := bufio.NewReaderSize(f, 1<<20)
	count := 0
	prev := make([]byte, 0, 3)
	buf := make([]byte, 1<<20)
	for {
		n, rerr := rd.Read(buf)
		wind := append(prev, buf[:n]...)
		for i := 0; i+4 <= len(wind); i++ {
			if string(wind[i:i+4]) == "fLaC" {
				count++
			}
		}
		if n >= 3 {
			prev = append(prev[:0], buf[n-3:n]...)
		}
		if rerr != nil {
			break
		}
	}
	if count == 0 {
		return 0, fmt.Errorf("%s: no fLaC marker found", path)
	}
	return count, nil
}
