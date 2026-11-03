package audio

import (
	"bufio"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
)

// FlacFrame is one audio frame of a FLAC file.
type FlacFrame struct {
	Offset        int64 // byte offset of the frame header
	Length        int   // header + subframes + CRC-16
	Start         int64 // absolute sample index of the first sample
	Samples       int   // samples in this frame
	BlocksizeCode int
	Coding        string // "fixed" or "variable"
}

// maxLeadingJunk bounds the gap tolerated between the metadata chain and the
// first frame (measured: 8 stray zero bytes on a real album).
const maxLeadingJunk = 4096

// FlacIndex describes the layout of a FLAC file without decoding it.
type FlacIndex struct {
	StreamInfo   StreamInfo
	FramesOffset int64
	Frames       []FlacFrame
	TotalSamples int64 // from the frame chain itself
	Trailer      int64 // bytes after the last frame (should be 0)
	// LeadingJunk is the gap between the metadata chain and the first sync
	// word; anything but zero means the file carried stray bytes but is
	// otherwise walkable.
	LeadingJunk int64
}

// maxEndAttempts bounds how many candidate ends are tried for one frame;
// false sync words are rare enough that a handful is plenty.
const maxEndAttempts = 64

// IndexFlacFrames walks a FLAC file's frame chain (FLAC spec §9) without
// decoding anything: it finds each frame header, derives the frame's sample
// count from its header and validates the frame's CRC-16 to confirm where it
// ends. The result is what a FUSE backend needs to synthesise a track's bytes
// (and it is also a cheap integrity check, because the frame chain must add
// up with STREAMINFO.total_samples).
func IndexFlacFrames(path string) (*FlacIndex, error) {
	blocks, si, framesOffset, err := ReadMetaBlocks(path)
	if err != nil {
		return nil, err
	}
	f, err := openSize(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	idx := &FlacIndex{StreamInfo: si, FramesOffset: framesOffset}
	_ = blocks
	if framesOffset >= f.size {
		return idx, nil
	}
	cands, err := scanSyncCandidates(f, framesOffset)
	if err != nil {
		return nil, err
	}
	if len(cands) == 0 {
		return nil, fmt.Errorf("%s: no FLAC frame sync found after the metadata blocks", path)
	}

	// The metadata block chain can be followed by stray bytes that some
	// taggers wrote (measured: 8 zero bytes on a real album). Tolerate a
	// bounded gap before the first frame, then walk the chain strictly.
	expect := framesOffset
	pi := 0
	failures := 0
	buf := make([]byte, 0, 1<<16)
	var lastSample int64

	probe := make([]byte, 32)
	skipped := int64(0)
	for {
		for pi < len(cands) && cands[pi] < expect {
			pi++
		}
		if pi >= len(cands) || cands[pi]-framesOffset > maxLeadingJunk {
			return nil, fmt.Errorf("%s: no valid frame header within %d bytes of the metadata chain (%d)",
				path, maxLeadingJunk, expect-framesOffset)
		}
		if n, _ := readAt(f, probe, cands[pi]); n > 0 {
			if _, fhdr, herr := parseFrameHeader(probe[:n], &si); herr == nil {
				if !si.Present || fhdr.Samples <= si.MaxBlocksize {
					skipped = cands[pi] - framesOffset
					expect = cands[pi]
					break
				}
			}
		}
		pi++
	}
	idx.LeadingJunk = skipped

	// From here the frame chain is strictly recursive: the frame that has to
	// exist at offset E is known once the previous frame ends, so the walk is
	// O(file) instead of O(candidates^2). A break in the chain is reported as
	// an error but whatever was indexed before the break stays in idx, which
	// keeps flacinfo useful on damaged files.
	walkErr := error(nil)
	for expect < f.size {
		for pi < len(cands) && cands[pi] < expect {
			pi++
		}
		if pi >= len(cands) {
			msg := fmt.Sprintf(
				"%s: frame chain broken at offset %d: no frame sync follows the previous frame",
				path, expect)
			if len(idx.Frames) > 0 {
				last := idx.Frames[len(idx.Frames)-1]
				msg += fmt.Sprintf(" (walked %d frame(s); last one starts at %d, %d byte(s))",
					len(idx.Frames), last.Offset, last.Length)
			}
			walkErr = errors.New(msg)
			break
		}
		if cands[pi] != expect {
			walkErr = fmt.Errorf(
				"%s: frame chain broken at offset %d: next sync word found at %d instead",
				path, expect, cands[pi])
			break
		}
		accepted := false
		for k := pi + 1; k <= len(cands) && k <= pi+maxEndAttempts; k++ {
			j := f.size
			if k < len(cands) {
				j = cands[k]
			}
			if j-expect < 8 { // header + CRC-16 cannot be shorter
				continue
			}
			length := int(j - expect)
			if cap(buf) < length {
				buf = make([]byte, length+(1<<16))
			}
			buf = buf[:length]
			if _, err := readAt(f, buf, expect); err != nil {
				return idx, err
			}
			_, frame, herr := parseFrameHeader(buf, &si)
			if herr != nil {
				continue
			}
			// The block size range is an impostor filter, but only from
			// above: the last frame of a FLAC file is legitimately shorter
			// than the rest, and ffmpeg's encoder even writes
			// min = max = typical block size (measured).
			if si.Present && frame.Samples > si.MaxBlocksize {
				continue
			}
			want := uint16(buf[length-2])<<8 | uint16(buf[length-1])
			if crc16(buf[:length-2]) != want {
				continue
			}
			frame.Offset = expect
			frame.Length = length
			frame.Start = lastSample
			idx.Frames = append(idx.Frames, frame)
			lastSample += int64(frame.Samples)
			expect += int64(length)
			accepted = true
			break
		}
		if !accepted {
			failures++
			pi++
			if failures > maxEndAttempts {
				walkErr = fmt.Errorf(
					"%s: cannot find a frame whose CRC-16 validates at offset %d; the file is damaged or not FLAC",
					path, expect)
				break
			}
		}
	}
	if len(idx.Frames) == 0 && walkErr == nil {
		walkErr = fmt.Errorf("%s: no valid FLAC frames found (%d false sync candidates)", path, failures)
	}
	idx.TotalSamples = lastSample
	if last := len(idx.Frames); last > 0 {
		fl := idx.Frames[last-1]
		idx.Trailer = f.size - (fl.Offset + int64(fl.Length))
	} else {
		idx.Trailer = f.size - expect
	}
	if si.Present && si.TotalSamples != 0 && idx.TotalSamples != si.TotalSamples && walkErr == nil {
		walkErr = fmt.Errorf(
			"%s: frame chain totals %d samples but STREAMINFO says %d",
			path, idx.TotalSamples, si.TotalSamples)
	}
	return idx, walkErr
}

// parseFrameHeader decodes a FLAC frame header (FLAC spec §9). buf must start
// at the sync word and contain the whole header. It returns the header length
// in bytes (including the CRC-8) and the frame's sample count.
//
// Block size codes 0000 and 1000-1111 mean "take the block size from
// STREAMINFO"; that is what ffmpeg's encoder writes (measured: code 12 with
// no extra bytes), so si is required.
func parseFrameHeader(buf []byte, si *StreamInfo) (int, FlacFrame, error) {
	var f FlacFrame
	if len(buf) < 6 {
		return 0, f, errors.New("frame shorter than a header")
	}
	if buf[0] != 0xFF || buf[1]&0xFC != 0xF8 {
		return 0, f, errors.New("no sync word")
	}
	f.Coding = "fixed"
	if buf[1]&0x02 != 0 {
		f.Coding = "variable"
	}
	bsCode := int(buf[2] >> 4)
	srCode := int(buf[2] & 0x0F)
	f.BlocksizeCode = bsCode
	fromStream := bsCode == 0 || bsCode >= 8
	if fromStream {
		if si == nil || !si.Present || si.MaxBlocksize <= 0 {
			return 0, f, fmt.Errorf(
				"block size code %d needs STREAMINFO, which is missing", bsCode)
		}
		f.Samples = si.MaxBlocksize
	}

	// Coded frame/sample number (UTF-8 style, 1..7 bytes), from byte 4 on.
	numLen, err := utf8CodedLength(buf[4])
	if err != nil {
		return 0, f, err
	}
	pos := 4 + numLen

	if !fromStream {
		// Optional block size field, then the optional sample rate field.
		switch bsCode {
		case 1:
			f.Samples = 192
		case 2, 3, 4, 5:
			f.Samples = 576 << (bsCode - 2) // 576, 1152, 2304, 4608
		case 6:
			if pos+1 > len(buf) {
				return 0, f, errors.New("truncated header")
			}
			f.Samples = int(buf[pos]) + 1
			pos++
		case 7:
			if pos+2 > len(buf) {
				return 0, f, errors.New("truncated header")
			}
			f.Samples = int(binary.BigEndian.Uint16(buf[pos:pos+2])) + 1
			pos += 2
		}
	}
	switch srCode {
	case 0, 8, 9, 10, 11:
		// Direct rates from the table, or "from STREAMINFO": no extra bytes.
	case 12:
		pos++
	case 13, 14:
		pos += 2
	case 15:
		return 0, f, errors.New("invalid sample rate code 15")
	}
	if pos < 0 || pos+1 > len(buf) {
		return 0, f, errors.New("truncated header")
	}
	if got := crc8(buf[:pos]); got != buf[pos] {
		return 0, f, fmt.Errorf("frame header CRC-8 mismatch (got %02x want %02x)", got, buf[pos])
	}
	if f.Samples <= 0 {
		return 0, f, fmt.Errorf("frame header has block size %d", f.Samples)
	}
	return pos + 1, f, nil
}

// utf8CodedLength reports how many bytes the coded number field occupies.
func utf8CodedLength(first byte) (int, error) {
	switch {
	case first < 0x80:
		return 1, nil
	case first < 0xC0:
		return 0, errors.New("invalid coded number 10xxxxxx")
	case first&0xE0 == 0xC0:
		return 2, nil
	case first&0xF0 == 0xE0:
		return 3, nil
	case first&0xF8 == 0xF0:
		return 4, nil
	case first&0xFC == 0xF8:
		return 5, nil
	case first&0xFE == 0xFC:
		return 6, nil
	case first == 0xFE:
		return 7, nil
	}
	return 0, errors.New("invalid coded number 11111111")
}

// scanSyncCandidates walks the frames region once, recording the offset of
// every 0xFF 0b111110xx pattern.
func scanSyncCandidates(f *sizeFile, from int64) ([]int64, error) {
	const chunk = 1 << 20
	if _, err := f.Seek(from, io.SeekStart); err != nil {
		return nil, err
	}
	rd := bufio.NewReaderSize(f, chunk)
	var cands []int64
	var prev byte
	var off = from
	buf := make([]byte, chunk)
	for {
		n, err := rd.Read(buf)
		if n > 0 {
			for i := 0; i < n; i++ {
				b := buf[i]
				if prev == 0xFF && b&0xFC == 0xF8 {
					cands = append(cands, off+int64(i)-1)
					if len(cands) > 1<<20 {
						return nil, errors.New("too many sync candidates; file does not look like FLAC")
					}
				}
				prev = b
			}
			off += int64(n)
		}
		if err == io.EOF || (err == nil && n == 0) {
			break
		}
		if err != nil {
			return nil, err
		}
	}
	return cands, nil
}

// sizeFile is a small wrapper so the scanner and the validator share one
// file handle plus its size.
type sizeFile struct {
	*os.File
	size int64
}

func openSize(path string) (*sizeFile, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	st, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, err
	}
	return &sizeFile{File: f, size: st.Size()}, nil
}

func readAt(f *sizeFile, buf []byte, off int64) (int, error) {
	n, err := f.File.ReadAt(buf, off)
	if err != nil && err != io.EOF {
		return n, err
	}
	if n != len(buf) {
		return n, io.ErrUnexpectedEOF
	}
	return n, nil
}
