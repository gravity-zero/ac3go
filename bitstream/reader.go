// Package bitstream provides an MSB-first bit reader over a byte slice.
//
// It is the low-level layer shared by the bitstream parsers of this module:
// AC-3 packs its fields as a big-endian bit string with no byte alignment, so
// every parser needs the same primitive.
//
// The reader never allocates and never panics. A read that runs past the end
// of the buffer latches a sticky error: that read and every read after it
// return zero, and Err reports the failure. Parsers can therefore read a long
// run of fields and check Err once at the end instead of after every field.
package bitstream

import "errors"

// ErrOverrun is returned by Err after a read ran past the end of the buffer.
var ErrOverrun = errors.New("bitstream: read past end of buffer")

// ErrWidth is returned by Err after a read requested more than 32 bits.
var ErrWidth = errors.New("bitstream: read width out of range")

// Reader reads bits MSB-first from a byte slice. The zero value is an empty
// reader; use Reset to point it at a buffer. A Reader holds a reference to the
// buffer it was given and never copies it, so it is safe to reuse across
// frames with Reset to keep the per-frame allocation count at zero.
//
// Two invariants hold everywhere below, and every bound in this file is
// written to lean on them rather than on arithmetic that could wrap:
//
//	0 <= pos <= len(buf)*8
//	len(buf) <= maxBufLen, so len(buf)*8 does not overflow an int
type Reader struct {
	buf []byte
	pos int // next bit to read, counted from bit 7 of buf[0]
	err error
}

// maxBufLen is the longest buffer whose length in bits still fits in an int.
// Reset refuses anything longer, which is what lets the bounds below multiply
// len(buf) by 8 without checking for overflow first. On a 64-bit platform no
// slice can reach the limit; on a 32-bit one it stands at 256 MiB, far above
// anything a bit-level parser is handed.
const maxBufLen = int(^uint(0)>>1) / 8

// NewReader returns a Reader positioned at the first bit of buf.
func NewReader(buf []byte) *Reader {
	var r Reader
	r.Reset(buf)
	return &r
}

// Reset points r at buf, rewinds it to the first bit and clears any latched
// error. It does not allocate.
//
// A buffer longer than maxBufLen cannot be addressed one bit at a time; Reset
// takes no such buffer and leaves r empty with ErrOverrun latched instead.
func (r *Reader) Reset(buf []byte) {
	r.pos = 0
	if len(buf) > maxBufLen {
		r.buf = nil
		r.err = ErrOverrun
		return
	}
	r.buf = buf
	r.err = nil
}

// Err reports the first error the reader latched, or nil.
func (r *Reader) Err() error { return r.err }

// BitPos returns the number of bits consumed so far.
func (r *Reader) BitPos() int { return r.pos }

// BitsRemaining returns the number of bits left in the buffer. It is zero once
// the reader has overrun.
func (r *Reader) BitsRemaining() int { return len(r.buf)*8 - r.pos }

// SeekBit moves the read position to the given absolute bit offset and clears
// any latched error. An offset outside the buffer latches ErrOverrun instead.
func (r *Reader) SeekBit(bit int) {
	if bit < 0 || bit > len(r.buf)*8 {
		r.fail(ErrOverrun)
		return
	}
	r.pos = bit
	r.err = nil
}

// Uint32 reads the next n bits, MSB-first, and returns them right-aligned.
// n must be in [0, 32]; a wider request latches ErrWidth. Reading zero bits
// returns zero and consumes nothing.
func (r *Reader) Uint32(n uint) uint32 {
	if n == 0 {
		return 0
	}
	if n > 32 {
		r.fail(ErrWidth)
		return 0
	}
	if r.err != nil {
		return 0
	}
	// Same subtraction as Skip. n is capped at 32 just above, so the sum could
	// only wrap for a buffer within 32 bits of maxBufLen; keeping both bounds
	// in the one form that cannot wrap costs nothing and leaves nothing to
	// re-derive.
	if int(n) > len(r.buf)*8-r.pos {
		r.fail(ErrOverrun)
		return 0
	}

	p := r.pos
	r.pos = p + int(n)

	// Fast path: the whole field lives inside one byte.
	off := uint(p & 7)
	if off+n <= 8 {
		return uint32(r.buf[p>>3]>>(8-off-n)) & (1<<n - 1)
	}

	var v uint32
	for n > 0 {
		off = uint(p & 7)
		avail := 8 - off
		take := avail
		if take > n {
			take = n
		}
		b := uint32(r.buf[p>>3] >> (avail - take))
		v = v<<take | b&(1<<take-1)
		p += int(take)
		n -= take
	}
	return v
}

// Bool reads a single bit and reports whether it is set.
func (r *Reader) Bool() bool {
	if r.err != nil {
		return false
	}
	if r.pos >= len(r.buf)*8 {
		r.fail(ErrOverrun)
		return false
	}
	b := r.buf[r.pos>>3] >> (7 - uint(r.pos&7)) & 1
	r.pos++
	return b != 0
}

// Skip advances the read position by n bits. A negative n, or an n that would
// carry the position past the end of the buffer, latches ErrOverrun and moves
// nothing.
//
// The bound is a subtraction rather than the more obvious pos+n > len(buf)*8
// on purpose. n is the caller's, and on a bit stream it is typically a length
// field read out of the stream itself, so it can be any int at all: the sum
// wraps negative for a large n, which would let the skip through and park the
// reader at a position no later read could survive. len(buf)*8-r.pos cannot
// wrap, by the invariants on Reader.
func (r *Reader) Skip(n int) {
	if r.err != nil {
		return
	}
	if n < 0 || n > len(r.buf)*8-r.pos {
		r.fail(ErrOverrun)
		return
	}
	r.pos += n
}

// Align advances the read position to the next byte boundary. It is a no-op
// when the reader is already aligned.
func (r *Reader) Align() {
	if r.err != nil {
		return
	}
	if n := r.pos & 7; n != 0 {
		r.Skip(8 - n)
	}
}

// fail latches err and parks the reader at the end of the buffer so that no
// further read can observe stale data.
func (r *Reader) fail(err error) {
	if r.err == nil {
		r.err = err
	}
	r.pos = len(r.buf) * 8
}
