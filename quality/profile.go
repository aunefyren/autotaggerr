package quality

import (
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
)

// Tier is one rank in a profile: qualities that count as equally good (Lidarr's
// "group"), and whether they are acceptable at all.
type Tier struct {
	Qualities []ID `json:"qualities"`
	Allowed   bool `json:"allowed"`
}

// Tiers is a profile's ranking, worst first. Stored as one JSON column, because the
// editor saves the whole list at once and nothing queries inside it.
type Tiers []Tier

// Value implements driver.Valuer.
func (t Tiers) Value() (driver.Value, error) {
	if t == nil {
		t = Tiers{}
	}
	b, err := json.Marshal(t)
	if err != nil {
		return nil, err
	}
	return string(b), nil
}

// Scan implements sql.Scanner. NULL or empty reads as no tiers.
func (t *Tiers) Scan(src any) error {
	var raw []byte
	switch v := src.(type) {
	case nil:
		*t = Tiers{}
		return nil
	case string:
		raw = []byte(v)
	case []byte:
		raw = v
	default:
		return fmt.Errorf("quality tiers: cannot scan %T", src)
	}
	if len(raw) == 0 {
		*t = Tiers{}
		return nil
	}
	return json.Unmarshal(raw, t)
}

// Profile is what a library (or an artist) wants its copies to be: which qualities
// are acceptable, which is good enough to stop upgrading at, and whether to upgrade.
type Profile struct {
	Tiers Tiers
	// Cutoff is the index into Tiers of the tier that is good enough. A copy at or
	// above it meets the cutoff; below it, it is worth upgrading.
	Cutoff         int
	UpgradeAllowed bool
}

// Rank is the tier a quality is in, or -1 when the profile does not list it.
func (p Profile) Rank(id ID) int {
	for i, tier := range p.Tiers {
		for _, q := range tier.Qualities {
			if q == id {
				return i
			}
		}
	}
	return -1
}

// Allowed reports whether a copy of this quality is acceptable at all.
func (p Profile) Allowed(id ID) bool {
	r := p.Rank(id)
	return r >= 0 && p.Tiers[r].Allowed
}

// CutoffMet reports whether a copy of this quality is good enough to stop at. A
// quality the profile does not list never meets it.
func (p Profile) CutoffMet(id ID) bool {
	r := p.Rank(id)
	return r >= 0 && r >= p.Cutoff
}

// IsUpgrade reports whether a candidate should replace what is held: upgrades are on,
// the candidate is acceptable and ranks higher, and what is held is below the cutoff.
// Once the cutoff is met nothing is an upgrade — that is what a cutoff is for.
func (p Profile) IsUpgrade(have, candidate ID) bool {
	if !p.UpgradeAllowed || !p.Allowed(candidate) || p.CutoffMet(have) {
		return false
	}
	return p.Rank(candidate) > p.Rank(have)
}

// AlbumCutoff judges an album's copies together: met only when every measured copy
// meets the cutoff. Unknown and unmeasured copies are left out rather than read as
// failing: "could not tell" is not "too low", and flagging an album on a file nobody
// measured would be a guess. known reports whether any copy was measured at all.
func (p Profile) AlbumCutoff(have []ID) (met bool, lowest ID, known bool) {
	lowestRank := 0
	met = true
	for _, id := range have {
		if id == "" || id == Unknown {
			continue
		}
		r := p.Rank(id)
		if !known || r < lowestRank {
			lowest, lowestRank = id, r
		}
		known = true
		if !p.CutoffMet(id) {
			met = false
		}
	}
	if !known {
		return true, "", false
	}
	return met, lowest, true
}

// CutoffQualities is the cutoff tier's qualities — what an upgrade would aim at.
func (p Profile) CutoffQualities() []ID {
	if p.Cutoff < 0 || p.Cutoff >= len(p.Tiers) {
		return nil
	}
	return p.Tiers[p.Cutoff].Qualities
}

// ErrInvalidProfile wraps every reason Validate refuses a profile.
var ErrInvalidProfile = errors.New("invalid quality profile")

// Validate refuses a profile that cannot be judged with: an unknown or repeated
// quality, an empty tier, no acceptable tier, or a cutoff that is out of range or not
// itself acceptable (a target you are not allowed to reach).
func (p Profile) Validate() error {
	seen := map[ID]bool{}
	anyAllowed := false
	for i, tier := range p.Tiers {
		if len(tier.Qualities) == 0 {
			return fmt.Errorf("%w: tier %d is empty", ErrInvalidProfile, i+1)
		}
		for _, q := range tier.Qualities {
			if _, ok := Lookup(q); !ok {
				return fmt.Errorf("%w: unknown quality %q", ErrInvalidProfile, q)
			}
			if seen[q] {
				return fmt.Errorf("%w: %s is listed twice", ErrInvalidProfile, Name(q))
			}
			seen[q] = true
		}
		anyAllowed = anyAllowed || tier.Allowed
	}
	if !anyAllowed {
		return fmt.Errorf("%w: no quality is allowed", ErrInvalidProfile)
	}
	if p.Cutoff < 0 || p.Cutoff >= len(p.Tiers) {
		return fmt.Errorf("%w: the cutoff is not one of the tiers", ErrInvalidProfile)
	}
	if !p.Tiers[p.Cutoff].Allowed {
		return fmt.Errorf("%w: the cutoff must be an allowed quality", ErrInvalidProfile)
	}
	return nil
}

// LadderTiers is every known quality in its own tier, worst first, none allowed — the
// starting point a profile is shaped from.
func LadderTiers() Tiers {
	out := make(Tiers, 0, len(ladder))
	for _, d := range ladder {
		out = append(out, Tier{Qualities: []ID{d.ID}})
	}
	return out
}

// Preset is a profile seeded on first start.
type Preset struct {
	Name    string
	Profile Profile
}

// Presets are the profiles a new install starts with: lossless, good lossy, and one
// that accepts anything and never asks for an upgrade.
func Presets() []Preset {
	lossless := Tiers{}
	for _, d := range ladder {
		if d.ID == FLAC || d.ID == FLAC24 {
			continue
		}
		lossless = append(lossless, Tier{Qualities: []ID{d.ID}})
	}
	lossless = append(lossless, Tier{Qualities: []ID{FLAC, FLAC24}, Allowed: true})

	hq := LadderTiers()
	for i := range hq {
		switch hq[i].Qualities[0] {
		case MP3VBRV0, MP3320, FLAC, FLAC24:
			hq[i].Allowed = true
		}
	}
	hqProfile := Profile{Tiers: hq, UpgradeAllowed: true}
	hqProfile.Cutoff = hqProfile.Rank(MP3320)

	anyTiers := LadderTiers()
	for i := range anyTiers {
		anyTiers[i].Allowed = anyTiers[i].Qualities[0] != Unknown
	}

	return []Preset{
		{"Lossless", Profile{Tiers: lossless, Cutoff: len(lossless) - 1, UpgradeAllowed: true}},
		{"High quality lossy", hqProfile},
		{"Any", Profile{Tiers: anyTiers, Cutoff: 1, UpgradeAllowed: false}},
	}
}
