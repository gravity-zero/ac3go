package cmaf

import "encoding/binary"

// The ISO base media file format box walk, kept to exactly what this package
// needs. A box is a big-endian 32-bit size and a four-character type, then a
// body; a size of 1 means the real size is a 64-bit field after the type, and a
// size of 0 means the box runs to the end of its parent. Container boxes hold
// child boxes in place of a body; the readers above know which are which.

type box struct {
	typ  string
	off  int    // offset of the box's first byte within the buffer it came from
	body []byte // the bytes after the header (children, for a container)
}

// firstBox reads the box at the head of b.
func firstBox(b []byte) (box, bool) {
	if len(b) < 8 {
		return box{}, false
	}
	size := int(binary.BigEndian.Uint32(b))
	hdr := 8
	switch size {
	case 1:
		if len(b) < 16 {
			return box{}, false
		}
		size = int(binary.BigEndian.Uint64(b[8:]))
		hdr = 16
	case 0:
		size = len(b)
	}
	if size < hdr || size > len(b) {
		return box{}, false
	}
	return box{typ: string(b[4:8]), body: b[hdr:size]}, true
}

// eachBox calls fn for every top-level box in b, in order. Each box's off is
// its offset within b, which the fragment readers need to resolve sample data
// offsets measured from the start of a moof.
func eachBox(b []byte, fn func(box) error) error { return walk(b, fn) }

// walk steps box by box through b, advancing by each box's full size.
func walk(b []byte, fn func(box) error) error {
	off := 0
	for off+8 <= len(b) {
		size := int(binary.BigEndian.Uint32(b[off:]))
		hdr := 8
		switch size {
		case 1:
			if off+16 > len(b) {
				return nil
			}
			size = int(binary.BigEndian.Uint64(b[off+8:]))
			hdr = 16
		case 0:
			size = len(b) - off
		}
		if size < hdr || off+size > len(b) {
			return nil
		}
		if err := fn(box{typ: string(b[off+4 : off+8]), off: off, body: b[off+hdr : off+size]}); err != nil {
			return err
		}
		off += size
	}
	return nil
}

// topLevel returns the first top-level box of the given type, or nil.
func topLevel(b []byte, typ string) box {
	var found *box
	_ = walk(b, func(bx box) error {
		if found == nil && bx.typ == typ {
			c := bx
			found = &c
		}
		return nil
	})
	if found == nil {
		return box{}
	}
	return *found
}

// eachChild calls fn for every child box of a container.
func eachChild(parent box, fn func(box) error) error {
	return walk(parent.body, fn)
}

// child returns the first child of parent with the given type, or nil.
func child(parent box, typ string) *box {
	var found *box
	_ = walk(parent.body, func(bx box) error {
		if found == nil && bx.typ == typ {
			c := bx
			found = &c
		}
		return nil
	})
	return found
}

// descend follows a chain of single container children, e.g.
// descend(mdia, "minf", "stbl").
func descend(b box, path ...string) *box {
	cur := &b
	for _, typ := range path {
		cur = child(*cur, typ)
		if cur == nil {
			return nil
		}
	}
	return cur
}

// handlerType returns an mdia's handler four-character code (e.g. "soun"), read
// from its hdlr box.
func handlerType(mdia box) string {
	hdlr := child(mdia, "hdlr")
	if hdlr == nil || len(hdlr.body) < 12 {
		return ""
	}
	// FullBox (4) + pre_defined (4) + handler_type (4).
	return string(hdlr.body[8:12])
}

// trackID reads a trak's track_ID from its tkhd box (0 if absent).
func trackID(trak box) uint32 {
	tkhd := child(trak, "tkhd")
	if tkhd == nil || len(tkhd.body) < 4 {
		return 0
	}
	// FullBox: version then flags. track_ID sits after the creation and
	// modification times, whose width depends on the version.
	version := tkhd.body[0]
	off := 4
	if version == 1 {
		off += 16 // 64-bit creation + modification
	} else {
		off += 8 // 32-bit creation + modification
	}
	if len(tkhd.body) < off+4 {
		return 0
	}
	return be32(tkhd.body[off:])
}

func be24(b []byte) uint32 { return uint32(b[0])<<16 | uint32(b[1])<<8 | uint32(b[2]) }
func be32(b []byte) uint32 { return binary.BigEndian.Uint32(b) }
func be64(b []byte) uint64 { return binary.BigEndian.Uint64(b) }
