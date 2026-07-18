package ac3

// CRC as specified in ETSI TS 102 366, clause 7.10: a 16-bit CRC over the
// polynomial x^16 + x^15 + x^2 + 1, most significant bit first, seeded with
// zero, with neither input nor output reflected.
//
// A frame carries two of them. crc1 covers the first 5/8 of the frame, so a
// decoder can reject a corrupt frame after receiving only part of it; crc2
// covers the rest. Each check word is placed so that running the CRC across
// its own region, check word included, yields zero.

const crcPoly = 0x8005

// crcTable is built once at init; the per-frame path only indexes it.
var crcTable = func() (t [256]uint16) {
	for i := range t {
		crc := uint16(i) << 8
		for range 8 {
			if crc&0x8000 != 0 {
				crc = crc<<1 ^ crcPoly
			} else {
				crc <<= 1
			}
		}
		t[i] = crc
	}
	return t
}()

// crc16 folds b into crc.
func crc16(crc uint16, b []byte) uint16 {
	for _, v := range b {
		crc = crc<<8 ^ crcTable[byte(crc>>8)^v]
	}
	return crc
}

// crcSplit returns the byte offset of the 5/8 point of a frame of frameSize
// bytes: the end of the region crc1 covers and the start of the region crc2
// covers.
func crcSplit(frameSize int) int {
	return (frameSize>>2 + frameSize>>4) << 1
}

// CheckCRC verifies both check words of one syncframe. frame must hold exactly
// the frame, syncword included; use SyncInfo.FrameSize to cut it out of a
// stream. It reports ErrCRC when either word fails and ErrShortFrame when the
// slice is too small to hold the frame it announces.
//
// A caller that only has the first 5/8 of a frame can call CheckCRC1 instead.
func CheckCRC(frame []byte) error {
	var si SyncInfo
	if err := ParseSyncInfo(frame, &si); err != nil {
		return err
	}
	// The enhanced syntax has no crc1: one check word, at the end, over the
	// whole frame.
	if isEAC3(si.Bsid) {
		return checkEAC3CRC(frame, si.FrameSize)
	}
	if err := checkCRC1(frame, si.FrameSize); err != nil {
		return err
	}
	return checkCRC2(frame, si.FrameSize)
}

// CheckCRC1 verifies only crc1, the check word covering the first 5/8 of the
// frame. It needs no more than that prefix, which lets a caller drop a corrupt
// frame early.
//
// Passing crc1 says nothing about the last 3/8 of the frame: a caller holding
// the whole frame should call CheckCRC instead.
func CheckCRC1(frame []byte) error {
	var si SyncInfo
	if err := ParseSyncInfo(frame, &si); err != nil {
		return err
	}
	if isEAC3(si.Bsid) {
		return errNoCRC1
	}
	return checkCRC1(frame, si.FrameSize)
}

// checkCRC1 and checkCRC2 are the check words of a frame whose syncinfo the
// caller has already parsed, which is what keeps the FrameReader from parsing
// it twice for every frame it hands out.

func checkCRC1(frame []byte, frameSize int) error {
	split := crcSplit(frameSize)
	if len(frame) < split {
		return shortFrameError(len(frame), split)
	}
	if crc16(0, frame[2:split]) != 0 {
		return crcError("crc1")
	}
	return nil
}

// checkEAC3CRC verifies the one check word an enhanced AC-3 frame carries.
//
// It is not crc2 under another name. AC-3 splits its frame in two and gives
// each part a word, so crc2 covers only what is past the split; an enhanced
// frame is not split, and its word covers everything from the end of the
// syncword to the end of the frame, itself included.
func checkEAC3CRC(frame []byte, frameSize int) error {
	if len(frame) < frameSize {
		return shortFrameError(len(frame), frameSize)
	}
	if crc16(0, frame[2:frameSize]) != 0 {
		return crcError("crc")
	}
	return nil
}

func checkCRC2(frame []byte, frameSize int) error {
	if len(frame) < frameSize {
		return shortFrameError(len(frame), frameSize)
	}
	if crc16(0, frame[crcSplit(frameSize):frameSize]) != 0 {
		return crcError("crc2")
	}
	return nil
}
