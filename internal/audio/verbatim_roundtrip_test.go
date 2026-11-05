package audio

import (
	"bytes"
	"testing"

	"github.com/mewkiz/flac/frame"
)

// Round-trip: our verbatim frame must parse cleanly through mewkiz, which
// applies the spec's subframe header layout (pad + type + wasted flag).
func TestVerbatimFrameMewkizRoundTrip(t *testing.T) {
	n := 64
	ch0 := make([]int32, n)
	ch1 := make([]int32, n)
	for i := range ch0 {
		ch0[i] = int32(i*37 - 1000)
		ch1[i] = int32(1000 - i*13)
	}
	raw, err := encodeVerbatimFrame([][]int32{ch0, ch1}, 12083400, 16)
	if err != nil {
		t.Fatal(err)
	}
	f, err := frame.New(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("frame header: %v", err)
	}
	// No BitsPerSample is injected: the header's explicit sample-size code
	// must be enough for a standalone parse (sizecode 000 is not, see
	// encodeVerbatimFrame).
	if f.BlockSize != uint16(n) {
		t.Fatalf("blocksize %d, want %d", f.BlockSize, n)
	}
	if err := f.Parse(); err != nil {
		t.Fatalf("frame.Parse: %v (frame bytes: %x...)", err, raw[:16])
	}
	for ch, want := range [][]int32{ch0, ch1} {
		got := f.Subframes[ch].Samples
		if len(got) != n {
			t.Fatalf("ch%d: %d samples, want %d", ch, len(got), n)
		}
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("ch%d sample %d = %d, want %d", ch, i, got[i], want[i])
			}
		}
	}
}

// TestRewriteHeaderRoundTrip: a copied frame whose header is renumbered for
// the new stream must stay standalone-parseable — including sources that
// carry sizecode 000 ("from STREAMINFO"), which rewriteHeader normalises to
// the explicit code.
func TestRewriteHeaderRoundTrip(t *testing.T) {
	n := 32
	ch0 := make([]int32, n)
	ch1 := make([]int32, n)
	for i := range ch0 {
		ch0[i] = int32(i*31 - 500)
		ch1[i] = int32(700 - i*17)
	}
	src, err := encodeVerbatimFrame([][]int32{ch0, ch1}, 5963, 16)
	if err != nil {
		t.Fatal(err)
	}
	// Simulate a source frame that declares "from STREAMINFO": clear the
	// sample-size nibble and recompute the header CRC-8 + frame CRC-16.
	// Header layout of encodeVerbatimFrame's output: 2 sync + 1 bs/sr +
	// 1 ch/size + 3 coded number + 2 block size + 1 CRC-8.
	nl := 3 // 5963 needs a 3-byte coded number
	crcAt := 4 + nl + 2
	src[3] &^= 0x0F
	src[crcAt] = crc8(src[:crcAt])
	c := crc16(src[:len(src)-2])
	src[len(src)-2], src[len(src)-1] = byte(c>>8), byte(c)

	out, err := rewriteHeader(src, stratPos, 0, 16)
	if err != nil {
		t.Fatal(err)
	}
	if out[3]&0x0F != 8 {
		t.Fatalf("size nibble = %#x, want 0x8 (explicit 16-bit code4<<1)", out[3]&0x0F)
	}
	f, err := frame.New(bytes.NewReader(out))
	if err != nil {
		t.Fatalf("frame header: %v", err)
	}
	if err := f.Parse(); err != nil {
		t.Fatalf("frame.Parse: %v", err)
	}
	if f.Subframes[0].Samples[0] != ch0[0] || f.Subframes[1].Samples[n-1] != ch1[n-1] {
		t.Fatalf("samples do not survive the rewrite: %d/%d vs %d/%d",
			f.Subframes[0].Samples[0], f.Subframes[1].Samples[n-1], ch0[0], ch1[n-1])
	}
}
