package e2e

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"slices"
	"sync"
	"testing"

	"github.com/gravity-zero/ac3go/ac3"
)

// These tests need the reference decoder, so they skip unless the environment
// describes one. What they can already check, before this module decodes a
// single sample, is that its framing agrees with the reference's: if our frame
// count times 1536 samples does not match the number of samples the reference
// produced from the same bytes, one of the two is wrong about where the frames
// are, and every later comparison would be built on sand.

// fixtures returns the committed synthetic streams. They are anonymous tones,
// so they can live in the repository; the real corpus cannot.
func fixtures(t testing.TB) map[string][]byte {
	t.Helper()
	files, err := filepath.Glob(filepath.Join("..", "..", "ac3", "testdata", "*.ac3"))
	if err != nil {
		t.Fatal(err)
	}
	out := map[string][]byte{}
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		out[filepath.Base(f)] = data
	}
	if len(out) == 0 {
		t.Fatal("no fixtures found")
	}
	return out
}

// walk returns the frame count and the header of the first frame.
func walk(t testing.TB, stream []byte) (frames int, h ac3.Header) {
	t.Helper()
	fr := ac3.NewFrameReader(bytes.NewReader(stream))
	for {
		frame, err := fr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("frame %d: %v", frames, err)
		}
		if err := ac3.CheckCRC(frame); err != nil {
			t.Fatalf("frame %d: %v", frames, err)
		}
		if frames == 0 {
			h = *fr.Header()
		}
		frames++
	}
	if got := fr.Skipped(); got != 0 {
		t.Errorf("skipped %d bytes of a stream that should be all frames", got)
	}
	return frames, h
}

// TestOracleAgreesOnFraming is the phase 0 end to end check: on every committed
// fixture, the number of samples the reference decoder produces must be exactly
// our frame count times 1536 times the channel count.
func TestOracleAgreesOnFraming(t *testing.T) {
	o := Setup(t)
	for name, stream := range fixtures(t) {
		t.Run(name, func(t *testing.T) {
			frames, h := walk(t, stream)
			if frames == 0 {
				t.Fatal("no frames")
			}
			pcm := o.DecodePCM(t, stream, 16)
			want := frames * ac3.SamplesPerFrame * h.Channels()
			if len(pcm) != want {
				t.Errorf("the reference decoded %d samples; %d frames of %d channels is %d",
					len(pcm), frames, h.Channels(), want)
			}
			if len(pcm) == 0 {
				t.Fatal("the reference decoded nothing")
			}
		})
	}
}

// TestOracleIsDeterministic checks the harness end to end against itself:
// staging, decoding, fetching and comparing all have to work before any of it
// can judge this module. Two decodes of the same bytes must be identical.
func TestOracleIsDeterministic(t *testing.T) {
	o := Setup(t)
	for name, stream := range fixtures(t) {
		t.Run(name, func(t *testing.T) {
			a := o.DecodePCM(t, stream, 16)
			b := o.DecodePCM(t, stream, 16)
			r, err := Compare(a, b, Exact)
			if err != nil {
				t.Fatalf("two decodes of the same stream differ: %v", err)
			}
			if !r.Equal {
				t.Errorf("two decodes of the same stream are not identical: %s", r)
			}
			if r.Samples == 0 {
				t.Fatal("the reference decoded nothing")
			}
		})
	}
}

// TestFixtureChannelsAreDistinct pins a property the fixtures must keep: every
// channel carries a different tone. A fixture whose channels are identical
// cannot catch a channel mapping bug, and it would pass every test here while
// hiding one.
func TestFixtureChannelsAreDistinct(t *testing.T) {
	o := Setup(t)
	for name, stream := range fixtures(t) {
		t.Run(name, func(t *testing.T) {
			_, h := walk(t, stream)
			if h.Channels() < 2 {
				t.Skip("a mono fixture has nothing to distinguish")
			}
			planes := Deinterleave(o.DecodePCM(t, stream, 16), h.Channels())
			layout := h.Layout()
			for a := range planes {
				for b := a + 1; b < len(planes); b++ {
					if r, err := Compare(planes[a], planes[b], Exact); err == nil && r.Equal {
						t.Errorf("channels %s and %s are identical: this fixture cannot catch a swap",
							layout[a], layout[b])
					}
				}
			}
		})
	}
}

// TestOracleDecodes24BitPCM checks the 24-bit path against the reference
// itself rather than against this harness's idea of the format, which is the
// only check worth anything here: the harness was reading three-byte samples
// as four-byte words, and every length it produced was a whole number of
// words, so nothing complained. It returned three quarters as many samples,
// each one built from parts of two, and called it the reference's answer.
//
// Two independent facts pin the packing. The sample count must be the one the
// framing predicts, which is what a wrong width breaks. And each 24-bit sample
// must be the top 24 bits of the reference's own 32-bit output for the same
// bytes, which is what a wrong offset or a wrong sign extension breaks.
func TestOracleDecodes24BitPCM(t *testing.T) {
	o := Setup(t)
	for name, stream := range fixtures(t) {
		t.Run(name, func(t *testing.T) {
			frames, h := walk(t, stream)
			if frames == 0 {
				t.Fatal("no frames")
			}

			s24 := o.DecodePCM(t, stream, 24)
			if want := frames * ac3.SamplesPerFrame * h.Channels(); len(s24) != want {
				t.Fatalf("the reference decoded %d samples at 24 bits; %d frames of %d channels is %d",
					len(s24), frames, h.Channels(), want)
			}

			// A 24-bit sample that does not fit in 24 bits was sign extended
			// from the wrong bit.
			for i, v := range s24 {
				if v < -(1 << 23) {
					t.Fatalf("sample %d is %d, below the 24-bit floor", i, v)
				}
				if v >= 1<<23 {
					t.Fatalf("sample %d is %d, above the 24-bit ceiling", i, v)
				}
			}

			s32 := o.DecodePCM(t, stream, 32)
			if len(s32) != len(s24) {
				t.Fatalf("the reference decoded %d samples at 32 bits and %d at 24",
					len(s32), len(s24))
			}
			for i := range s24 {
				if got, want := s32[i]>>8, s24[i]; got != want {
					t.Fatalf("sample %d: the reference's 32-bit output says %d, its 24-bit output says %d",
						i, got, want)
				}
			}
		})
	}
}

// TestOracleIsolatesConcurrentRuns is the harness checked against the way it is
// actually run. Two Oracles work at once, each decoding two different streams
// at once, and every answer must be the one a lone decode gives.
//
// This is not a hypothetical. The harness used to name its workdir and its
// files after what they held, so every run on the machine staged over the same
// paths: concurrent tests fed each other's streams to the reference and
// deleted each other's output while it was being read. The failure mode is the
// bad one: an oracle quietly reporting another run's audio as this stream's
// reference. So the check is that the answer is right, not merely that nothing
// crashed.
func TestOracleIsolatesConcurrentRuns(t *testing.T) {
	Setup(t) // Skip here, on this test, when there is no reference decoder.

	fx := fixtures(t)
	// Two fixtures that cannot be mistaken for one another: different channel
	// counts and different lengths, so a swap shows up as a length mismatch
	// and not merely as different audio.
	names := []string{"tone_48k_mono_96k.ac3", "tone_48k_5p1_448k.ac3"}
	for _, n := range names {
		if fx[n] == nil {
			t.Skipf("fixture %s is missing", n)
		}
	}

	// What each stream decodes to with nothing else running.
	alone := map[string][]int32{}
	for _, n := range names {
		alone[n] = Setup(t).DecodePCM(t, fx[n], 16)
		if len(alone[n]) == 0 {
			t.Fatalf("the reference decoded nothing from %s", n)
		}
	}

	for runner := range 2 {
		t.Run(fmt.Sprintf("runner-%d", runner), func(t *testing.T) {
			t.Parallel()
			// Each runner stands for a separate test setting up its own
			// Oracle, as two packages under "go test ./..." would.
			o := Setup(t)
			for _, n := range names {
				t.Run(n, func(t *testing.T) {
					// And each runner drives its own Oracle from two subtests
					// at once, decoding different streams through it.
					t.Parallel()
					for i := range 4 {
						got := o.DecodePCM(t, fx[n], 16)
						r, err := Compare(got, alone[n], Exact)
						if err != nil {
							t.Fatalf("decode %d of %s differs from the same stream decoded alone: %v",
								i, n, err)
						}
						if !r.Equal {
							t.Fatalf("decode %d of %s is not what the same stream decodes to alone: %s",
								i, n, r)
						}
					}
				})
			}
		})
	}
}

// TestComparatorCatchesADefectInRealAudio proves the comparator on real
// reference output rather than on synthetic arrays: a perturbation of the kind
// a decoder bug produces must be caught at the tolerance meant to catch it.
func TestComparatorCatchesADefectInRealAudio(t *testing.T) {
	o := Setup(t)
	stream := fixtures(t)["tone_48k_stereo_192k.ac3"]
	if stream == nil {
		t.Skip("the stereo fixture is missing")
	}
	ref := o.DecodePCM(t, stream, 16)
	if len(ref) == 0 {
		t.Fatal("the reference decoded nothing")
	}

	tests := []struct {
		name    string
		perturb func([]int32)
		tol     Tolerance
		wantErr bool
	}{
		{"untouched", func([]int32) {}, Exact, false},
		{"one sample off by one LSB", func(s []int32) { s[len(s)/2]++ }, Exact, true},
		{"one sample off by one LSB", func(s []int32) { s[len(s)/2]++ }, QuasiExact, false},
		{"one sample off by three LSB", func(s []int32) { s[len(s)/2] += 3 }, QuasiExact, true},
		{"half level, as a downmix or dialnorm bug would be",
			func(s []int32) {
				for i := range s {
					s[i] /= 2
				}
			}, Dithered, true},
		{"silence",
			func(s []int32) { clear(s) }, Dithered, true},
		// The stereo fixture carries a different tone in each channel, which
		// is what makes this case mean anything: swap two identical channels
		// and nothing has changed.
		{"the two channels swapped, as a layout bug would be",
			func(s []int32) {
				for i := 0; i+1 < len(s); i += 2 {
					s[i], s[i+1] = s[i+1], s[i]
				}
			}, Dithered, true},
	}
	for _, tt := range tests {
		t.Run(tt.name+" at "+tt.tol.String(), func(t *testing.T) {
			perturbed := make([]int32, len(ref))
			copy(perturbed, ref)
			tt.perturb(perturbed)
			_, err := Compare(perturbed, ref, tt.tol)
			if (err != nil) != tt.wantErr {
				t.Errorf("Compare = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

// The span of a real track a corpus test looks at. Well past any leader, long
// enough to cover a few hundred frames of real programme material.
const (
	corpusSpanStart   = 600
	corpusSpanSeconds = 30
)

// TestOracleOnRealCorpus runs the framing agreement against the real media the
// decoder is ultimately for. It needs the corpus as well as the reference, so
// it skips twice over. The corpus never enters the repository: only the counts
// derived from it appear here.
func TestOracleOnRealCorpus(t *testing.T) {
	o := Setup(t)
	dir := o.Corpus(t)

	sources := o.mediaFiles(t, dir, mediaFileLimit)
	if len(sources) == 0 {
		t.Skipf("no media files under %s", dir)
	}

	tested := 0
	for _, src := range sources {
		tracks := o.AC3Tracks(t, src)
		if len(tracks) == 0 {
			continue
		}
		tr := tracks[0]
		t.Run(path.Base(src), func(t *testing.T) {
			// A span rather than the whole track: a feature film's audio is
			// hundreds of thousands of frames, and the framing either agrees
			// or it does not.
			stream := o.ExtractSpan(t, src, tr.Index, corpusSpanStart, corpusSpanSeconds)
			if len(stream) == 0 {
				t.Skip("the extracted stream is empty")
			}
			frames, h := walk(t, stream)
			if frames == 0 {
				t.Fatal("no frames in the extracted stream")
			}
			t.Logf("%d frames, %s, %d kbit/s", frames, h.Format(), h.Sync.BitRate/1000)

			if h.Channels() != tr.Channels {
				t.Errorf("our header says %d channels, the reference says %d", h.Channels(), tr.Channels)
			}
			if h.Sync.SampleRate != tr.SampleRate {
				t.Errorf("our header says %d Hz, the reference says %d", h.Sync.SampleRate, tr.SampleRate)
			}

			pcm := o.DecodePCM(t, stream, 16)
			want := frames * ac3.SamplesPerFrame * h.Channels()
			if len(pcm) != want {
				t.Errorf("the reference decoded %d samples; %d frames of %d channels is %d",
					len(pcm), frames, h.Channels(), want)
			}
		})
		tested++
		if tested >= 3 {
			break // enough to prove the path; the corpus is large
		}
	}
	if tested == 0 {
		t.Skipf("no AC-3 track found under %s", dir)
	}
}

// mediaFiles lists media files under a runner-side directory, recursively.
// A max of zero means every one of them.
//
// Two things here are deliberate, and both were bugs first.
//
// The pattern goes in argv rather than through a shell. A shell would need the
// directory concatenated into a command line, and the directories real films
// live in have spaces in their names - so the listing would come back empty,
// which reads as "no corpus" rather than as "the path was split in three".
//
// And it recurses. A corpus of films is a flat directory; a corpus of series is
// nothing but sub-directories, and a non-recursive listing of one returns
// nothing at all. Every test that depends on it then skips, cheerfully, having
// concluded the corpus is empty - which is exactly what happened here, and cost
// a session's worth of believing there was no stereo AC-3 to be had.
func (o *Oracle) mediaFiles(t testing.TB, dir string, max int) []string {
	t.Helper()
	out, err := o.exec("find", dir, "-type", "f",
		"(", "-name", "*.mkv", "-o", "-name", "*.mp4", ")")
	if err != nil {
		// Not a skip and not an empty listing: a corpus path the container
		// cannot see is a broken configuration, and reporting it as "no media
		// files" is the same lie the comment above cost a session to. An empty
		// corpus makes find succeed and print nothing; only a real failure
		// lands here.
		t.Fatalf("listing the corpus under %s failed: %v", dir, err)
	}
	var files []string
	for _, line := range splitLines(out) {
		if line == "" {
			continue
		}
		files = append(files, line)
		if max > 0 && len(files) >= max {
			break
		}
	}
	return files
}

// mediaFileLimit is how many files the tests that probe every one of them will
// look at. Each costs a call into the reference, and the corpus of series holds
// several hundred: the point of those tests is that the framing holds on real
// encoders' output, which a spread of forty says as well as five hundred would.
const mediaFileLimit = 40

func splitLines(s string) []string {
	var out []string
	for _, line := range bytes.Split([]byte(s), []byte("\n")) {
		out = append(out, string(bytes.TrimSpace(line)))
	}
	return out
}

// TestWorkdirNamesAreUnique pins the property the isolation rests on. It needs
// no reference decoder: naming is this module's job, and it is the part that
// has to hold when "go test ./..." runs several package binaries at once.
func TestWorkdirNamesAreUnique(t *testing.T) {
	const goroutines, each = 8, 64

	names := make(chan string, goroutines*each)
	var wg sync.WaitGroup
	for range goroutines {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range each {
				n, err := newWorkdirName()
				if err != nil {
					t.Error(err)
					return
				}
				names <- n
			}
		}()
	}
	wg.Wait()
	close(names)

	seen := map[string]bool{}
	for n := range names {
		if seen[n] {
			t.Fatalf("newWorkdirName returned %q twice: two runs would share a workdir", n)
		}
		seen[n] = true
	}
	if len(seen) != goroutines*each {
		t.Errorf("got %d names, want %d", len(seen), goroutines*each)
	}
}

// TestUniqueNamesAreUnique covers the second half of the isolation: one Oracle
// is shared by the parallel subtests of one test, and they must not stage over
// each other inside the workdir they do share.
func TestUniqueNamesAreUnique(t *testing.T) {
	const goroutines, each = 8, 64

	o := &Oracle{}
	names := make(chan string, goroutines*each)
	var wg sync.WaitGroup
	for range goroutines {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range each {
				names <- o.uniqueName("decode-in", ".ac3")
			}
		}()
	}
	wg.Wait()
	close(names)

	seen := map[string]bool{}
	for n := range names {
		if seen[n] {
			t.Fatalf("uniqueName returned %q twice: two decodes would share a file", n)
		}
		seen[n] = true
	}
}

// TestUnpackPCM checks the packing the reference actually writes, and the
// guard on it. The 24-bit vectors are the ones a wrong width gets wrong:
// see TestOracleDecodes24BitPCM for the same claim put to the reference.
func TestUnpackPCM(t *testing.T) {
	tests := []struct {
		name    string
		raw     []byte
		bits    int
		want    []int32
		wantErr bool
	}{
		{"16 bits", []byte{0x00, 0x00, 0xff, 0x7f, 0x00, 0x80}, 16,
			[]int32{0, 32767, -32768}, false},
		// Three bytes per sample, little endian, sign extended from bit 23.
		{"24 bits", []byte{0x00, 0x00, 0x00, 0xff, 0xff, 0x7f, 0x00, 0x00, 0x80}, 24,
			[]int32{0, 8388607, -8388608}, false},
		{"24 bits, sign extension past the top byte", []byte{0xf9, 0x56, 0x81}, 24,
			[]int32{-8300807}, false},
		{"32 bits", []byte{0xff, 0xff, 0xff, 0x7f, 0x00, 0x00, 0x00, 0x80}, 32,
			[]int32{2147483647, -2147483648}, false},
		{"empty", nil, 24, []int32{}, false},
		// The guard the old width made useless: 12 bytes is a whole number of
		// 4-byte words, so reading 24-bit output as 32-bit sailed through it
		// and returned nine samples of nonsense as the reference's answer.
		{"24 bits, a whole number of 4-byte words but of 3-byte samples too",
			make([]byte, 12), 24, make([]int32, 4), false},
		{"24 bits, a partial sample", make([]byte, 10), 24, nil, true},
		{"16 bits, a partial sample", make([]byte, 3), 16, nil, true},
		{"32 bits, a partial sample", make([]byte, 6), 32, nil, true},
		{"a depth the reference is not asked for", make([]byte, 8), 8, nil, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := unpackPCM(tt.raw, tt.bits)
			if (err != nil) != tt.wantErr {
				t.Fatalf("unpackPCM = %v, wantErr %v", err, tt.wantErr)
			}
			if tt.wantErr {
				return
			}
			if !slices.Equal(got, tt.want) {
				t.Errorf("unpackPCM = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestSetupSkipsWithoutAnEnvironment is the guard on the guard: "go test ./..."
// on a machine with none of this must stay green.
func TestSetupSkipsWithoutAnEnvironment(t *testing.T) {
	t.Setenv(EnvMode, "")
	t.Setenv(EnvTool, "")

	skipped := runAndReportSkip(t, func(tb testing.TB) { Setup(tb) })
	if !skipped {
		t.Error("Setup did not skip with no environment set")
	}
}

// TestSetupSkipsWithoutATool covers the half-configured case: naming a runner
// but no binary is a skip, not a failure.
func TestSetupSkipsWithoutATool(t *testing.T) {
	t.Setenv(EnvMode, "local")
	t.Setenv(EnvTool, "")

	if !runAndReportSkip(t, func(tb testing.TB) { Setup(tb) }) {
		t.Error("Setup did not skip when no tool was named")
	}
}

// TestSetupSkipsOnAnAbsentTool covers a configured but wrong environment: a
// tool that is not installed is this machine's problem, not this module's.
func TestSetupSkipsOnAnAbsentTool(t *testing.T) {
	t.Setenv(EnvMode, "local")
	t.Setenv(EnvTool, "no-such-binary-ac3go-test")

	if !runAndReportSkip(t, func(tb testing.TB) { Setup(tb) }) {
		t.Error("Setup did not skip on a tool that is not installed")
	}
}

// TestSetupSkipsOnAnAbsentContainer covers a stale container name.
func TestSetupSkipsOnAnAbsentContainer(t *testing.T) {
	t.Setenv(EnvMode, "docker:no-such-container-ac3go-test")
	t.Setenv(EnvTool, "anything")

	if !runAndReportSkip(t, func(tb testing.TB) { Setup(tb) }) {
		t.Error("Setup did not skip on a container that does not exist")
	}
}

// TestCorpusSkipsWhenUnset checks the second gate independently of the first.
func TestCorpusSkipsWhenUnset(t *testing.T) {
	o := &Oracle{}
	if !runAndReportSkip(t, func(tb testing.TB) { o.Corpus(tb) }) {
		t.Error("Corpus did not skip when unset")
	}
}

// TestRequiredTurnsSkipsIntoFailures is the counterweight to every skip in this
// harness.
//
// The skips are what let "go test ./..." stay green on a machine with no
// reference decoder, and they are also what would hide the oracle going away:
// a renamed container reads exactly like a machine that never had one. Setting
// EnvRequired is the promise that an oracle is there, and the point of the
// promise is that breaking it fails.
//
// Both directions are checked. A mechanism that failed unconditionally would
// pass the first half of this and make the whole module unrunnable anywhere
// else.
func TestRequiredTurnsSkipsIntoFailures(t *testing.T) {
	for _, c := range []struct {
		name string
		fn   func(testing.TB)
	}{
		{"no mode", func(tb testing.TB) { Setup(tb) }},
		{"no corpus", func(tb testing.TB) { (&Oracle{}).Corpus(tb) }},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Setenv(EnvMode, "")
			t.Setenv(EnvTool, "")

			t.Setenv(EnvRequired, "")
			if rec := runAndRecord(t, c.fn); !rec.skipped || rec.failed {
				t.Errorf("unset: skipped=%v failed=%v, want a skip: a machine with no "+
					"oracle must still be able to run this module", rec.skipped, rec.failed)
			}

			t.Setenv(EnvRequired, "1")
			if rec := runAndRecord(t, c.fn); rec.skipped || !rec.failed {
				t.Errorf("%s=1: skipped=%v failed=%v, want a failure: a promised oracle "+
					"that is not there is the thing this exists to catch",
					EnvRequired, rec.skipped, rec.failed)
			}
		})
	}
}

// runAndReportSkip runs fn against a recording testing.TB and reports whether
// it skipped. Setup calls Skip on its argument, which stops the goroutine it
// runs on, so it gets one of its own.
//
// It clears EnvRequired first. Every caller is asserting that some
// misconfiguration is a skip, and EnvRequired is precisely what turns those
// skips into failures - so inheriting it from the environment would make all of
// them fail during a real oracle run, which is the one run where they are least
// welcome and hardest to read. Clearing it here rather than in each test is so
// that the next such test cannot forget to.
func runAndReportSkip(t *testing.T, fn func(testing.TB)) bool {
	t.Helper()
	t.Setenv(EnvRequired, "")
	return runAndRecord(t, fn).skipped
}

// runAndRecord is runAndReportSkip with the whole outcome, for the callers that
// need to tell a skip from a failure rather than just spot a skip.
func runAndRecord(t *testing.T, fn func(testing.TB)) *recorder {
	t.Helper()
	rec := &recorder{TB: t}
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer func() { recover() }()
		fn(rec)
	}()
	<-done
	return rec
}

// recorder stands in for a testing.TB so that a skip can be observed instead of
// skipping the test doing the observing.
type recorder struct {
	testing.TB
	skipped bool
	failed  bool
}

func (r *recorder) Helper()               {}
func (r *recorder) Log(...any)            {}
func (r *recorder) Logf(string, ...any)   {}
func (r *recorder) Skip(...any)           { r.SkipNow() }
func (r *recorder) Skipf(string, ...any)  { r.SkipNow() }
func (r *recorder) SkipNow()              { r.skipped = true; panic(skipSentinel{}) }
func (r *recorder) Fatal(...any)          { r.FailNow() }
func (r *recorder) Fatalf(string, ...any) { r.FailNow() }
func (r *recorder) Error(...any)          { r.failed = true }
func (r *recorder) Errorf(string, ...any) { r.failed = true }
func (r *recorder) FailNow()              { r.failed = true; panic(skipSentinel{}) }
func (r *recorder) Cleanup(func())        {}
func (r *recorder) Setenv(_, _ string)    {}
func (r *recorder) Name() string          { return "recorder" }

type skipSentinel struct{}
