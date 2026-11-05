package audio

import (
	"fmt"
	"os"

	"github.com/mewkiz/flac"
)

// DecodeIndexRange decodes the samples of the given (already indexed) frames,
// returning one slice per frame, one per channel, in real channel order (the
// decoder undoes left/side, side/right and mid/side coupling itself).
//
// The decoder's cursor must line up with the supplier's index: when the first
// frame is not the stream's first, we use the seek table (or scan) to jump to
// its first sample before iterating, which is exactly what keeps this decode
// in lockstep with the index the splitter relies on.
func DecodeIndexRange(path string, frames []FlacFrame) ([][][]int32, error) {
	if len(frames) == 0 {
		return nil, fmt.Errorf("no frames to decode")
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	// NewSeek keeps the reader seekable, which Stream.Seek (our jump to the
	// first frame of the range) needs.
	stream, err := flac.NewSeek(f)
	if err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	defer stream.Close()

	if first := frames[0].Start; first > 0 {
		if _, serr := stream.Seek(uint64(first)); serr != nil {
			return nil, fmt.Errorf("seek %s to sample %d: %w", path, first, serr)
		}
	}

	out := make([][][]int32, 0, len(frames))
	for k := 0; k < len(frames); k++ {
		want := frames[k]
		f, ferr := stream.ParseNext()
		if ferr != nil {
			return nil, fmt.Errorf("%s: frame %d (%s): %w", path, k, want.Coding, ferr)
		}
		chans := make([][]int32, len(f.Subframes))
		for ch := range chans {
			chans[ch] = f.Subframes[ch].Samples
		}
		if n := len(chans[0]); n != want.Samples {
			return nil, fmt.Errorf("%s: frame %d decodes %d samples, index says %d",
				path, k, n, want.Samples)
		}
		// NOTE: Frame.SampleNumber() is not usable as a cross-check here: for
		// the final, shorter frame of an ffmpeg-written stream it reports
		// Num*BlockSize, which is wrong when the last frame is shorter than
		// the others (measured: 30849*3696 vs the real 126357504). The frame
		// index is the authority for positions.
		out = append(out, chans)
	}
	return out, nil
}

// bitBuffer packs bits MSB first, the way FLAC frames are laid out. The last
// byte is padded with zeros if the bits do not end on a byte boundary.
type bitBuffer struct {
	buf    []byte
	filled int // bits written into the pending byte
	cur    byte
}

func (b *bitBuffer) wts(v uint64, n int) {
	for i := n - 1; i >= 0; i-- {
		bit := byte(v>>uint(i)) & 1
		b.cur = b.cur<<1 | bit
		b.filled++
		if b.filled == 8 {
			b.buf = append(b.buf, b.cur)
			b.cur, b.filled = 0, 0
		}
	}
}

func (b *bitBuffer) padToByte() {
	if b.filled == 0 {
		return
	}
	for b.filled < 8 {
		b.cur = b.cur << 1
		b.filled++
	}
	b.buf = append(b.buf, b.cur)
	b.cur, b.filled = 0, 0
}

// utf8Number encodes a number the FLAC way (1..7 bytes).
func utf8Number(v uint64) []byte {
	switch {
	case v < 1<<7:
		return []byte{byte(v)}
	case v < 1<<11:
		return []byte{0b11000000 | byte(v>>6), 0b10000000 | byte(v&0x3F)}
	case v < 1<<16:
		return []byte{0b11100000 | byte(v>>12),
			0b10000000 | byte((v>>6)&0x3F), 0b10000000 | byte(v&0x3F)}
	case v < 1<<21:
		return []byte{0b11110000 | byte(v>>18),
			0b10000000 | byte((v>>12)&0x3F), 0b10000000 | byte((v>>6)&0x3F),
			0b10000000 | byte(v&0x3F)}
	case v < 1<<26:
		return []byte{0b11111000 | byte(v>>24),
			0b10000000 | byte((v>>18)&0x3F), 0b10000000 | byte((v>>12)&0x3F),
			0b10000000 | byte((v>>6)&0x3F), 0b10000000 | byte(v&0x3F)}
	case v < 1<<31:
		return []byte{0b11111100 | byte(v>>30),
			0b10000000 | byte((v>>24)&0x3F), 0b10000000 | byte((v>>18)&0x3F),
			0b10000000 | byte((v>>12)&0x3F), 0b10000000 | byte((v>>6)&0x3F),
			0b10000000 | byte(v&0x3F)}
	default:
		return []byte{0xFE,
			0b10000000 | byte((v>>30)&0x3F), 0b10000000 | byte((v>>24)&0x3F),
			0b10000000 | byte((v>>18)&0x3F), 0b10000000 | byte((v>>12)&0x3F),
			0b10000000 | byte((v>>6)&0x3F), 0b10000000 | byte(v&0x3F)}
	}
}

// encodeVerbatimFrame writes one FLAC frame whose subframes are all VERBATIM
// (uncompressed samples), i.e. the easiest fully valid frame there is.
//
// It is used for the two boundary frames of a split, where re-using the
// source bytes is impossible because their samples are a *subset* of the
// original frame's.
//
// samples are in real channel order (no coupling needed: the frame header
// declares independent channels). firstSampleNum becomes the frame's sample
// number, written with SAMPLE-number coding so the partial block size is
// legal.
//
// The frame header uses block size code 7 (16-bit field), sample rate code 0
// and sample size code 0 ("take from STREAMINFO"), which every decoder
// honours for a stream whose STREAMINFO they were introduced from (our
// output's STREAMINFO is authoritative for both).
func encodeVerbatimFrame(samples [][]int32, firstSampleNum uint64, bps int) ([]byte, error) {
	nch := len(samples)
	if nch < 1 || nch > 8 {
		return nil, fmt.Errorf("channel count %d out of 1..8", nch)
	}
	n := len(samples[0])
	for _, ch := range samples {
		if len(ch) != n {
			return nil, fmt.Errorf("channels disagree on sample count")
		}
	}
	if n < 1 || n > 65535 {
		return nil, fmt.Errorf("block size %d out of 1..65535", n)
	}
	width := 0
	switch bps {
	case 8, 16, 24, 32:
		width = bps
	default:
		return nil, fmt.Errorf("unsupported bit depth %d", bps)
	}

	// <3> sample size: write the EXPLICIT code whenever the spec has one.
	// Code 000 ("get from STREAMINFO") is legal for a stream but poisons a
	// standalone frame parse: mewkiz leaves frame.BitsPerSample at 0, reads
	// zero-bit VERBATIM samples and then trips over a CRC-16 that lands in
	// the silence (measured: "expected 0x0000"). ffmpeg writes the explicit
	// code too (byte3 = 0x18 for stereo/16-bit). 32-bit has no code in the
	// spec, so it stays 000 (mewkiz cannot decode 32-bit FLAC anyway).
	sizeCode := uint64(sizeCodeFor(bps))

	var hdr, sub bitBuffer
	// Frame header: sync, reserved 0, VARIABLE blocking, block size code 7
	// (16-bit block size - 1), sample rate code 0 (from STREAMINFO),
	// independent channels, explicit sample size, reserved,
	// UTF-8 SAMPLE number, 16-bit block size, CRC-8.
	hdr.wts(0b11111111111110, 14)
	hdr.wts(0, 1)
	hdr.wts(1, 1) // strategy bit = variable = sample-number coding
	hdr.wts(7, 4)
	hdr.wts(0, 4)
	hdr.wts(uint64(nch-1), 4)
	hdr.wts(sizeCode, 3)
	hdr.wts(0, 1) // reserved
	for _, b := range utf8Number(firstSampleNum) {
		hdr.wts(uint64(b), 8)
	}
	hdr.wts(uint64(n-1), 16)
	hdr.buf = append(hdr.buf, crc8(hdr.buf))

	for _, ch := range samples {
		// Subframe header (FLAC spec §8.2): <0> zero padding, <6> type,
		// <1> wasted-bits flag. All eight bits are mandatory; dropping the
		// flag shifts every reader one bit into the samples.
		sub.wts(0, 1) // padding
		sub.wts(1, 6) // VERBATIM
		sub.wts(0, 1) // no wasted bits
		for _, s := range ch {
			sub.wts(uint64(uint32(s)), width)
		}
	}
	sub.padToByte()

	out := append(hdr.buf, sub.buf...)
	value := crc16(out)
	out = append(out, byte(value>>8), byte(value))
	return out, nil
}
