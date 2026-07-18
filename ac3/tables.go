package ac3

// Tables from ETSI TS 102 366, clause 4 (bit stream syntax) and clause 5
// (bit stream semantics). Field names follow the spec.

// sampleRates maps fscod to the sampling rate in Hz. fscod 3 is reserved.
var sampleRates = [4]int{48000, 44100, 32000, 0}

// bitRates maps frmsizecod >> 1 to the nominal bit rate in kbit/s
// (clause 4.4.1.3, table 4.13, "nominal bit rate" column).
var bitRates = [19]int{
	32, 40, 48, 56, 64, 80, 96, 112, 128, 160,
	192, 224, 256, 320, 384, 448, 512, 576, 640,
}

// frameSizes maps frmsizecod to the frame size in 16-bit words, one column per
// fscod (clause 4.4.1.3, table 4.13). At 44.1 kHz a frame does not divide
// evenly into words, so the low bit of frmsizecod selects between two sizes
// and the encoder alternates to hold the average rate.
var frameSizes = [38][3]uint16{
	{64, 69, 96},
	{64, 70, 96},
	{80, 87, 120},
	{80, 88, 120},
	{96, 104, 144},
	{96, 105, 144},
	{112, 121, 168},
	{112, 122, 168},
	{128, 139, 192},
	{128, 140, 192},
	{160, 174, 240},
	{160, 175, 240},
	{192, 208, 288},
	{192, 209, 288},
	{224, 243, 336},
	{224, 244, 336},
	{256, 278, 384},
	{256, 279, 384},
	{320, 348, 480},
	{320, 349, 480},
	{384, 417, 576},
	{384, 418, 576},
	{448, 487, 672},
	{448, 488, 672},
	{512, 557, 768},
	{512, 558, 768},
	{640, 696, 960},
	{640, 697, 960},
	{768, 835, 1152},
	{768, 836, 1152},
	{896, 975, 1344},
	{896, 976, 1344},
	{1024, 1114, 1536},
	{1024, 1115, 1536},
	{1152, 1253, 1728},
	{1152, 1254, 1728},
	{1280, 1393, 1920},
	{1280, 1394, 1920},
}

// Audio coding modes (acmod, clause 4.4.2.3, table 4.15). The naming is the
// spec's "front/rear" notation: 3/2 means three front and two rear channels.
const (
	AcmodDualMono uint8 = iota // 1+1: two independent programs
	AcmodMono                  // 1/0
	AcmodStereo                // 2/0
	Acmod3F                    // 3/0
	Acmod2F1R                  // 2/1
	Acmod3F1R                  // 3/1
	Acmod2F2R                  // 2/2
	Acmod3F2R                  // 3/2
)

// acmodChannels maps acmod to the number of full bandwidth channels, that is
// every channel except the LFE.
var acmodChannels = [8]int{2, 1, 2, 3, 3, 4, 4, 5}

// acmodNames maps acmod to the spec's short name for the mode.
var acmodNames = [8]string{"1+1", "1/0", "2/0", "3/0", "2/1", "3/1", "2/2", "3/2"}

// centerMixLevels maps cmixlev to the attenuation applied to the centre
// channel when it is mixed into the left and right outputs (clause 4.4.2.4,
// table 4.16). Index 3 is reserved.
// gainLevels is the table the enhanced syntax's three bit mix level fields
// index directly (clause E.1.3.1.10 and on). AC-3's two bit fields do not index
// it: they index tables 4.16 and 4.17, which name three of these nine each.
//
// The values are the same family as everything else here, a factor of two per
// six decibels, and levelZero is a level rather than an absence: a frame can
// ask for a channel to be left out of the downmix entirely.
var gainLevels = [9]float32{
	levelPlus3dB,
	levelPlus1Point5dB,
	1,
	levelMinus1Point5dB,
	levelMinus3dB,
	levelMinus4Point5dB,
	levelMinus6dB,
	0, // levelZero: drop the channel
	levelMinus9dB,
}

// The indices into gainLevels that an enhanced frame defaults to when it states
// no mixing metadata. They are not what an AC-3 frame with no mix level does,
// which is to have no such channel at all.
const (
	gainLevelMinus4Point5dB = 5
	gainLevelMinus6dB       = 6
)

// clampSurroundGainLevel holds a stated surround level to the range the spec
// gives it. The field is three bits and the top half of the table is what it
// can name: a surround louder than 3 dB down is not something the format lets a
// frame ask for, and the reference clamps rather than rejects.
func clampSurroundGainLevel(v uint8) uint8 { return min(max(v, 3), 7) }

// The gains the mix level fields name, exactly rather than as the spec prints
// them. The spec's tables give three decimals - 0.707, 0.595 - and those are
// roundings of these rather than values in their own right: 0.595 is what
// 2^(-4.5/6) rounds to, while 10^(-4.5/20) would round to 0.596. So the printed
// table is built on a factor of two per six decibels, which is the convention
// the dialogue level uses too (see dialnorm.go), and these are that
// convention's values at full precision. The reference computes them the same
// way. The window of window.go is the same story: the spec prints a rounding
// and the value is the arithmetic behind it.
const (
	levelPlus3dB        = 1.4142135623730951 // 2^(1/2)
	levelPlus1Point5dB  = 1.1892071150027210 // 2^(1/4)
	levelMinus1Point5dB = 0.8408964152537145 // 2^(-1/4)
	levelMinus3dB       = 0.7071067811865476 // 2^(-1/2), one over the root of two
	levelMinus4Point5dB = 0.5946035575013605 // 2^(-3/4)
	levelMinus6dB       = 0.5                // 2^(-1)
	levelMinus9dB       = 0.3535533905932738 // 2^(-3/2)
)

var centerMixLevels = [4]float32{levelMinus3dB, levelMinus4Point5dB, levelMinus6dB, 0}

// surroundMixLevels maps surmixlev to the attenuation applied to the surround
// channels in a downmix (clause 4.4.2.5, table 4.17). Value 2 is "0", that is
// drop the surrounds entirely; index 3 is reserved.
var surroundMixLevels = [4]float32{levelMinus3dB, levelMinus6dB, 0, 0}

// Surround encode modes (dsurmod, clause 4.4.2.6, table 4.18).
const (
	DsurmodNotIndicated uint8 = iota
	DsurmodNo
	DsurmodYes
	DsurmodReserved
)

// Bit stream modes (bsmod, clause 4.4.2.2, table 4.14). The meaning of bsmod 7
// depends on acmod, which is why it carries two names.
var bsmodNames = [8]string{
	"main audio service: complete main (CM)",
	"main audio service: music and effects (ME)",
	"associated service: visually impaired (VI)",
	"associated service: hearing impaired (HI)",
	"associated service: dialogue (D)",
	"associated service: commentary (C)",
	"associated service: emergency (E)",
	"associated service: voice over (VO) or karaoke",
}

// roomTypes maps roomtyp to the mixing room type (clause 4.4.2.9, table 4.20).
var roomTypes = [4]string{"not indicated", "large room, X curve monitor", "small room, flat monitor", "reserved"}
