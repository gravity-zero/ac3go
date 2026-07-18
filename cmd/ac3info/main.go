// Command ac3info lists the syncframes of an elementary AC-3 stream.
//
// It reads a file or standard input, walks every syncframe, and prints what
// the headers say. It is a debugging tool for this module: when a stream will
// not decode, this is what says whether the framing, the check words or the
// bit stream information is at fault.
//
//	ac3info stream.ac3            # one line per frame, then a summary
//	ac3info -summary stream.ac3   # the summary only
//	ac3info -n 4 -v stream.ac3    # the first four frames, every field
//	cat stream.ac3 | ac3info -    # read standard input
package main

import (
	"bufio"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/gravity-zero/ac3go/ac3"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "ac3info:", err)
		os.Exit(1)
	}
}

func run() error {
	var (
		limit   = flag.Int("n", 0, "stop after n frames (0 means all)")
		summary = flag.Bool("summary", false, "print the summary only")
		verbose = flag.Bool("v", false, "print every bit stream information field of each frame")
		nocrc   = flag.Bool("nocrc", false, "do not verify the check words")
	)
	flag.Usage = func() {
		fmt.Fprintln(os.Stderr, "usage: ac3info [flags] <stream.ac3 | ->")
		flag.PrintDefaults()
	}
	flag.Parse()
	if flag.NArg() != 1 {
		flag.Usage()
		return errors.New("expected exactly one input")
	}

	in, close, err := open(flag.Arg(0))
	if err != nil {
		return err
	}
	defer close()

	out := bufio.NewWriter(os.Stdout)
	defer out.Flush()
	return list(in, out, *limit, *summary, *verbose, !*nocrc)
}

func open(name string) (io.Reader, func(), error) {
	if name == "-" {
		return bufio.NewReaderSize(os.Stdin, 1<<16), func() {}, nil
	}
	f, err := os.Open(name)
	if err != nil {
		return nil, nil, err
	}
	return bufio.NewReaderSize(f, 1<<16), func() { f.Close() }, nil
}

// stats accumulates what the summary reports.
type stats struct {
	frames   int64
	bytes    int64
	badCRC   int64
	samples  int64
	shapes   map[string]int64 // distinct header shapes, in first-seen order
	order    []string
	dialnorm map[int]int64
}

func list(in io.Reader, out *bufio.Writer, limit int, summaryOnly, verbose, checkCRC bool) error {
	fr := ac3.NewFrameReader(in)
	// The frame reader's own crc1 check is what makes resync trustworthy, so
	// it stays on; -nocrc only drops the full check and the reporting.
	st := stats{shapes: map[string]int64{}, dialnorm: map[int]int64{}}
	var offset int64

	for limit == 0 || st.frames < int64(limit) {
		frame, err := fr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return fmt.Errorf("frame %d: %w", st.frames, err)
		}
		h := fr.Header()

		crcNote := ""
		if checkCRC {
			if err := ac3.CheckCRC(frame); err != nil {
				st.badCRC++
				crcNote = " CRC-BAD"
			}
		}

		offset = fr.Skipped() + st.bytes
		st.frames++
		st.bytes += int64(len(frame))
		// Not SamplesPerFrame: an AC-3 frame is always six blocks, an enhanced
		// one is one, two, three or six. Counting every frame as six would
		// overstate the duration of a short-frame stream by up to six times,
		// and understate the bit rate by as much - which reads as a plausible
		// number rather than as a mistake.
		st.samples += int64(h.Sync.NumBlocks * ac3.SamplesPerBlock)
		shape := describe(h)
		if _, seen := st.shapes[shape]; !seen {
			st.order = append(st.order, shape)
		}
		st.shapes[shape]++
		st.dialnorm[h.DialnormDB()]++

		if summaryOnly {
			continue
		}
		fmt.Fprintf(out, "frame %-6d @%-10d %5d B  %s%s\n", st.frames-1, offset, len(frame), shape, crcNote)
		if verbose {
			writeFields(out, h)
		}
	}

	writeSummary(out, &st, fr, checkCRC)
	return nil
}

// describe is the one-line shape of a frame: what a reader wants to see repeat
// identically down the whole stream.
func describe(h *ac3.Header) string {
	lfe := ""
	if h.Lfeon {
		lfe = "+LFE"
	}
	return fmt.Sprintf("%d Hz %d kbit/s bsid=%d acmod=%d(%s)%s %s dialnorm=%d dB",
		h.Sync.SampleRate, h.Sync.BitRate/1000, h.Sync.Bsid, h.Acmod, h.AcmodName(), lfe,
		h.Layout(), h.DialnormDB())
}

func writeFields(out *bufio.Writer, h *ac3.Header) {
	p := func(format string, args ...any) { fmt.Fprintf(out, "    "+format+"\n", args...) }

	p("syncinfo   fscod=%d (%d Hz)  frmsizecod=%d (%d B, %d kbit/s)  crc1=%#04x",
		h.Sync.Fscod, h.Sync.SampleRate, h.Sync.Frmsizecod, h.Sync.FrameSize, h.Sync.BitRate/1000, h.Sync.CRC1)
	p("bsi        bsid=%d  bsmod=%d (%s)  acmod=%d (%s)  lfeon=%v",
		h.Sync.Bsid, h.Bsmod, h.BsmodName(), h.Acmod, h.AcmodName(), h.Lfeon)
	if h.HasCmixlev {
		p("cmixlev    %d (x%.3f)", h.Cmixlev, h.CenterMixLevel())
	}
	if h.HasSurmixlev {
		p("surmixlev  %d (x%.3f)", h.Surmixlev, h.SurroundMixLevel())
	}
	if h.HasDsurmod {
		p("dsurmod    %d", h.Dsurmod)
	}
	p("dialnorm   %d (%d dB)", h.Dialnorm, h.DialnormDB())
	if h.Compre {
		p("compr      %#02x", h.Compr)
	}
	if h.Langcode {
		p("langcod    %#02x", h.Langcod)
	}
	if h.Audprodie {
		p("audprodi   mixlevel=%d (%d dB SPL)  roomtyp=%d (%s)", h.Mixlevel, 80+int(h.Mixlevel), h.Roomtyp, h.RoomType())
	}
	if h.Acmod == 0 {
		p("program 2  dialnorm2=%d (%d dB)  compr2e=%v  langcod2e=%v  audprodi2e=%v",
			h.Dialnorm2, h.Dialnorm2DB(), h.Compr2e, h.Langcod2e, h.Audprodi2e)
	}
	p("flags      copyrightb=%v  origbs=%v", h.Copyrightb, h.Origbs)
	if h.Timecod1e || h.Timecod2e {
		p("timecode   timecod1e=%v timecod1=%d  timecod2e=%v timecod2=%d", h.Timecod1e, h.Timecod1, h.Timecod2e, h.Timecod2)
	}
	if h.Xbsi1e {
		p("xbsi1      dmixmod=%d  ltrtcmixlev=%d  ltrtsurmixlev=%d  lorocmixlev=%d  lorosurmixlev=%d",
			h.Dmixmod, h.Ltrtcmixlev, h.Ltrtsurmixlev, h.Lorocmixlev, h.Lorosurmixlev)
	}
	if h.Xbsi2e {
		p("xbsi2      dsurexmod=%d  dheadphonmod=%d  adconvtyp=%v", h.Dsurexmod, h.Dheadphonmod, h.Adconvtyp)
	}
	if add := h.AddBSI(); add != nil {
		p("addbsi     %d B  %x", len(add), add)
	}
	p("audio      starts at bit %d (byte %d.%d)", h.AudioStartBit, h.AudioStartBit/8, h.AudioStartBit%8)
}

func writeSummary(out *bufio.Writer, st *stats, fr *ac3.FrameReader, checkCRC bool) {
	fmt.Fprintln(out, "--")
	fmt.Fprintf(out, "frames     %d\n", st.frames)
	fmt.Fprintf(out, "bytes      %d in frames, %d skipped\n", st.bytes, fr.Skipped())
	if st.frames == 0 {
		return
	}
	h := fr.Header()
	rate := h.Sync.SampleRate
	if rate > 0 {
		secs := float64(st.samples) / float64(rate)
		fmt.Fprintf(out, "duration   %.3f s (%d samples at %d Hz)\n", secs, st.samples, rate)
		fmt.Fprintf(out, "bitrate    %.1f kbit/s measured\n", float64(st.bytes)*8/secs/1000)
	}
	if checkCRC {
		fmt.Fprintf(out, "crc        %d bad of %d\n", st.badCRC, st.frames)
	}
	fmt.Fprintf(out, "shapes     %d distinct\n", len(st.order))
	for _, s := range st.order {
		fmt.Fprintf(out, "  %6d x  %s\n", st.shapes[s], s)
	}
	if len(st.dialnorm) > 1 {
		fmt.Fprintf(out, "dialnorm   %d distinct values across the stream\n", len(st.dialnorm))
	}
}
