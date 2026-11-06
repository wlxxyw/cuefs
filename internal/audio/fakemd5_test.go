package audio

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

// buildFakeSource makes a small FLAC of identical, decodable verbatim frames
// (silence-free dither so the PCM is not all-zero), STREAMINFO set for three
// 4096-sample frames of stereo 16-bit audio.
func buildFakeSource(t *testing.T, dir string) (string, *FlacIndex) {
	t.Helper()
	const n = 4096
	ch0 := make([]int32, n)
	ch1 := make([]int32, n)
	s0, s1 := 0, 0
	for i := range ch0 {
		// A deterministic dither keeps the verbatim bodies non-trivial.
		s0 = (s0*7 + 3) % 100
		s1 = (s1*13 + 5) % 90
		ch0[i], ch1[i] = int32(s0)-50, int32(s1)-45
	}
	frame, err := encodeVerbatimFrame([][]int32{ch0, ch1}, 0, 16)
	if err != nil {
		t.Fatal(err)
	}
	si := streamInfoBytes(n, 44100, 2, 16, n*3)
	path := filepath.Join(dir, "album.flac")
	if err := os.WriteFile(path, buildFlac(si, []VorbisEntry{
		{Key: "TITLE", Value: "x"},
	}, [][]byte{frame, frame, frame}), 0o644); err != nil {
		t.Fatal(err)
	}
	idx, err := IndexFlacFrames(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(idx.Frames) != 3 {
		t.Fatalf("index walks %d frame(s), want 3", len(idx.Frames))
	}
	return path, idx
}

// idxStub adapts a prebuilt index to the resolver contract.
type idxStub struct{ idx *FlacIndex }

func (r *idxStub) Index(path string) (*FlacIndex, error) { return r.idx, nil }

// FakeSpec splits src over [start,end) on a fixture index.
func FakeSpec(src string, idx IndexResolver, start, end int64, fake bool) SplitSpec {
	return SplitSpec{
		Src: src, Start: start, End: end,
		SampleRate: 44100, Channels: 2, Bits: 16,
		Indexer:           idx,
		FakeStreamInfoMD5: fake,
	}
}

// TestWriteFLACTrackFakeMD5: the fake path writes zeros into STREAMINFO,
// keeps the audio frames byte-identical to the real-md5 path, and still
// re-encodes the boundary frames (this window starts and ends mid-frame).
func TestWriteFLACTrackFakeMD5(t *testing.T) {
	dir := t.TempDir()
	src, idx := buildFakeSource(t, dir)

	// Frames cover [0,12288): both ends of the window hit frame interiors.
	outReal := filepath.Join(dir, "real.flac")
	outFake := filepath.Join(dir, "fake.flac")
	if err := WriteFLACTrack(FakeSpec(src, &idxStub{idx}, 2048, 10240, false), outReal); err != nil {
		t.Fatal(err)
	}
	if err := WriteFLACTrack(FakeSpec(src, &idxStub{idx}, 2048, 10240, true), outFake); err != nil {
		t.Fatal(err)
	}

	_, siReal, _, err := ReadMetaBlocks(outReal)
	if err != nil {
		t.Fatal(err)
	}
	_, siFake, _, err := ReadMetaBlocks(outFake)
	if err != nil {
		t.Fatal(err)
	}
	if want := int64(10240 - 2048); siReal.TotalSamples != want || siFake.TotalSamples != want {
		t.Errorf("totals %d/%d, want %d", siReal.TotalSamples, siFake.TotalSamples, want)
	}
	if zeros := ([16]byte{}); siFake.MD5 != zeros {
		t.Errorf("fake md5 = %x, want all zeros", siFake.MD5)
	}
	if siReal.MD5 == siFake.MD5 {
		t.Error("real and fake md5 agree; the flag did nothing")
	}

	// Audio: the frame payloads must be byte-identical between the runs.
	rawReal, err := os.ReadFile(outReal)
	if err != nil {
		t.Fatal(err)
	}
	rawFake, err := os.ReadFile(outFake)
	if err != nil {
		t.Fatal(err)
	}
	_, _, framesReal, err := ReadMetaBlocks(outReal)
	if err != nil {
		t.Fatal(err)
	}
	_, _, framesFake, err := ReadMetaBlocks(outFake)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(rawReal[framesReal:], rawFake[framesFake:]) {
		t.Error("fake-md5 run produced different audio frames")
	}
}

// TestWriteFLACTrackFakeMD5Decodes: decoding both outputs independently
// yields the very same samples - a reader that ignores the md5 field sees
// identical audio either way.
func TestWriteFLACTrackFakeMD5Decodes(t *testing.T) {
	dir := t.TempDir()
	src, _ := buildFakeSource(t, dir)

	pcmOf := func(name string, fake bool) []int32 {
		out := filepath.Join(dir, name)
		start, end := int64(100), int64(3*4096-100)
		if err := WriteFLACTrack(FakeSpec(src, nil, start, end, fake), out); err != nil {
			t.Fatalf("split %s: %v", name, err)
		}
		idx, err := IndexFlacFrames(out)
		if err != nil {
			t.Fatal(err)
		}
		samples, err := DecodeIndexRange(out, idx.Frames)
		if err != nil {
			t.Fatal(err)
		}
		var flat []int32
		for _, fr := range samples {
			for _, chs := range fr {
				flat = append(flat, chs...)
			}
		}
		return flat
	}

	real := pcmOf("real.flac", false)
	fake := pcmOf("fake.flac", true)
	if len(real) != len(fake) {
		t.Fatalf("sample counts differ: %d vs %d", len(real), len(fake))
	}
	// Stereo interleaved: compare every sample.
	for i := range real {
		if real[i] != fake[i] {
			t.Fatalf("sample %d differs: %d vs %d", i, real[i], fake[i])
		}
	}
}
