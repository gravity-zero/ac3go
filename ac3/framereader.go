package ac3

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
)

// A FrameReader cuts syncframes out of an elementary AC-3 stream.
//
// It reads into one buffer and hands out slices of it, so walking a whole
// stream allocates nothing beyond the buffer it starts with. The slice Next
// returns stays valid only until the following call.
//
// A stream is not guaranteed to start on a frame boundary, and a damaged
// stream can lose one. The reader therefore resyncs: it scans for a syncword
// whose header parses and whose check words verify, counts the bytes it threw
// away, and carries on. Those checks are what make a syncword found in the
// middle of audio data cheap to reject.
//
// A frame the reader hands out is a frame every byte of which is covered by a
// check word that verified, unless SetCRCMode says otherwise.
type FrameReader struct {
	r   io.Reader
	buf []byte
	// buf[start:end] is the data read and not yet returned.
	start, end int
	eof        bool

	// hdr is the header of the frame Next last returned; cand is where a
	// candidate is parsed while it is still only a candidate. They are two
	// fields rather than one so that a header a caller can see is always a
	// header that belonged to a frame.
	hdr  Header
	cand Header

	// readErr is what the source said when it stopped, held until the frames
	// already buffered have been handed over.
	readErr error
	frame   []byte
	skipped int64
	frames  int64
	crc     CRCMode
	empty   int // consecutive reads that returned no bytes and no error
}

// A CRCMode says how much of a frame a FrameReader verifies before handing it
// over. The zero value is CRCFull.
type CRCMode int

const (
	// CRCFull verifies both check words of every frame, which together cover
	// every byte of it. It is the default, and the only mode under which a
	// frame the reader returns is whole: crc1 alone covers the first 5/8 of a
	// frame, so under CRCFirst a frame can pass with its last 3/8 spliced in
	// from somewhere else.
	CRCFull CRCMode = iota

	// CRCFirst verifies crc1 only. It is what a caller wants when it means to
	// decode a frame the tail of which may be damaged, and is willing to look
	// at partial audio rather than lose the frame: the spec places crc1 so
	// that the first 5/8 of a frame can be trusted on its own.
	//
	// It is an AC-3 notion. An enhanced AC-3 frame has no crc1 - it carries
	// one check word, over all of it - so there is no partial frame to settle
	// for and this verifies exactly what CRCFull does.
	CRCFirst

	// CRCNone verifies neither. Nothing then tells a real syncword from a byte
	// pair in the audio data that happens to look like one, so resync becomes
	// guesswork; it is for feeding a decoder a stream whose framing is already
	// known to be sound.
	CRCNone
)

// String returns the mode's name.
func (m CRCMode) String() string {
	switch m {
	case CRCFull:
		return "full"
	case CRCFirst:
		return "crc1"
	case CRCNone:
		return "none"
	}
	return "unknown"
}

// maxEmptyReads bounds how many (0, nil) reads in a row are tolerated before
// the stream is called finished.
const maxEmptyReads = 100

// NewFrameReader returns a FrameReader over r. Frames are validated against
// both of their check words as they are found; use SetCRCMode to narrow or
// drop the check.
func NewFrameReader(r io.Reader) *FrameReader {
	return &FrameReader{
		r:   r,
		buf: make([]byte, 0, 2*MaxAnyFrameSize),
		crc: CRCFull,
	}
}

// SetCRCMode selects how much of a frame Next verifies before returning it.
// The default is CRCFull, and narrowing it narrows what a frame Next returns
// is worth: see CRCMode.
func (fr *FrameReader) SetCRCMode(m CRCMode) { fr.crc = m }

// CRCMode returns the mode Next verifies frames under.
func (fr *FrameReader) CRCMode() CRCMode { return fr.crc }

// Skipped returns the number of bytes discarded so far while looking for a
// syncword: leading garbage, padding, or the tail of a damaged frame.
func (fr *FrameReader) Skipped() int64 { return fr.skipped }

// Frames returns the number of frames returned so far.
func (fr *FrameReader) Frames() int64 { return fr.frames }

// Header returns the parsed header of the frame Next returned last, and the
// zero Header before the first one. It is overwritten by every call to Next
// that returns a frame, and by no other.
//
// That last part is the whole of the guarantee and it is not free: finding a
// frame means parsing headers out of candidates that turn out not to be frames
// at all - a syncword that occurred by chance inside audio data, a frame whose
// check word disagrees - and the obvious way to write this leaves the last of
// those in place for a caller to read. A caller that asks after the stream ends
// is exactly the one that would get it, which is not a rare path: it is what
// summarising a stream looks like.
func (fr *FrameReader) Header() *Header { return &fr.hdr }

// Next returns the next syncframe, syncword included, as a slice into the
// reader's buffer that is valid until the next call. It returns io.EOF once
// the stream is exhausted; trailing bytes that hold no frame are counted by
// Skipped rather than reported as an error.
func (fr *FrameReader) Next() ([]byte, error) {
	for {
		if err := fr.fill(); err != nil {
			return nil, err
		}
		avail := fr.buf[fr.start:fr.end]
		if len(avail) < SyncInfoSize {
			fr.skipped += int64(len(avail))
			fr.start = fr.end
			return nil, io.EOF
		}

		n, err := fr.tryFrameAt(avail)
		switch {
		case err == nil:
			// The candidate is a frame, so its header becomes the reader's.
			// Until this line the header lives in the scratch copy, where a
			// caller cannot see it.
			fr.hdr = fr.cand
			fr.frame = avail[:n]
			fr.start += n
			fr.frames++
			return fr.frame, nil
		case errors.Is(err, ErrByteOrder) && fr.frames == 0 && fr.skipped == 0:
			// A stream whose very first bytes are a swapped syncword is a
			// swapped stream, and saying so is worth far more than resyncing
			// past it: every frame in it is swapped, so the reader would grind
			// through the whole thing, find nothing, and report an empty stream
			// - which reads as "there was no audio" rather than as "the bytes
			// are in the wrong order, call SwapBytes".
			//
			// Only at the head, and only before anything has been returned or
			// skipped. Further in, a swapped syncword is just two bytes of
			// audio data that happen to look like one, and there it has to be
			// stepped over like any other false sync.
			return nil, err
		case errors.Is(err, ErrShortFrame) && !fr.eof:
			// The frame is real but has not arrived in full yet. Compacting
			// makes room; the next fill will bring the rest.
			fr.compact()
			fr.readMore()
		default:
			// Not a frame here. Advance one byte and look again. Scanning for
			// the syncword's first byte keeps this from being a byte-at-a-time
			// crawl through audio data.
			//
			// Only one byte is given up, never the frame size the rejected
			// candidate announced: that size was read out of a header that has
			// just been shown to head no frame, and a stream that lost bytes
			// mid-frame has the next sound frame sitting inside the span it
			// claims. Trusting it would take a clean frame down with the
			// damaged one.
			skip := 1 + syncScan(avail[1:])
			fr.start += skip
			fr.skipped += int64(skip)
		}
	}
}

// tryFrameAt reports the size of the frame starting at b, or an error saying
// why b does not start one.
//
// The whole frame is in hand by the time the check words are weighed, so both
// are weighed: a frame that satisfies crc1 but not crc2 carries its first 5/8
// from one frame and its last 3/8 from wherever the stream resumed after a
// loss, and handing that to a decoder as a valid frame feeds it audio data
// that never belonged to the frame it is decoding.
func (fr *FrameReader) tryFrameAt(b []byte) (int, error) {
	if err := ParseHeader(b, &fr.cand); err != nil {
		return 0, err
	}
	size := fr.cand.Sync.FrameSize
	if len(b) < size {
		return 0, shortFrameError(len(b), size)
	}
	// The enhanced syntax has one check word rather than two: there is no crc1
	// to verify a frame's first five eighths against, only the word at the end
	// covering all of it. So CRCFirst, which is about settling for that first
	// part, has nothing to settle for and verifies the same word CRCFull does.
	// The alternative would be to verify nothing under CRCFirst, which is a
	// silent hole rather than a documented compromise.
	if isEAC3(fr.cand.Sync.Bsid) {
		if fr.crc != CRCNone {
			if err := checkEAC3CRC(b, size); err != nil {
				return 0, err
			}
		}
		return size, nil
	}

	switch fr.crc {
	case CRCFull:
		if err := checkCRC1(b, size); err != nil {
			return 0, err
		}
		if err := checkCRC2(b, size); err != nil {
			return 0, err
		}
	case CRCFirst:
		if err := checkCRC1(b, size); err != nil {
			return 0, err
		}
	}
	return size, nil
}

// syncScan returns the offset of the next byte that could open a syncword, or
// len(b) when there is none.
func syncScan(b []byte) int {
	for i, v := range b {
		if v == byte(Syncword>>8) {
			return i
		}
	}
	return len(b)
}

// fill makes sure the buffer holds either a whole frame's worth of bytes or
// everything left in the stream.
func (fr *FrameReader) fill() error {
	for fr.end-fr.start < MaxAnyFrameSize && !fr.eof {
		fr.compact()
		fr.readMore()
	}
	if fr.start == fr.end && fr.eof {
		return fr.end0()
	}
	return nil
}

// end0 is how the stream ends: with whatever went wrong, or with io.EOF when
// nothing did.
func (fr *FrameReader) end0() error {
	if fr.readErr != nil {
		return fr.readErr
	}
	return io.EOF
}

// compact moves the unread bytes to the front of the buffer.
func (fr *FrameReader) compact() {
	if fr.start == 0 {
		return
	}
	fr.end = copy(fr.buf[:cap(fr.buf)], fr.buf[fr.start:fr.end])
	fr.buf = fr.buf[:fr.end]
	fr.start = 0
}

// readMore appends one read's worth of bytes to the buffer, growing it only if
// the frame at hand cannot fit in what is left.
// readMore pulls what it can from the source. It never fails: a source that
// does is remembered in readErr and treated as the end of the stream, so that
// the frames already in hand are still handed over. See end0.
func (fr *FrameReader) readMore() {
	if fr.eof {
		return
	}
	if fr.end == cap(fr.buf) {
		fr.buf = append(fr.buf, 0)[:fr.end]
	}
	n, err := fr.r.Read(fr.buf[fr.end:cap(fr.buf)])
	fr.end += n
	fr.buf = fr.buf[:fr.end]
	switch {
	case errors.Is(err, io.EOF):
		fr.eof = true
	case err != nil:
		// Held rather than returned. The bytes already in the buffer are as
		// good as they ever were - the source failing does not unmake the
		// frames it already handed over - and a reader that reported the error
		// straight away would throw away up to a whole frame's worth of sound
		// that had arrived intact. The error surfaces once the buffer is dry,
		// in place of the io.EOF that would otherwise end the stream, so a
		// caller still learns the stream was cut short rather than finished.
		fr.readErr = fmt.Errorf("ac3: read stream: %w", err)
		fr.eof = true
	case n == 0:
		// A Reader is allowed to return (0, nil), but a Reader that only ever
		// does so must not hang us. Give it the same rope bufio does, then
		// treat the stream as finished.
		fr.empty++
		if fr.empty >= maxEmptyReads {
			fr.eof = true
		}
	default:
		fr.empty = 0
	}
}

// SwapBytes rewrites b in place from 16-bit little-endian words to the
// big-endian order the spec defines, so that a byte-swapped capture can be fed
// to a FrameReader. A trailing odd byte is left alone.
//
// ParseSyncInfo returns ErrByteOrder when it meets such a stream, which is the
// signal to call this.
func SwapBytes(b []byte) {
	for i := 0; i+1 < len(b); i += 2 {
		binary.BigEndian.PutUint16(b[i:], binary.LittleEndian.Uint16(b[i:]))
	}
}
