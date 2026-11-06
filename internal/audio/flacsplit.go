package audio

import (
	"crypto/md5"
	"encoding/binary"
	"fmt"
	"io"
	"os"
)

// SplitSpec is everything the pure-Go FLAC splitter needs.
type SplitSpec struct {
	Src        string
	Start, End int64 // sample range [start,end)
	SampleRate int
	Channels   int
	Bits       int
	Tags       []VorbisEntry

	// Indexer resolves the source's frame index; nil means "index it
	// here" (one full CRC-16 walk per call). Runs that cut many tracks
	// from one source share a single walk through an IndexCache: without
	// one, a 300 MB album is walked once per track - the same bytes out of
	// the page cache, N times.
	Indexer IndexResolver

	// FakeStreamInfoMD5 writes zeros into STREAMINFO.md5 (FLAC spec:
	// "unknown, do not check") and skips the whole-window decode that the
	// real MD5 requires - the split then only decodes the (at most two)
	// boundary frames for their VERBATIM re-encode. Keep it consistent
	// across split and mount: same track, same bytes.
	FakeStreamInfoMD5 bool
}

// strategy is the header-number coding a frame of the new stream uses. The
// source's absolute frame numbers are meaningless after the splice, so every
// frame is rewritten when the source stream is FRAME-numbered (fixed
// blocking): the header's strategy bit becomes sample-number coding with the
// output-relative position, and both CRC-8 and CRC-16 get recomputed. The
// audio body stays a byte copy.
type strategy int

const (
	stratNone strategy = iota // header already carries sample positions
	stratPos                  // rewrite: sample-number coding
)

// boundaryWork carries everything needed to re-encode one boundary frame as
// VERBATIM subframes.
type boundaryWork struct {
	samples [][]int32
	at      int // index into emitted
}

// WriteFLACTrack cuts [start,end) out of a FLAC source and writes a complete
// FLAC file (STREAMINFO + VORBIS_COMMENT + frames), entirely in Go: frames
// fully inside the range are copied byte for byte (header numbers rewritten
// for their new positions), and the (at most two) boundary frames are
// decoded and re-encoded with VERBATIM subframes.
//
// STREAMINFO carries the *real* MD5 of the produced audio (computed while
// decoding the range), so downstream tools can still verify it.
func WriteFLACTrack(spec SplitSpec, out string) error {
	if spec.SampleRate <= 0 || spec.Channels <= 0 {
		return fmt.Errorf("need sample rate/channels, got %+v", spec)
	}
	switch spec.Bits {
	case 8, 16, 24, 32:
	default:
		return fmt.Errorf("unsupported FLAC bit depth %d", spec.Bits)
	}
	if spec.Channels > 8 {
		return fmt.Errorf("%d channels exceed the FLAC maximum of 8", spec.Channels)
	}

	// The split path REQUIRES a complete frame chain: IndexFlacFrames
	// resumes over false-sync dead ends (benign for the flacinfo指引
	// index), but a partial chain is only a broken chain for a splitter.
	// Bail with the underlying error rather than splicing a broken stream.
	// The split requires a chain that COVERS the window, not a chain that
	// covers the whole file: IndexFlacFrames bails at the first dead end
	// and keeps whatever it indexed before the break. Windows inside that
	// covered prefix are exact; beyond it there is nothing to splice.
	idx, err := IndexFlacFrames(spec.Src)
	if spec.Indexer != nil {
		idx, err = spec.Indexer.Index(spec.Src)
	}
	if err != nil {
		if len(idx.Frames) == 0 {
			return err
		}
		end := spec.End
		if end < 0 {
			end = idx.TotalSamples
		}
		last := idx.Frames[len(idx.Frames)-1]
		if end > last.Start+int64(last.Samples) {
			return err
		}
	}
	if spec.End < 0 {
		// The last track runs to the end of the stream.
		spec.End = idx.TotalSamples
	}
	i0, i1, ok := CoveringFrames(idx, spec.Start, spec.End)
	if !ok {
		return fmt.Errorf("no frames cover [%d,%d)", spec.Start, spec.End)
	}
	_, si, _, err := ReadMetaBlocks(spec.Src)
	if err != nil {
		return err
	}
	if si.BitsPerSample == 0 {
		si.BitsPerSample = spec.Bits
	}

	// The real STREAMINFO md5 is computed while decoding every covering
	// frame of the window - that decode IS the split's CPU bill. With
	// FakeStreamInfoMD5 the header carries zeros ("unknown" per the FLAC
	// spec) and only the (at most two) boundary frames get decoded, for
	// their VERBATIM re-encode.
	var samples [][][]int32
	var sum [16]byte
	if !spec.FakeStreamInfoMD5 {
		samples, err = DecodeIndexRange(spec.Src, idx.Frames[i0:i1+1])
		if err != nil {
			return err
		}
		// each covering frame contributes only its in-track span, which is
		// exactly what the boundary VERBATIM frames hold after re-encoding.
		raw, cerr := ChecksumRange(samples, spec, idx.Frames[i0:i1+1])
		if cerr != nil {
			return cerr
		}
		copy(sum[:], raw)
	}

	// Lay out the new stream: byte copies for fully contained frames,
	// verbatim re-encodes for the (at most two) boundary frames.
	strat := stratNone
	if i1 >= i0 && idx.Frames[i0].Coding == "fixed" {
		// The source stream is frame-numbered: positions must be rewritten.
		strat = stratPos
	}
	// boundarySamples resolves one boundary frame's decoded PCM: from the
	// pre-decoded window in real-md5 mode, or one targeted decode in
	// fake-md5 mode (frames are independently decodable, so a byte-anchored
	// parse is exact - and it dodges mewkiz's seek-limitation).
	boundarySamples := func(i int) ([][]int32, error) {
		if samples != nil {
			return samples[i-i0], nil
		}
		return DecodeFrame(spec.Src, idx.Frames[i])
	}
	var emitted []FlacFrame
	var boundary []boundaryWork
	totalSamples := int64(0)
	for i := i0; i <= i1; i++ {
		f := idx.Frames[i]
		st, en := f.Start, f.Start+int64(f.Samples)
		inStart, inEnd := st, en
		if inStart < spec.Start {
			inStart = spec.Start
		}
		if inEnd > spec.End {
			inEnd = spec.End
		}
		n := int(inEnd - inStart)
		sampleNum := totalSamples
		totalSamples += int64(n)
		if inStart == st && inEnd == en {
			emitted = append(emitted, FlacFrame{
				Offset:        f.Offset,
				Length:        f.Length,
				Start:         st,
				Samples:       f.Samples,
				BlocksizeCode: f.BlocksizeCode,
				Coding:        f.Coding,
				Strategy:      strat,
				SampleNum:     sampleNum,
			})
			continue
		}
		dec, derr := boundarySamples(i)
		if derr != nil {
			return derr
		}
		chanSamples := make([][]int32, len(dec))
		offset := int(inStart - st)
		for ch := range chanSamples {
			chanSamples[ch] = dec[ch][offset : offset+n]
		}
		emitted = append(emitted, FlacFrame{
			Samples:   n,
			Strategy:  strat,
			SampleNum: sampleNum,
		})
		boundary = append(boundary, boundaryWork{
			samples: chanSamples,
			at:      len(emitted) - 1,
		})
	}
	if totalSamples != spec.End-spec.Start {
		return fmt.Errorf("assembled %d samples, want %d", totalSamples, spec.End-spec.Start)
	}

	// Materialise the frame bytes.
	src, err := os.Open(spec.Src)
	if err != nil {
		return err
	}
	defer src.Close()
	for wi := range boundary {
		w := &boundary[wi]
		f := &emitted[w.at]
		bps := si.BitsPerSample
		if bps <= 0 {
			bps = spec.Bits
		}
		enc, err := encodeVerbatimFrame(w.samples, uint64(f.SampleNum), bps)
		if err != nil {
			return fmt.Errorf("verbatim frame at sample %d: %w", f.SampleNum, err)
		}
		f.Bytes = enc
		f.Length = len(enc)
		f.Start = int64(f.SampleNum)
		f.Offset = 0
	}
	for i := range emitted {
		f := &emitted[i]
		if f.Bytes != nil {
			continue
		}
		// Copied frames: read the source frame and rewrite its header for
		// the new position. The audio body stays byte-identical.
		srcFrame := make([]byte, f.Length)
		if _, err := readFullAt(src, srcFrame, f.Offset); err != nil {
			return fmt.Errorf("read frame at %d: %w", f.Offset, err)
		}
		body, err := rewriteHeader(srcFrame, f.Strategy, uint64(f.SampleNum), si.BitsPerSample)
		if err != nil {
			return err
		}
		f.Bytes = body
		f.Length = len(body)
		f.Offset = 0
	}

	// STREAMINFO of the produced track.
	st := StreamInfo{
		MinBlocksize:  1 << 16,
		MaxBlocksize:  1,
		SampleRate:    si.SampleRate,
		Channels:      si.Channels,
		BitsPerSample: si.BitsPerSample,
		TotalSamples:  totalSamples,
	}
	for _, f := range emitted {
		if f.Samples < st.MinBlocksize {
			st.MinBlocksize = f.Samples
		}
		if f.Samples > st.MaxBlocksize {
			st.MaxBlocksize = f.Samples
		}
	}
	if st.MinBlocksize < 1 {
		st.MinBlocksize = 1
	}
	if st.MaxBlocksize > 65535 {
		st.MaxBlocksize = 65535
	}
	if st.MinBlocksize > st.MaxBlocksize {
		st.MinBlocksize = st.MaxBlocksize
	}
	st.MD5 = sum

	blocks := []MetaBlock{
		{Kind: BlockStreamInfo, Data: encodeStreamInfo(st)},
		{Kind: BlockVorbisComment, Data: EncodeVorbisComment(DefaultVendor, spec.Tags)},
		{Kind: BlockPadding, Data: make([]byte, MinPadding), Last: true},
	}

	tmp := out + ".part"
	fh, err := os.Create(tmp)
	if err != nil {
		return err
	}
	if _, err := fh.Write([]byte("fLaC")); err != nil {
		return failWrite(fh, tmp, "write magic: %v", err)
	}
	for _, b := range blocks {
		n := len(b.Data)
		hdr := []byte{
			byte(b.Kind & 0x7F),
			byte(n >> 16 & 0x7F),
			byte(n >> 8),
			byte(n),
		}
		if b.Last {
			hdr[0] |= 0x80
		}
		if _, err := fh.Write(hdr); err != nil {
			return failWrite(fh, tmp, "write block header: %v", err)
		}
		if _, err := fh.Write(b.Data); err != nil {
			return failWrite(fh, tmp, "write block data: %v", err)
		}
	}
	for _, fr := range emitted {
		if _, err := fh.Write(fr.Bytes); err != nil {
			return failWrite(fh, tmp, "write frame: %v", err)
		}
	}
	if err := fh.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, out)
}

func failWrite(fh *os.File, tmp, format string, a ...any) error {
	fh.Close()
	os.Remove(tmp)
	return fmt.Errorf(format, a...)
}

// rewriteHeader switches a source frame's header to sample-number coding
// with the given position (FLAC spec §9, "blocking strategy"). The body
// stays byte-identical; CRC-8 and CRC-16 are recomputed because both cover
// the header.
//
// Layout of a valid header after 0xFF 0b11xssbss:
//
//	00-03  sync, reserved, strategy, blocksize code, sample rate code
//	04..   UTF-8 coded number, then optional block size / sample rate bytes,
//	       then the CRC-8
//	body   subframes + CRC-16
func rewriteHeader(srcFrame []byte, strat strategy, sampleNum uint64, bps int) ([]byte, error) {
	if strat == stratNone {
		return srcFrame, nil
	}
	if len(srcFrame) < 6 {
		return nil, fmt.Errorf("frame too short to rewrite (%d bytes)", len(srcFrame))
	}
	nl, err := utf8CodedLength(srcFrame[4])
	if err != nil {
		return nil, fmt.Errorf("frame header: %w", err)
	}
	pos := 4 + nl // position of the byte AFTER the old coded number

	out := make([]byte, 0, len(srcFrame)+8)
	out = append(out, srcFrame[0], srcFrame[1]|0x01) // strategy = sample
	// Normalise the sample-size nibble to the explicit code (see
	// encodeVerbatimFrame): sources written with 000 "from STREAMINFO" would
	// otherwise break standalone frame parsers on the copied frames too.
	// nibble = <3-bit size code><1 reserved>, i.e. code<<1.
	out = append(out, srcFrame[2], (srcFrame[3]&0xF0)|(sizeCodeFor(bps)<<1))
	out = writeUTF8Number(out, sampleNum)

	// The header continues with the optional block size / sample rate bytes
	// and ends with its CRC-8. The extra bytes follow the number field but
	// precede the CRC-8, so they sit right before it at
	//
	//	body start = pos + extras (the CRC-8 is the last header byte)
	// and the extras are identical for the old and the new header, which is
	// why the frame's LENGTH can be precomputed from the old header's CRC
	// position.
	//
	// The cleanest derivation uses the source header's own CRC-8 position:
	// after appending the new number, append the source's bytes from pos up
	// to and including the old CRC-8, then overwrite the CRC-8.

	// The subframes+payload sit after the old CRC-8.
	const crc16Len = 2 // frame CRC-16 trailer
	// Estimate the number of extra header bytes (block size, rate): the old
	// header CRC-8 is at (pos + extras); the extras also equal the number of
	// bytes the source header stretches beyond pos before its CRC-8. With
	// fixed 4096-style blocks (bsCode 5) there are no extras, but general
	// cases (bsCode 6/7, rate 12/13/14) shift them.
	end0 := utf8CodedEnd(srcFrame, pos) // = header CRC-8 index in srcFrame
	out = append(out, srcFrame[pos:end0]...)
	out = append(out, crc8(out))

	// Append the body and recompute the frame CRC-16.
	out = append(out, srcFrame[end0+1:len(srcFrame)-2]...)
	crc := crc16(out)
	out = append(out, byte(crc>>8), byte(crc))
	return out, nil
}

// sizeCodeFor maps a bit depth onto the frame header's explicit sample-size
// code (0 when the spec has none, e.g. 32-bit).
func sizeCodeFor(bps int) byte {
	switch bps {
	case 8:
		return 1
	case 12:
		return 2
	case 16:
		return 4
	case 20:
		return 5
	case 24:
		return 6
	default:
		return 0
	}
}

// utf8CodedEnd returns the index of the byte AFTER a source frame's header
// (i.e. the position of the header's own CRC-8), reading only as much as
// needed for the optional block-size and sample-rate bytes.
func utf8CodedEnd(f []byte, numEnd int) int {
	bsCode := int(f[2] >> 4)
	srCode := int(f[2] & 0x0F)
	pos := numEnd
	if bsCode == 0 || (bsCode >= 8 && bsCode <= 15) {
		switch bsCode {
		case 0, 8, 9, 10, 11, 12, 13, 14, 15:
			// "from STREAMINFO" / grab from the tail of the header
		}
	} else if bsCode == 6 {
		pos++
	} else if bsCode == 7 {
		pos += 2
	}
	switch srCode {
	case 12:
		pos++
	case 13, 14:
		pos += 2
	}
	return pos
}

// codecLen returns how many bytes the coded-number field occupies after
// writeUTF8Number produced, so that callers can recompute the header CRC-8.
func codecLen(v uint64) int {
	switch {
	case v < 1<<7:
		return 1
	case v < 1<<11:
		return 2
	case v < 1<<16:
		return 3
	case v < 1<<21:
		return 4
	case v < 1<<26:
		return 5
	case v < 1<<31:
		return 6
	default:
		return 7
	}
}

// writeUTF8Number appends a number in FLAC's UTF-8 style coding.
func writeUTF8Number(b []byte, v uint64) []byte {
	switch codecLen(v) {
	case 1:
		return append(b, byte(v))
	case 2:
		return append(b, 0b11000000|byte(v>>6), 0b10000000|byte(v&0x3F))
	case 3:
		return append(b, 0b11100000|byte(v>>12),
			0b10000000|byte((v>>6)&0x3F), 0b10000000|byte(v&0x3F))
	case 4:
		return append(b, 0b11110000|byte(v>>18),
			0b10000000|byte((v>>12)&0x3F), 0b10000000|byte((v>>6)&0x3F),
			0b10000000|byte(v&0x3F))
	case 5:
		return append(b, 0b11111000|byte(v>>24),
			0b10000000|byte((v>>18)&0x3F), 0b10000000|byte((v>>12)&0x3F),
			0b10000000|byte((v>>6)&0x3F), 0b10000000|byte(v&0x3F))
	case 6:
		return append(b, 0b11111100|byte(v>>30),
			0b10000000|byte((v>>24)&0x3F), 0b10000000|byte((v>>18)&0x3F),
			0b10000000|byte((v>>12)&0x3F), 0b10000000|byte((v>>6)&0x3F),
			0b10000000|byte(v&0x3F))
	default:
		return append(b, 0xFE,
			0b10000000|byte((v>>30)&0x3F), 0b10000000|byte((v>>24)&0x3F),
			0b10000000|byte((v>>18)&0x3F), 0b10000000|byte((v>>12)&0x3F),
			0b10000000|byte((v>>6)&0x3F), 0b10000000|byte(v&0x3F))
	}
}

// readFullAt reads exactly len(buf) bytes at off.
func readFullAt(f *os.File, buf []byte, off int64) (int, error) {
	n := 0
	for n < len(buf) {
		k, err := f.ReadAt(buf[n:], off+int64(n))
		n += k
		if err != nil {
			if err == io.EOF && n == len(buf) {
				return n, nil
			}
			return n, err
		}
	}
	return n, nil
}

func readFull(f *os.File, buf []byte) (int, error) {
	n := 0
	for n < len(buf) {
		k, err := f.Read(buf[n:])
		n += k
		if err != nil {
			return n, err
		}
	}
	return n, nil
}

// CoveringFrames returns the index bounds of the frames that overlap
// [from,to). A negative to means "to the end of the stream" and is resolved
// from the index's total sample count.
func CoveringFrames(idx *FlacIndex, from, to int64) (int, int, bool) {
	if to < 0 {
		to = idx.TotalSamples
	}
	i0, i1 := -1, -1
	for i := range idx.Frames {
		f := idx.Frames[i]
		if f.Start+int64(f.Samples) <= from {
			continue
		}
		if f.Start >= to {
			break
		}
		if i0 < 0 {
			i0 = i
		}
		i1 = i
	}
	if i0 < 0 || i1 < 0 {
		return 0, 0, false
	}
	return i0, i1, true
}

// ChecksumRange computes the FLAC STREAMINFO MD5 over the produced samples
// (interleaved, little-endian, bit depth / 8 bytes per sample).
func ChecksumRange(audio [][][]int32, spec SplitSpec, frames []FlacFrame) ([]byte, error) {
	width := spec.Bits / 8
	sum := md5.New()
	var buf [4]byte
	for fi, frame := range audio {
		// Only the samples that end up in the new stream contribute.
		st := frames[fi].Start
		en := st + int64(frames[fi].Samples)
		if st < spec.Start {
			st = spec.Start
		}
		if en > spec.End {
			en = spec.End
		}
		begin := int(st - frames[fi].Start)
		end := int(en - frames[fi].Start)
		n := len(frame[0])
		if end > n {
			end = n
		}
		if begin >= end {
			continue
		}
		for i := begin; i < end; i++ {
			for _, ch := range frame {
				v := ch[i]
				// Little-endian, two's complement, bit depth / 8 bytes.
				for k := 0; k < width; k++ {
					buf[k] = byte(v >> uint(8*k))
				}
				sum.Write(buf[:width])
			}
		}
	}
	return sum.Sum(nil), nil
}

// encodeStreamInfo renders a 34 byte STREAMINFO payload.
func encodeStreamInfo(si StreamInfo) []byte {
	p := make([]byte, 34)
	binary.BigEndian.PutUint16(p[0:2], uint16(si.MinBlocksize))
	binary.BigEndian.PutUint16(p[2:4], uint16(si.MaxBlocksize))
	v := uint64(si.SampleRate)<<44 | uint64(si.Channels-1)<<41 |
		uint64(si.BitsPerSample-1)<<36 | (uint64(si.TotalSamples) & ((1 << 36) - 1))
	var tail [8]byte
	binary.BigEndian.PutUint64(tail[:], v)
	copy(p[10:18], tail[:])
	copy(p[18:34], si.MD5[:])
	return p
}
