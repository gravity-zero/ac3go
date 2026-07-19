// Package e2e is the test harness that checks this decoder against a
// reference decoder.
//
// A decoder is only correct against something. The something here is an
// external reference decoder, invoked as a subprocess, either on the local
// PATH or inside a container. It does two jobs: it cuts elementary AC-3
// streams out of real media files, and it decodes those streams to the PCM
// this module's output is compared against.
//
// Nothing in this package is imported by the library. The module stays free of
// dependencies; the harness only shells out and compares bytes.
//
// # Configuration
//
// The harness is off unless the environment says otherwise, so that a plain
// "go test ./..." stays green on a machine that has none of this. Setup skips
// the test when a variable is missing, and never fails for want of an
// environment.
//
//	AC3GO_E2E          where to run the reference decoder:
//	                   "docker:<container>" runs it in a running container,
//	                   "local" runs it on the PATH. Unset means skip.
//	AC3GO_E2E_TOOL     name of the reference decoder binary.
//	AC3GO_E2E_PROBE    name of its companion stream-inspection binary.
//	                   Defaults to AC3GO_E2E_TOOL.
//	AC3GO_E2E_CORPUS   a directory of real media files, as the runner sees it
//	                   (inside the container, in docker mode). Unset means the
//	                   tests that need real media skip.
//
// The three command lines default to the argv the harness knows, with the
// binary substituted in. Override them when the reference decoder wants a
// different spelling. {tool}, {src}, {track}, {ss}, {durflag}, {dur}, {bits},
// {in}, {out} and {fmt} are substituted.
//
// A template is split into arguments on spaces once, before substitution, and
// never again: a substituted value is always exactly one argument, so a media
// path may contain spaces. An argument that expands to nothing is dropped.
//
//	AC3GO_E2E_EXTRACT_ARGV
//	AC3GO_E2E_DECODE_ARGV
//	AC3GO_E2E_PROBE_ARGV
package e2e

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// Environment variables the harness reads.
const (
	EnvMode     = "AC3GO_E2E"
	EnvTool     = "AC3GO_E2E_TOOL"
	EnvProbe    = "AC3GO_E2E_PROBE"
	EnvCorpus   = "AC3GO_E2E_CORPUS"
	EnvExtract  = "AC3GO_E2E_EXTRACT_ARGV"
	EnvDecode   = "AC3GO_E2E_DECODE_ARGV"
	EnvProbeCmd = "AC3GO_E2E_PROBE_ARGV"

	// EnvRequired asserts that an oracle is available. Set it and every reason
	// this harness would otherwise skip becomes a failure.
	//
	// It exists because the skips are indistinguishable from success. A stale
	// container name, an image rebuilt without the tool, a corpus path the
	// runner cannot see: each one is a skip, so the run stays green having
	// compared nothing, and it stays green every day after. The comparison is
	// the only thing that proves this decoder against the reference, and losing
	// it silently is worse than not having it.
	//
	// Set it wherever the oracle is supposed to exist - which is the developer's
	// machine, not a hosted runner. See the note in .github/workflows/ci.yml.
	EnvRequired = "AC3GO_E2E_REQUIRED"
)

// Default argv for the reference decoder. They are templates, not a tool: the
// binary comes from the environment, and any of them can be replaced there.
const (
	defaultExtractArgv = "{tool} -v error -ss {ss} {durflag} {dur} -i {src} -map 0:{track} -c copy -f {fmt} -y {out}"
	defaultDecodeArgv  = "{tool} -v error {decflag} {decval} -i {in} -f s{bits}le -y {out}"
	defaultProbeArgv   = "{tool} -v error -select_streams a -show_entries stream=index,codec_name,channels,sample_rate -of csv=p=0 {src}"
)

// commandTimeout bounds any single call into the reference decoder. Decoding a
// half hour of audio is fast; a call that takes longer than this has hung.
const commandTimeout = 5 * time.Minute

// An Oracle runs the reference decoder.
type Oracle struct {
	container string // empty when running on the local PATH
	tool      string
	probe     string
	corpus    string

	extractArgv []string
	decodeArgv  []string
	probeArgv   []string

	// workdir is where staged files live on the runner's side. It belongs to
	// this Oracle alone: see newWorkdirName.
	workdir string

	// seq numbers the files this Oracle stages, so that two calls never write
	// the same path. The workdir is already private to this Oracle, but a
	// single Oracle is shared by the parallel subtests of one test, and they
	// decode different streams through it.
	seq atomic.Uint64
}

// newWorkdirName returns a name no other Oracle will use.
//
// The runner's /tmp is shared by every test binary on the machine, and a
// harness that named its files after what they hold would have concurrent runs
// overwrite each other's streams and delete each other's output. That does not
// fail loudly: it decodes the wrong bytes and reports the answer as the
// reference's. An oracle that can silently return another run's result cannot
// judge anything, so each one gets a directory of its own. The pid keeps the
// name readable when a run is interrupted and something is left behind; the
// random half is what actually makes it unique, since "go test ./..." runs
// package binaries concurrently and pids are reused.
func newWorkdirName() (string, error) {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("name a private workdir: %w", err)
	}
	return fmt.Sprintf("ac3go-e2e-%d-%s", os.Getpid(), hex.EncodeToString(b[:])), nil
}

// uniqueName returns a file name unused within this Oracle's workdir.
func (o *Oracle) uniqueName(prefix, ext string) string {
	return fmt.Sprintf("%s-%d%s", prefix, o.seq.Add(1), ext)
}

// remove deletes runner-side files. Cleanup at the end of the test removes the
// whole workdir anyway; this keeps a long corpus run from holding every
// intermediate stream at once. Local paths are the test's temporary directory,
// which Go cleans up itself.
func (o *Oracle) remove(paths ...string) {
	if o.container == "" {
		return
	}
	_, _ = o.exec(append([]string{"rm", "-f"}, paths...)...)
}

// Required reports whether the environment claims an oracle must be available.
// See EnvRequired.
func Required() bool {
	switch os.Getenv(EnvRequired) {
	case "", "0", "false":
		return false
	}
	return true
}

// unavailable reports that no oracle can be had. It skips, which is what lets
// this module's tests run on a machine that has no reference decoder - unless
// EnvRequired says one was promised, in which case its absence is the failure.
func unavailable(t testing.TB, format string, args ...any) {
	t.Helper()
	if Required() {
		t.Fatalf("%s is set but the oracle is unavailable: "+format,
			append([]any{EnvRequired}, args...)...)
	}
	t.Skipf(format, args...)
}

// Setup returns an Oracle, or skips the test when the environment does not
// describe one. It also skips when the environment describes a runner that
// does not answer, so that a stale container name is a skip and not a failure
// of this module.
//
// Set EnvRequired to turn every one of those skips into a failure, on a machine
// where the oracle is supposed to be there.
func Setup(t testing.TB) *Oracle {
	t.Helper()

	mode := os.Getenv(EnvMode)
	if mode == "" {
		unavailable(t, "%s is not set: no reference decoder to compare against", EnvMode)
	}
	tool := os.Getenv(EnvTool)
	if tool == "" {
		unavailable(t, "%s is set but %s is not: no reference decoder named", EnvMode, EnvTool)
	}

	name, err := newWorkdirName()
	if err != nil {
		t.Fatalf("%v", err)
	}

	o := &Oracle{
		tool:        tool,
		probe:       envOr(EnvProbe, tool),
		corpus:      os.Getenv(EnvCorpus),
		extractArgv: strings.Fields(envOr(EnvExtract, defaultExtractArgv)),
		decodeArgv:  strings.Fields(envOr(EnvDecode, defaultDecodeArgv)),
		probeArgv:   strings.Fields(envOr(EnvProbeCmd, defaultProbeArgv)),
		workdir:     path.Join("/tmp", name),
	}

	switch {
	case mode == "local":
		// The test's own temporary directory is already private to it, and Go
		// removes it.
		o.workdir = t.TempDir()
		if _, err := exec.LookPath(tool); err != nil {
			unavailable(t, "%s=local but %q is not on the PATH: %v", EnvMode, tool, err)
		}
	case strings.HasPrefix(mode, "docker:"):
		o.container = strings.TrimPrefix(mode, "docker:")
		if o.container == "" {
			t.Fatalf("%s=%q names no container", EnvMode, mode)
		}
		if err := o.ping(); err != nil {
			unavailable(t, "container %q is not usable: %v", o.container, err)
		}
	default:
		t.Fatalf("%s=%q: want \"local\" or \"docker:<container>\"", EnvMode, mode)
	}

	if o.container != "" {
		if _, err := o.exec(o.tool, "-version"); err != nil {
			unavailable(t, "%q does not run in container %q: %v", o.tool, o.container, err)
		}
		// Deliberately not "mkdir -p": the workdir must not already exist. If
		// it somehow did, this Oracle would be sharing it, which is the one
		// thing the private name is there to prevent, and failing is better
		// than judging a decoder on another run's files.
		if _, err := o.exec("mkdir", o.workdir); err != nil {
			unavailable(t, "cannot create %s in container %q: %v", o.workdir, o.container, err)
		}
		t.Cleanup(func() { _, _ = o.exec("rm", "-rf", o.workdir) })
	}
	return o
}

// ping reports whether the container is running and reachable.
func (o *Oracle) ping() error {
	if _, err := exec.LookPath("docker"); err != nil {
		return fmt.Errorf("docker is not on the PATH: %w", err)
	}
	out, err := run("docker", "inspect", "-f", "{{.State.Running}}", o.container)
	if err != nil {
		return err
	}
	if strings.TrimSpace(out) != "true" {
		return errors.New("container is not running")
	}
	return nil
}

// Corpus returns the configured directory of real media, as the runner sees
// it, and skips the test when there is none.
func (o *Oracle) Corpus(t testing.TB) string {
	t.Helper()
	if o.corpus == "" {
		unavailable(t, "%s is not set: no real media to test against", EnvCorpus)
	}
	return o.corpus
}

// Track is one audio track of a media file.
type Track struct {
	Index      int
	Codec      string
	Channels   int
	SampleRate int
}

// Tracks lists the audio tracks of a media file. src is a path on the runner's
// side, so under Corpus in docker mode.
func (o *Oracle) Tracks(t testing.TB, src string) []Track {
	t.Helper()
	out, err := o.exec(o.expand(o.probeArgv, map[string]string{"tool": o.probe, "src": src})...)
	if err != nil {
		t.Fatalf("probe %s: %v", src, err)
	}
	var tracks []Track
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		fields := strings.Split(strings.TrimSpace(line), ",")
		if len(fields) < 4 {
			continue
		}
		var tr Track
		if _, err := fmt.Sscanf(fields[0], "%d", &tr.Index); err != nil {
			continue
		}
		tr.Codec = fields[1]
		fmt.Sscanf(fields[2], "%d", &tr.SampleRate)
		fmt.Sscanf(fields[3], "%d", &tr.Channels)
		tracks = append(tracks, tr)
	}
	return tracks
}

// AC3Tracks is Tracks filtered to the tracks this module decodes.
func (o *Oracle) AC3Tracks(t testing.TB, src string) []Track {
	t.Helper()
	var out []Track
	for _, tr := range o.Tracks(t, src) {
		if tr.Codec == "ac3" {
			out = append(out, tr)
		}
	}
	return out
}

// EAC3Tracks is AC3Tracks for the enhanced codec. 7.1 lives here and nowhere
// else: the independent substream of E-AC-3 tops out at 5.1, so eight channels
// mean a dependent substream, which plain AC-3 has no notion of.
func (o *Oracle) EAC3Tracks(t testing.TB, src string) []Track {
	t.Helper()
	var out []Track
	for _, tr := range o.Tracks(t, src) {
		if tr.Codec == "eac3" {
			out = append(out, tr)
		}
	}
	return out
}

// Extract copies an elementary AC-3 stream out of a media file without
// re-encoding it, and returns its bytes. src is a path on the runner's side;
// track is the index Tracks reported.
//
// The extraction goes through the reference decoder's tooling on purpose: this
// module must not have to parse a container to be tested.
//
// A whole track of a feature film is hundreds of megabytes and hundreds of
// thousands of frames. Prefer ExtractSpan unless the test really needs all of
// it: a defect that a minute of audio does not show is not usually one that an
// hour of it does.
func (o *Oracle) Extract(t testing.TB, src string, track int) []byte {
	t.Helper()
	return o.extract(t, src, track, 0, 0, "ac3")
}

// ExtractSpan is Extract over dur seconds starting at start, which is what
// keeps a test against a feature film to seconds rather than minutes.
func (o *Oracle) ExtractSpan(t testing.TB, src string, track int, start, dur float64) []byte {
	t.Helper()
	if dur <= 0 {
		t.Fatalf("ExtractSpan: dur = %v, want a positive number of seconds", dur)
	}
	return o.extract(t, src, track, start, dur, "ac3")
}

// ExtractSpanEAC3 is ExtractSpan for an E-AC-3 track, extracted through the
// eac3 muxer - the ac3 muxer refuses the enhanced codec, and 7.1 lives only in
// E-AC-3. The bytes come out as a raw elementary stream either way, both
// substreams of a 7.1 access unit included, because -c copy preserves the
// packet.
func (o *Oracle) ExtractSpanEAC3(t testing.TB, src string, track int, start, dur float64) []byte {
	t.Helper()
	if dur <= 0 {
		t.Fatalf("ExtractSpanEAC3: dur = %v, want a positive number of seconds", dur)
	}
	return o.extract(t, src, track, start, dur, "eac3")
}

// extract runs the extraction; dur of zero means the whole track.
func (o *Oracle) extract(t testing.TB, src string, track int, start, dur float64, format string) []byte {
	t.Helper()
	remote := path.Join(o.workdir, o.uniqueName(fmt.Sprintf("extract-%d", track), ".ac3"))
	vars := map[string]string{
		"tool":    o.tool,
		"src":     src,
		"track":   fmt.Sprint(track),
		"ss":      trimFloat(start),
		"durflag": "",
		"dur":     "",
		"fmt":     format,
		"out":     remote,
	}
	if dur > 0 {
		vars["durflag"], vars["dur"] = "-t", trimFloat(dur)
	}
	args := o.expand(o.extractArgv, vars)
	if _, err := o.exec(args...); err != nil {
		t.Fatalf("extract track %d of %s: %v", track, src, err)
	}
	defer o.remove(remote)
	return o.fetch(t, remote)
}

// trimFloat renders a number of seconds without a trailing ".000000".
func trimFloat(v float64) string {
	return strconv.FormatFloat(v, 'f', -1, 64)
}

// DecodePCM decodes an elementary AC-3 stream with the reference decoder and
// returns interleaved samples, sign extended into int32. bits must be 16, 24
// or 32.
//
// Each depth is the reference's own packing, not a padded word: 24-bit samples
// come back on three bytes. 32 bits is the reference at full precision, which
// is what a 24-bit comparison is checked against.
//
// This is the number the decoder under test has to reproduce.
func (o *Oracle) DecodePCM(t testing.TB, stream []byte, bits int) []int32 {
	t.Helper()
	return o.decodePCM(t, stream, bits, "", "")
}

// DecodePCMTargetLevel decodes a stream with the reference asked to normalize
// every programme's dialogue to target dB below full scale.
//
// It is a separate entry point rather than an argument to DecodePCM because it
// is the one thing the reference does not do at its defaults: left alone it
// applies no dialogue normalization at all, whatever the stream states, so a
// comparison of normalized samples has to say so on both sides or it is
// comparing a decoder that normalized against one that did not.
func (o *Oracle) DecodePCMTargetLevel(t testing.TB, stream []byte, bits, target int) []int32 {
	t.Helper()
	return o.decodePCM(t, stream, bits, "-target_level", strconv.Itoa(target))
}

// DecodePCMDownmix decodes a stream with the reference asked to fold it into
// layout ("stereo", "mono") using the AC-3 decoder's own downmix.
//
// The flag matters and the obvious one is wrong: asking for two channels the
// usual way gets a generic downmix out of the resampler, computed after the
// decode from coefficients of its own, which ignores the levels the stream
// states and attenuates by nothing. This asks the AC-3 decoder to do the
// downmix clause 7.8 describes, which is the one under test.
func (o *Oracle) DecodePCMDownmix(t testing.TB, stream []byte, bits int, layout string) []int32 {
	t.Helper()
	return o.decodePCM(t, stream, bits, "-downmix", layout)
}

// decodePCM runs the reference over a stream, optionally with one flag and its
// value spliced in ahead of the input.
func (o *Oracle) decodePCM(t testing.TB, stream []byte, bits int, decflag, decval string) []int32 {
	t.Helper()
	if bits != 16 && bits != 24 && bits != 32 {
		t.Fatalf("DecodePCM: bits = %d, want 16, 24 or 32", bits)
	}
	in := o.stage(t, o.uniqueName("decode-in", ".ac3"), stream)
	out := path.Join(o.workdir, o.uniqueName("decode-out", ".pcm"))
	defer o.remove(in, out)

	args := o.expand(o.decodeArgv, map[string]string{
		"tool":    o.tool,
		"in":      in,
		"bits":    fmt.Sprint(bits),
		"out":     out,
		"decflag": decflag,
		"decval":  decval,
	})
	if _, err := o.exec(args...); err != nil {
		t.Fatalf("decode %d bytes of AC-3: %v", len(stream), err)
	}
	samples, err := unpackPCM(o.fetch(t, out), bits)
	if err != nil {
		t.Fatalf("decode %d bytes of AC-3: %v", len(stream), err)
	}
	return samples
}

// unpackPCM turns the reference's raw little-endian output into sign extended
// samples.
//
// The reference packs a sample into exactly bits/8 bytes: "s24le" is three
// bytes, not a 24-bit value padded into a 32-bit word. Reading it as four
// would be silent nonsense rather than an error, because a buffer of 3-byte
// samples is very often a whole number of 4-byte words too: the length guard
// only means something once the width is right.
func unpackPCM(raw []byte, bits int) ([]int32, error) {
	if bits != 16 && bits != 24 && bits != 32 {
		return nil, fmt.Errorf("unpack PCM: bits = %d, want 16, 24 or 32", bits)
	}
	width := bits / 8
	if len(raw)%width != 0 {
		return nil, fmt.Errorf("decoded %d bytes, not a whole number of %d-byte samples",
			len(raw), width)
	}
	samples := make([]int32, len(raw)/width)
	for i := range samples {
		samples[i] = decodeLE(raw[i*width:], width)
	}
	return samples, nil
}

// decodeLE reads one little-endian signed sample of the given width, sign
// extending it from the top of the packed value rather than from the top of
// the word.
func decodeLE(b []byte, width int) int32 {
	var v uint32
	for i := range width {
		v |= uint32(b[i]) << (8 * uint(i))
	}
	// Sign extend from the top of the sample.
	shift := uint(32 - 8*width)
	return int32(v<<shift) >> shift
}

// stage puts data where the runner can read it and returns the runner-side
// path.
func (o *Oracle) stage(t testing.TB, name string, data []byte) string {
	t.Helper()
	local := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(local, data, 0o644); err != nil {
		t.Fatalf("stage %s: %v", name, err)
	}
	if o.container == "" {
		return local
	}
	remote := path.Join(o.workdir, name)
	if _, err := run("docker", "cp", local, o.container+":"+remote); err != nil {
		t.Fatalf("copy %s into container: %v", name, err)
	}
	return remote
}

// fetch reads a file the runner produced.
func (o *Oracle) fetch(t testing.TB, remote string) []byte {
	t.Helper()
	if o.container == "" {
		data, err := os.ReadFile(remote)
		if err != nil {
			t.Fatalf("read %s: %v", remote, err)
		}
		return data
	}
	local := filepath.Join(t.TempDir(), path.Base(remote))
	if _, err := run("docker", "cp", o.container+":"+remote, local); err != nil {
		t.Fatalf("copy %s out of container: %v", remote, err)
	}
	data, err := os.ReadFile(local)
	if err != nil {
		t.Fatalf("read %s: %v", local, err)
	}
	return data
}

// exec runs argv on the runner's side.
func (o *Oracle) exec(argv ...string) (string, error) {
	if o.container != "" {
		argv = append([]string{"docker", "exec", o.container}, argv...)
	}
	return run(argv[0], argv[1:]...)
}

// expand substitutes {name} placeholders in a templated argv.
//
// A template is split into arguments once, when it is read; substitution never
// splits again. That is what lets a media path hold spaces, which the paths
// of real films invariably do. An argument that expands to nothing is dropped,
// which is how an optional flag is spelled.
func (o *Oracle) expand(argv []string, vars map[string]string) []string {
	out := make([]string, 0, len(argv))
	for _, a := range argv {
		for k, v := range vars {
			a = strings.ReplaceAll(a, "{"+k+"}", v)
		}
		if a == "" {
			continue
		}
		out = append(out, a)
	}
	return out
}

// run executes a command and returns its standard output, folding standard
// error into the error so a failure says why. A command that hangs is killed
// rather than left to stall the test binary until its own deadline.
func run(name string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), commandTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, name, args...)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		if ctx.Err() != nil {
			return "", fmt.Errorf("%s: timed out after %s", name, commandTimeout)
		}
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return "", fmt.Errorf("%s: %s", name, msg)
	}
	return string(out), nil
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
