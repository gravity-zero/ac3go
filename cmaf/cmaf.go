// Package cmaf pulls an AC-3 or E-AC-3 elementary stream out of the
// fragmented-MP4 (CMAF) segments a browser or an HLS/DASH player delivers.
//
// It is deliberately narrow. ac3go decodes an elementary bitstream - a run of
// self-delimiting syncframes - but in a browser the audio arrives wrapped in
// ISO base media boxes: an initialization segment that declares the track, then
// media segments carrying the coded samples. This package is the thin adapter
// between the two, so that the wasm build can take what a player already has (a
// CMAF audio segment, or the bytes appended to an MSE SourceBuffer) and hand the
// decoder frames, without a second container library.
//
// What it is not: an MP4 muxer, a demuxer for video or muxed content, or an
// HLS/DASH client. It reads one audio track's samples and returns their bytes.
// Manifest parsing and segment fetching belong to the player; producing the
// segments belongs to the packager. This package's whole contract is: CMAF
// audio in, elementary AC-3/E-AC-3 out.
package cmaf

import (
	"errors"
	"fmt"
)

// Codec is what a track's sample entry declared: the four-character code the
// ISO base media format uses for AC-3 and E-AC-3.
type Codec string

const (
	CodecAC3  Codec = "ac-3"
	CodecEAC3 Codec = "ec-3"
)

// ErrNoAudioTrack means no AC-3 or E-AC-3 track was found where one was
// expected: an initialization segment with a video-only or empty moov, or a
// media segment whose fragments carry no audio.
var ErrNoAudioTrack = errors.New("cmaf: no AC-3 or E-AC-3 audio track")

// Demuxer extracts one audio track's elementary bitstream from the CMAF
// segments it is fed. Hand it an initialization segment once, then each media
// segment; or hand a whole fragmented file to Elementary. It keeps the track it
// found across calls and is not safe for concurrent use.
type Demuxer struct {
	haveTrack bool
	trackID   uint32
	codec     Codec
	// defaultSize is the track's default sample size from trex, used when a
	// fragment states neither per-sample sizes nor its own default.
	defaultSize uint32
}

// NewDemuxer returns a Demuxer with no track yet.
func NewDemuxer() *Demuxer { return &Demuxer{} }

// Codec reports the audio codec of the track found so far, or the empty string
// before Init or Segment has seen one.
func (d *Demuxer) Codec() Codec { return d.codec }

// Init parses an initialization segment (ftyp + moov) and remembers its AC-3 or
// E-AC-3 audio track. It is optional: a media segment that carries a single
// audio fragment can be read without it. Calling it lets Segment pick the audio
// track out of a fragment that names several.
func (d *Demuxer) Init(seg []byte) (Codec, error) {
	moov := topLevel(seg, "moov")
	if moov.typ != "moov" {
		return "", fmt.Errorf("cmaf: init segment has no moov")
	}
	if err := d.readMoov(moov); err != nil {
		return "", err
	}
	if !d.haveTrack {
		return "", ErrNoAudioTrack
	}
	return d.codec, nil
}

// Segment extracts the audio samples of one media segment (moof + mdat, one or
// more fragments) as elementary bytes: a whole number of syncframes, ready for
// ac3.NewFrameReader or to append to a running stream. A fragment for a track
// other than the one Init found is skipped.
func (d *Demuxer) Segment(seg []byte) ([]byte, error) {
	var out []byte
	err := eachBox(seg, func(b box) error {
		if b.typ != "moof" {
			return nil
		}
		frames, err := d.readFragment(seg, b)
		if err != nil {
			return err
		}
		out = append(out, frames...)
		return nil
	})
	return out, err
}

// Elementary reads a whole fragmented file - its own initialization boxes and
// every fragment - and returns the audio track's elementary bitstream. It is
// Init followed by Segment over one buffer, for when the segments are not split
// out.
func (d *Demuxer) Elementary(fmp4 []byte) ([]byte, error) {
	var out []byte
	err := eachBox(fmp4, func(b box) error {
		switch b.typ {
		case "moov":
			return d.readMoov(b)
		case "moof":
			frames, err := d.readFragment(fmp4, b)
			if err != nil {
				return err
			}
			out = append(out, frames...)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if !d.haveTrack && len(out) == 0 {
		return nil, ErrNoAudioTrack
	}
	return out, nil
}

// Elementary is the one-shot form: pull the whole audio bitstream out of a
// fragmented file with no Demuxer to hold.
func Elementary(fmp4 []byte) (Codec, []byte, error) {
	d := NewDemuxer()
	b, err := d.Elementary(fmp4)
	return d.codec, b, err
}

// ---------------------------------------------------------------------------
// moov: find the audio track and its defaults.
// ---------------------------------------------------------------------------

func (d *Demuxer) readMoov(moov box) error {
	// trex, in mvex, carries per-track default sample sizes a fragment may lean
	// on. Read them before the traks so the track's default is on hand.
	trexSize := map[uint32]uint32{}
	if mvex := child(moov, "mvex"); mvex != nil {
		_ = eachChild(*mvex, func(b box) error {
			if b.typ == "trex" && len(b.body) >= 24 {
				id := be32(b.body[4:])
				trexSize[id] = be32(b.body[20:]) // default_sample_size
			}
			return nil
		})
	}

	return eachChild(moov, func(trak box) error {
		if trak.typ != "trak" {
			return nil
		}
		mdia := child(trak, "mdia")
		if mdia == nil || handlerType(*mdia) != "soun" {
			return nil
		}
		stbl := descend(*mdia, "minf", "stbl")
		if stbl == nil {
			return nil
		}
		codec := sampleEntryCodec(*stbl)
		if codec == "" {
			return nil // an audio track this decoder does not carry
		}
		id := trackID(trak)
		d.haveTrack = true
		d.trackID = id
		d.codec = codec
		d.defaultSize = trexSize[id]
		return nil
	})
}

// sampleEntryCodec returns the codec of stbl's first sample entry if it is one
// this package handles, or the empty string otherwise.
func sampleEntryCodec(stbl box) Codec {
	stsd := child(stbl, "stsd")
	if stsd == nil || len(stsd.body) < 8 {
		return ""
	}
	// stsd is a FullBox (4) then entry_count (4), then the sample entries.
	entry, ok := firstBox(stsd.body[8:])
	if !ok {
		return ""
	}
	switch Codec(entry.typ) {
	case CodecAC3, CodecEAC3:
		return Codec(entry.typ)
	}
	return ""
}

// ---------------------------------------------------------------------------
// moof: read a fragment's samples.
// ---------------------------------------------------------------------------

func (d *Demuxer) readFragment(buf []byte, moof box) ([]byte, error) {
	var out []byte
	err := eachChild(moof, func(traf box) error {
		if traf.typ != "traf" {
			return nil
		}
		frames, err := d.readTraf(buf, moof, traf)
		if err != nil {
			return err
		}
		out = append(out, frames...)
		return nil
	})
	return out, err
}

func (d *Demuxer) readTraf(buf []byte, moof, traf box) ([]byte, error) {
	tfhd := child(traf, "tfhd")
	trun := child(traf, "trun")
	if tfhd == nil || trun == nil {
		return nil, fmt.Errorf("cmaf: fragment missing tfhd or trun")
	}

	th, err := parseTfhd(*tfhd)
	if err != nil {
		return nil, err
	}
	// A named track we know is not the audio one: skip it. With no Init, a lone
	// fragment is taken as the audio.
	if d.haveTrack && th.trackID != d.trackID {
		return nil, nil
	}

	tr, err := parseTrun(*trun)
	if err != nil {
		return nil, err
	}

	// Where the samples start. CMAF sets default-base-is-moof, so the trun data
	// offset is measured from the start of the moof box; base_data_offset, when
	// present, gives it outright.
	var base int64
	switch {
	case th.hasBaseOffset:
		base = int64(th.baseOffset)
	default: // default-base-is-moof, or the first-track fallback
		base = int64(moof.off)
	}
	start := base + int64(tr.dataOffset)

	// Sample sizes: the trun's own, else the fragment's default, else the
	// track's default from trex.
	def := th.defaultSize
	if def == 0 {
		def = d.defaultSize
	}
	total := int64(0)
	for i := 0; i < tr.sampleCount; i++ {
		if len(tr.sizes) > 0 {
			total += int64(tr.sizes[i])
		} else {
			total += int64(def)
		}
	}
	if def == 0 && len(tr.sizes) == 0 {
		return nil, fmt.Errorf("cmaf: fragment states no sample sizes")
	}
	if start < 0 || start+total > int64(len(buf)) {
		return nil, fmt.Errorf("cmaf: sample data [%d,%d) outside the %d-byte buffer", start, start+total, len(buf))
	}
	return buf[start : start+total], nil
}

type tfhd struct {
	trackID       uint32
	hasBaseOffset bool
	baseOffset    uint64
	defaultSize   uint32
}

func parseTfhd(b box) (tfhd, error) {
	if len(b.body) < 8 {
		return tfhd{}, fmt.Errorf("cmaf: short tfhd")
	}
	flags := be24(b.body[1:])
	p := b.body[4:]
	var t tfhd
	t.trackID = be32(p)
	p = p[4:]
	if flags&0x000001 != 0 { // base_data_offset
		if len(p) < 8 {
			return tfhd{}, fmt.Errorf("cmaf: short tfhd base offset")
		}
		t.hasBaseOffset = true
		t.baseOffset = be64(p)
		p = p[8:]
	}
	if flags&0x000002 != 0 { // sample_description_index
		p = p[4:]
	}
	if flags&0x000008 != 0 { // default_sample_duration
		p = p[4:]
	}
	if flags&0x000010 != 0 { // default_sample_size
		if len(p) < 4 {
			return tfhd{}, fmt.Errorf("cmaf: short tfhd default size")
		}
		t.defaultSize = be32(p)
	}
	return t, nil
}

type trun struct {
	sampleCount int
	dataOffset  int32
	sizes       []uint32
}

func parseTrun(b box) (trun, error) {
	if len(b.body) < 8 {
		return trun{}, fmt.Errorf("cmaf: short trun")
	}
	flags := be24(b.body[1:])
	count := int(be32(b.body[4:]))
	p := b.body[8:]
	var t trun
	t.sampleCount = count
	if flags&0x000001 != 0 { // data_offset
		if len(p) < 4 {
			return trun{}, fmt.Errorf("cmaf: short trun data offset")
		}
		t.dataOffset = int32(be32(p))
		p = p[4:]
	}
	if flags&0x000004 != 0 { // first_sample_flags
		p = p[4:]
	}
	if flags&0x000200 != 0 { // per-sample sizes present
		t.sizes = make([]uint32, 0, count)
	}
	per := 0
	if flags&0x000100 != 0 {
		per += 4
	}
	if flags&0x000200 != 0 {
		per += 4
	}
	if flags&0x000400 != 0 {
		per += 4
	}
	if flags&0x000800 != 0 {
		per += 4
	}
	for i := 0; i < count && per > 0; i++ {
		if len(p) < per {
			return trun{}, fmt.Errorf("cmaf: trun ends mid-sample")
		}
		q := p
		if flags&0x000100 != 0 { // duration
			q = q[4:]
		}
		if flags&0x000200 != 0 { // size
			t.sizes = append(t.sizes, be32(q))
			q = q[4:]
		}
		p = p[per:]
	}
	return t, nil
}
