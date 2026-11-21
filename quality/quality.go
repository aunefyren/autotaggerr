// Package quality names how good a copy of a track is, and judges it against a
// profile. It is the decision engine for "is this good enough, and is that better?",
// kept free of the database and of files on purpose: today it judges files already in
// the library (Upgrade wanted), and a future indexer search must judge a release by
// its title with the same rules. Anything that measures (file headers) or parses
// (release names) produces a Measured or an ID and hands it here. See docs/quality.md.
package quality

import "strings"

// ID is a rung on the quality ladder. The strings are stored (library items, profile
// tiers), so they never change; a new codec is new rows, not renamed ones.
type ID string

const (
	Unknown  ID = "unknown"
	MP3Low   ID = "mp3-low" // below 128 kbit/s
	MP3128   ID = "mp3-128"
	MP3160   ID = "mp3-160"
	MP3192   ID = "mp3-192"
	MP3VBRV2 ID = "mp3-vbr-v2"
	MP3224   ID = "mp3-224"
	MP3256   ID = "mp3-256"
	MP3VBRV0 ID = "mp3-vbr-v0"
	MP3320   ID = "mp3-320"
	FLAC     ID = "flac"
	FLAC24   ID = "flac-24"
)

// Group is the broad family of a quality.
type Group string

const (
	GroupUnknown  Group = "unknown"
	GroupLossy    Group = "lossy"
	GroupLossless Group = "lossless"
)

// Definition describes one rung.
type Definition struct {
	ID    ID     `json:"id"`
	Name  string `json:"name"`
	Group Group  `json:"group"`
}

// ladder is every known quality, worst first — the order a new profile ranks them in.
// Names are Lidarr's spelling, so someone coming from it reads the same words, and a
// release title saying "MP3-320" or "FLAC 24bit" names the rung it parses to.
var ladder = []Definition{
	{Unknown, "Unknown", GroupUnknown},
	{MP3Low, "MP3 (under 128)", GroupLossy},
	{MP3128, "MP3-128", GroupLossy},
	{MP3160, "MP3-160", GroupLossy},
	{MP3192, "MP3-192", GroupLossy},
	{MP3VBRV2, "MP3-VBR-V2", GroupLossy},
	{MP3224, "MP3-224", GroupLossy},
	{MP3256, "MP3-256", GroupLossy},
	{MP3VBRV0, "MP3-VBR-V0", GroupLossy},
	{MP3320, "MP3-320", GroupLossy},
	{FLAC, "FLAC", GroupLossless},
	{FLAC24, "FLAC 24bit", GroupLossless},
}

// Ladder returns every known quality, worst first.
func Ladder() []Definition {
	out := make([]Definition, len(ladder))
	copy(out, ladder)
	return out
}

// Lookup returns a quality's definition, and whether it is known.
func Lookup(id ID) (Definition, bool) {
	for _, d := range ladder {
		if d.ID == id {
			return d, true
		}
	}
	return Definition{}, false
}

// Name is a quality's display name; an unknown ID reads as itself.
func Name(id ID) string {
	if d, ok := Lookup(id); ok {
		return d.Name
	}
	return string(id)
}

// Measured is what a file's headers say about its audio. BitrateKbps is the average
// for VBR; VBRQuality is the LAME preset (0 for V0 … 9 for V9) or -1 when the file
// does not say.
type Measured struct {
	Codec       string
	BitrateKbps int
	VBR         bool
	VBRQuality  int
	BitDepth    int
	SampleRate  int
}

// Classify places measured audio on the ladder. It never guesses upward: a VBR file
// that is not clearly V0 is never called V0, and anything it cannot place is Unknown.
func Classify(m Measured) ID {
	switch strings.ToUpper(m.Codec) {
	case "FLAC":
		if m.BitDepth > 16 {
			return FLAC24
		}
		if m.BitDepth > 0 {
			return FLAC
		}
		return Unknown
	case "MP3":
		if m.VBR {
			return classifyVBR(m)
		}
		return classifyCBR(m.BitrateKbps)
	}
	return Unknown
}

// classifyVBR reads the LAME preset when the file states it. V1 is placed at V2, the
// rung below it: Lidarr's ladder has no V1, and rounding down is the reading that never
// claims a cutoff the file does not meet. Without a preset the average bitrate decides,
// with bands that sit under what each preset typically averages (V0 ≈ 245, V2 ≈ 190).
func classifyVBR(m Measured) ID {
	if m.VBRQuality >= 0 && m.VBRQuality <= 9 {
		switch {
		case m.VBRQuality == 0:
			return MP3VBRV0
		case m.VBRQuality <= 2:
			return MP3VBRV2
		}
		return classifyCBR(m.BitrateKbps)
	}
	switch {
	case m.BitrateKbps >= 220:
		return MP3VBRV0
	case m.BitrateKbps >= 170:
		return MP3VBRV2
	}
	return classifyCBR(m.BitrateKbps)
}

// classifyCBR places a bitrate on the highest rung it reaches.
func classifyCBR(kbps int) ID {
	switch {
	case kbps <= 0:
		return Unknown
	case kbps >= 320:
		return MP3320
	case kbps >= 256:
		return MP3256
	case kbps >= 224:
		return MP3224
	case kbps >= 192:
		return MP3192
	case kbps >= 160:
		return MP3160
	case kbps >= 128:
		return MP3128
	}
	return MP3Low
}
