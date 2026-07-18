package ac3

import (
	"errors"
	"fmt"
)

// Sentinel errors a caller can route on with errors.Is. Everything this
// package returns wraps one of them with the detail of what went wrong.
var (
	// ErrNoSync means the buffer does not start with a syncword.
	ErrNoSync = errors.New("no syncword")

	// ErrByteOrder means the buffer starts with a byte-swapped syncword. Some
	// tools write AC-3 as 16-bit little-endian words; swap the byte pairs and
	// parse again.
	ErrByteOrder = errors.New("byte-swapped stream")

	// ErrShortFrame means the buffer holds fewer bytes than the frame needs.
	ErrShortFrame = errors.New("buffer shorter than the frame")

	// ErrCRC means a frame failed one of its two check words.
	ErrCRC = errors.New("crc mismatch")

	// ErrReserved means a field holds a value the spec reserves, which makes
	// the frame undecodable.
	ErrReserved = errors.New("reserved value")

	// ErrUnsupportedBSID means the frame announces a bit stream version this
	// package does not decode.
	ErrUnsupportedBSID = errors.New("unsupported bsid")

	// ErrExponent means a decoded exponent left the range the format defines.
	// The differential chain that produces exponents has no check word of its
	// own, so this is usually the first sign that a frame was mis-parsed or
	// corrupted.
	ErrExponent = errors.New("exponent out of range")

	// ErrBitAlloc means the bit allocation side information does not describe
	// a decodable block.
	ErrBitAlloc = errors.New("invalid bit allocation")

	// ErrMantissa means a quantized mantissa holds a code its quantizer has no
	// level for, which no encoder can emit.
	ErrMantissa = errors.New("invalid mantissa code")

	// ErrAudBlk means an audio block does not decode: a field the format
	// requires is missing, or the six blocks do not fit the frame that carries
	// them.
	ErrAudBlk = errors.New("invalid audio block")
)

func shortFrameError(have, want int) error {
	return fmt.Errorf("ac3: have %d bytes, need %d: %w", have, want, ErrShortFrame)
}

func crcError(which string) error {
	return fmt.Errorf("ac3: %s: %w", which, ErrCRC)
}

func reservedError(field string, value uint32) error {
	return fmt.Errorf("ac3: %s = %d: %w", field, value, ErrReserved)
}

func unsupportedBSID(bsid uint8) error {
	return fmt.Errorf("ac3: bsid = %d, this decoder handles up to %d and %d to %d: %w",
		bsid, MaxBSID, MinEAC3BSID, MaxEAC3BSID, ErrUnsupportedBSID)
}

// frameTooShort reports a frame whose own stated size is smaller than the
// header it has to hold. It is not a truncated read - the bytes may all be
// there - it is a frame that cannot be what it says it is, which is what a
// false sync in an enhanced stream looks like.
// errNoCRC1 is what CheckCRC1 returns for an enhanced AC-3 frame. There is no
// crc1 to check there, and saying so is better than returning nil, which would
// read as "the first five eighths are sound" when nothing was verified.
var errNoCRC1 = fmt.Errorf("ac3: enhanced AC-3 carries no crc1, only the check word at the end: %w", ErrCRC)

// The parts of enhanced AC-3 this decoder does not reach yet or on purpose.
var (
	// ErrUnsupportedEAC3 is what an enhanced frame this decoder cannot finish
	// returns. A caller can route on it to fall back rather than to fail.
	ErrUnsupportedEAC3 = errors.New("unsupported enhanced AC-3 feature")

	// errEAC3EnhancedCoupling is not a gap to be filled on the same terms. The
	// reference does not decode enhanced coupling either, so a stream using it
	// could not be checked against anything, and no real stream measured so far
	// does - which follows, since the corpus plays through that reference.
	errEAC3EnhancedCoupling = fmt.Errorf("ac3: enhanced coupling is not decoded: %w", ErrUnsupportedEAC3)
)

func couplingNotAllowed(acmod uint8) error {
	return fmt.Errorf("ac3: acmod %d codes fewer than two channels and cannot couple: %w",
		acmod, ErrReserved)
}

func unsupportedSubstream(id uint8) error {
	return fmt.Errorf("ac3: substream %d is not the independent one this decoder reads: %w",
		id, ErrUnsupportedEAC3)
}

// unsupportedReducedRate reports the half rate syntax. The reference does not
// decode it either - the spec does not say how bit allocation works there - so
// there is nothing to check an implementation of it against.
func unsupportedReducedRate(hz int) error {
	return fmt.Errorf("ac3: the reduced sampling rate syntax (%d Hz) is not decoded: %w",
		hz, ErrUnsupportedEAC3)
}

func frameTooShort(stated, need int) error {
	return fmt.Errorf("ac3: frame states %d bytes, its header alone needs %d: %w",
		stated, need, ErrShortFrame)
}

func badExponent(exp int) error {
	return fmt.Errorf("ac3: exponent %d is outside 0..%d: %w", exp, maxExponent, ErrExponent)
}

func shortExponentBuffer(want, have int) error {
	return fmt.Errorf("ac3: exponent set needs %d entries, buffer holds %d: %w", want, have, ErrExponent)
}

func badDeltaSegment(band, length int) error {
	return fmt.Errorf("ac3: delta bit allocation segment runs from band %d for %d bands, past %d: %w",
		band, length, nBands, ErrBitAlloc)
}

func badBandwidth(start, end int) error {
	return fmt.Errorf("ac3: channel covers bins %d..%d: %w", start, end, ErrBitAlloc)
}

func badMantissaGroup(bap uint8, code int) error {
	return fmt.Errorf("ac3: bap %d group code %d overflows its quantizer: %w", bap, code, ErrMantissa)
}

func badMantissaCode(bap uint8, code int) error {
	return fmt.Errorf("ac3: bap %d mantissa code %d has no level: %w", bap, code, ErrMantissa)
}

func blockError(blk int, err error) error {
	return fmt.Errorf("ac3: audio block %d: %w", blk, err)
}

func channelError(ch int, err error) error {
	return fmt.Errorf("channel %d: %w", ch, err)
}

func couplingError(err error) error {
	return fmt.Errorf("coupling channel: %w", err)
}

func missingInBlockZero(field string) error {
	return fmt.Errorf("ac3: %s reuses a block that does not exist: %w", field, ErrAudBlk)
}

// badCouplingRange takes sub-band numbers rather than the codes they came from
// because an enhanced block that extends its spectrum has no cplendf to report:
// its coupling ends where the extension starts, at a sub-band the code cannot
// always express.
func badCouplingRange(cplbegf, lastSubbnd int) error {
	return fmt.Errorf("ac3: coupling begins at sub-band %d and ends at %d: %w",
		cplbegf, lastSubbnd, ErrAudBlk)
}

func badSpxRange(begin, end int) error {
	return fmt.Errorf("ac3: spectral extension begins at sub-band %d and ends at %d: %w",
		begin, end, ErrAudBlk)
}

func badSpxCopyStart(dst, src int) error {
	return fmt.Errorf("ac3: spectral extension copies from bin %d into bin %d, which is at or below it: %w",
		src, dst, ErrAudBlk)
}

func frameOverrun(end, avail int) error {
	return fmt.Errorf("ac3: the audio blocks end at bit %d, past the %d the frame has for them: %w",
		end, avail, ErrAudBlk)
}

func wrap(err error) error {
	return fmt.Errorf("ac3: %w", err)
}
