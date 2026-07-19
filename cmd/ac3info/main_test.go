package main

import (
	"bufio"
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gravity-zero/ac3go/ac3"
)

// The fixtures are the ac3 package's, read rather than copied: this tool exists
// to report what that package parses, so a fixture of its own would let the two
// drift apart and still agree with themselves.
func fixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "ac3", "testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// runList is list with its output collected.
func runList(t *testing.T, stream []byte, limit int, summaryOnly, verbose, checkCRC bool) string {
	t.Helper()
	var buf bytes.Buffer
	out := bufio.NewWriter(&buf)
	if err := list(bytes.NewReader(stream), out, limit, summaryOnly, verbose, checkCRC); err != nil {
		t.Fatalf("list: %v", err)
	}
	if err := out.Flush(); err != nil {
		t.Fatal(err)
	}
	return buf.String()
}

// TestListSummary checks the numbers the summary reports against what the
// fixture is known to be.
//
// The duration line is the one worth having. It is derived rather than read -
// frames times blocks times 256, over the sampling rate - so it is the one
// number here that can be wrong while every field it is built from is right.
func TestListSummary(t *testing.T) {
	// Every frame of every fixture is six blocks, so the duration is frames
	// times 1536 over the rate. The counts are what the fixtures hold, read off
	// them rather than assumed.
	for _, c := range []struct {
		file     string
		frames   int
		duration string
	}{
		{"tones_48k_stereo_192k.ac3", 13, "0.416"},
		{"tones_48k_stereo_192k.eac3", 32, "1.024"},
		{"tones_48k_5p1_384k.eac3", 32, "1.024"},
	} {
		t.Run(c.file, func(t *testing.T) {
			got := runList(t, fixture(t, c.file), 0, true, false, true)

			for _, want := range []string{
				fmt.Sprintf("frames     %d", c.frames),
				// With the comma: "0 skipped" alone is a substring of "20
				// skipped" and of every other count ending in a zero.
				", 0 skipped",
				fmt.Sprintf("crc        0 bad of %d", c.frames),
				"duration   " + c.duration + " s",
				"shapes     1 distinct",
			} {
				if !strings.Contains(got, want) {
					t.Errorf("summary has no %q:\n%s", want, got)
				}
			}
		})
	}
}

// TestListDurationArithmetic checks the duration and the bit rate that follows
// from it.
//
// It does NOT check the thing that arithmetic exists for, and saying so is the
// point of this comment. An AC-3 frame is always six blocks; an enhanced frame
// is one, two, three or six. Counting 1536 samples a frame - which this tool
// did until the block count was wired in - is right about every stream anyone
// here has and wrong about a short-frame one, by up to six times on the
// duration and as much the other way on the bit rate, in both cases producing a
// plausible number rather than an obvious mistake.
//
// Every fixture is a six-block stream, so both the right formula and the wrong
// one give the numbers below and this test passes either way. Catching that
// needs a short-frame fixture, and neither the corpus nor the encoder that made
// these has one: no real stream measured so far uses fewer than six blocks, and
// building one by hand means writing an enhanced frame from nothing. It is in
// the backlog. Until then the block count is checked in the ac3 package, where
// the field is parsed, and this checks only that the summary multiplies what it
// is given.
func TestListDurationArithmetic(t *testing.T) {
	got := runList(t, fixture(t, "tones_48k_stereo_192k.eac3"), 0, true, false, false)
	// 32 frames, 6 blocks each, 256 samples a block.
	if !strings.Contains(got, "(49152 samples at 48000 Hz)") {
		t.Errorf("summary does not report 32*6*256 samples:\n%s", got)
	}
	// And the bit rate that follows from it: 24576 bytes over 1.024 s.
	if !strings.Contains(got, "bitrate    192.0 kbit/s") {
		t.Errorf("summary does not report 192 kbit/s:\n%s", got)
	}
}

// TestListPerFrameLines checks the listing itself, and that -limit stops it.
func TestListPerFrameLines(t *testing.T) {
	got := runList(t, fixture(t, "tones_48k_stereo_192k.eac3"), 3, false, false, false)

	for _, want := range []string{
		"frame 0      @0",
		"frame 1      @768",
		"frame 2      @1536",
		"bsid=16",
		"acmod=2(2/0)",
		"frames     3",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("listing has no %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "frame 3 ") {
		t.Errorf("limit 3 listed a fourth frame:\n%s", got)
	}
}

// TestListVerboseFields checks that the field dump reaches the fields, since
// the whole point of the tool is that a reader can see them.
func TestListVerboseFields(t *testing.T) {
	got := runList(t, fixture(t, "tones_48k_5p1_384k.eac3"), 1, false, true, false)
	// These have to be strings the dump alone emits. The bare per-frame line
	// already names dialnorm, acmod and bsid, so asserting those words would
	// pass with the whole dump deleted.
	for _, want := range []string{
		"syncinfo   fscod=",
		"bsi        bsid=",
		"dialnorm   ",
		"flags      copyrightb=",
		"audio      starts at bit ",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("verbose dump has no %q:\n%s", want, got)
		}
	}
	// And the dump must be what carries them: the same run without -v must not.
	terse := runList(t, fixture(t, "tones_48k_5p1_384k.eac3"), 1, false, false, false)
	if strings.Contains(terse, "syncinfo   fscod=") {
		t.Errorf("the field dump appears without -v:\n%s", terse)
	}
}

// TestListReportsBadCRC checks that a broken frame is counted rather than
// waved through, and that it takes -crc to notice.
//
// The frame reader verifies check words itself, so a corrupt frame never
// reaches the listing at all: it is skipped, and the bytes it occupied are
// reported as skipped. That is what this pins - a stream with a broken frame
// must not read as a stream of good frames.
func TestListReportsBadCRC(t *testing.T) {
	stream := bytes.Clone(fixture(t, "tones_48k_stereo_192k.eac3"))
	// Break a byte in the middle of the second frame's payload.
	stream[768+400] ^= 0xFF

	got := runList(t, stream, 0, true, false, true)
	if strings.Contains(got, "frames     32") {
		t.Errorf("a stream with a corrupt frame still reported all 32 frames:\n%s", got)
	}
	if strings.Contains(got, ", 0 skipped") {
		t.Errorf("a corrupt frame's bytes were not reported as skipped:\n%s", got)
	}
}

// TestListEmptyStream pins that nothing in is not an error out: the summary
// says there was nothing, and says it without dividing by a sample rate it does
// not have.
func TestListEmptyStream(t *testing.T) {
	got := runList(t, nil, 0, true, false, false)
	if !strings.Contains(got, "frames     0") {
		t.Errorf("empty stream:\n%s", got)
	}
	if strings.Contains(got, "duration") {
		t.Errorf("empty stream reported a duration:\n%s", got)
	}
}

// eac3ShortFrame builds an enhanced frame of nblkscod-coded blocks with an
// empty payload. No encoder within reach emits frames of fewer than six
// blocks, so the only way to hold the duration arithmetic to one is to write
// the frame by hand. The payload is silence the tool never reads - it lists
// headers - and the check words are absent, which is why the tests that use
// this pass checkCRC false.
func eac3ShortFrame(nblkscod uint32, sizeBytes int) []byte {
	var buf []byte
	var acc uint64
	var nacc uint
	write := func(v uint32, n uint) {
		acc = acc<<n | uint64(v)
		nacc += n
		for nacc >= 8 {
			nacc -= 8
			buf = append(buf, byte(acc>>nacc))
		}
	}
	write(0x0B77, 16)                // syncword
	write(0, 2)                      // strmtyp: independent
	write(0, 3)                      // substreamid
	write(uint32(sizeBytes/2-1), 11) // frmsiz, in words from zero
	write(0, 2)                      // fscod: 48 kHz
	write(nblkscod, 2)               // numblkscod
	write(2, 3)                      // acmod: stereo
	write(0, 1)                      // lfeon
	write(16, 5)                     // bsid
	write(31, 5)                     // dialnorm
	write(0, 1)                      // compre
	write(0, 1)                      // mixmdate
	write(0, 1)                      // infomdate
	write(0, 1)                      // convsync (fewer than six blocks)
	write(0, 1)                      // addbsie
	for nacc > 0 {
		write(0, 1)
	}
	for len(buf) < sizeBytes {
		buf = append(buf, 0)
	}
	// The frame reader verifies the check word no matter what the tool was
	// asked, so the frame has to carry a true one. Searching the two bytes is
	// simpler than reimplementing the polynomial here, and it cannot drift
	// from what the reader checks, because the reader's own check is the test.
	for w := 0; w < 1<<16; w++ {
		buf[sizeBytes-2], buf[sizeBytes-1] = byte(w>>8), byte(w)
		if ac3.CheckCRC(buf) == nil {
			return buf
		}
	}
	panic("no check word satisfies the frame")
}

// TestListShortFrames pins the duration of frames of fewer than six blocks.
// The bug this guards against was real: the tool once counted 1536 samples
// per frame no matter what the frame said, which reads plausibly - it is the
// only length AC-3 has - and overstates the duration of a short-frame stream
// by up to six times.
func TestListShortFrames(t *testing.T) {
	for _, c := range []struct {
		nblkscod uint32
		frames   int
		duration string
	}{
		{0, 6, "0.032"}, // 6 frames of 1 block: 1536 samples
		{1, 3, "0.032"}, // 3 frames of 2 blocks: the same span said differently
		{2, 4, "0.064"}, // 4 frames of 3 blocks
	} {
		var stream []byte
		for range c.frames {
			stream = append(stream, eac3ShortFrame(c.nblkscod, 128)...)
		}
		got := runList(t, stream, 0, true, false, false)
		for _, want := range []string{
			fmt.Sprintf("frames     %d", c.frames),
			"duration   " + c.duration + " s",
		} {
			if !strings.Contains(got, want) {
				t.Errorf("nblkscod %d: summary has no %q:\n%s", c.nblkscod, want, got)
			}
		}
	}
}
