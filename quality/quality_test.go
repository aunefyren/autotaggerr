package quality

import (
	"errors"
	"testing"
)

func TestClassify(t *testing.T) {
	cases := []struct {
		name string
		m    Measured
		want ID
	}{
		{"flac 16", Measured{Codec: "FLAC", BitDepth: 16}, FLAC},
		{"flac 24", Measured{Codec: "flac", BitDepth: 24}, FLAC24},
		{"flac unread", Measured{Codec: "FLAC"}, Unknown},
		{"cbr 320", Measured{Codec: "MP3", BitrateKbps: 320}, MP3320},
		{"cbr 256", Measured{Codec: "MP3", BitrateKbps: 256}, MP3256},
		{"cbr 192", Measured{Codec: "MP3", BitrateKbps: 192}, MP3192},
		{"cbr 128", Measured{Codec: "MP3", BitrateKbps: 128}, MP3128},
		{"cbr 96", Measured{Codec: "MP3", BitrateKbps: 96}, MP3Low},
		{"cbr none", Measured{Codec: "MP3"}, Unknown},
		{"v0 by preset", Measured{Codec: "MP3", VBR: true, VBRQuality: 0, BitrateKbps: 200}, MP3VBRV0},
		{"v1 rounds down", Measured{Codec: "MP3", VBR: true, VBRQuality: 1, BitrateKbps: 225}, MP3VBRV2},
		{"v2 by preset", Measured{Codec: "MP3", VBR: true, VBRQuality: 2, BitrateKbps: 190}, MP3VBRV2},
		{"v5 by bitrate", Measured{Codec: "MP3", VBR: true, VBRQuality: 5, BitrateKbps: 130}, MP3128},
		{"vbr no preset high", Measured{Codec: "MP3", VBR: true, VBRQuality: -1, BitrateKbps: 245}, MP3VBRV0},
		{"vbr no preset mid", Measured{Codec: "MP3", VBR: true, VBRQuality: -1, BitrateKbps: 185}, MP3VBRV2},
		{"vbr no preset low", Measured{Codec: "MP3", VBR: true, VBRQuality: -1, BitrateKbps: 150}, MP3128},
		{"other codec", Measured{Codec: "OGG", BitrateKbps: 320}, Unknown},
	}
	for _, c := range cases {
		if got := Classify(c.m); got != c.want {
			t.Errorf("%s: Classify = %q, want %q", c.name, got, c.want)
		}
	}
}

func TestLadderNamesAreUniqueAndKnown(t *testing.T) {
	seen := map[ID]bool{}
	for _, d := range Ladder() {
		if seen[d.ID] {
			t.Fatalf("%q listed twice", d.ID)
		}
		seen[d.ID] = true
		if Name(d.ID) != d.Name {
			t.Fatalf("Name(%q) = %q", d.ID, Name(d.ID))
		}
	}
	if Name("nope") != "nope" {
		t.Fatal("an unknown ID should read as itself")
	}
}

func lossless() Profile {
	return Profile{
		Tiers: Tiers{
			{Qualities: []ID{MP3192}},
			{Qualities: []ID{MP3320}, Allowed: true},
			{Qualities: []ID{FLAC, FLAC24}, Allowed: true},
		},
		Cutoff:         2,
		UpgradeAllowed: true,
	}
}

func TestProfileJudgement(t *testing.T) {
	p := lossless()
	if p.Rank(FLAC) != p.Rank(FLAC24) {
		t.Fatal("qualities in one tier rank equally")
	}
	if p.Rank(MP3128) != -1 || p.Allowed(MP3128) || p.CutoffMet(MP3128) {
		t.Fatal("an unlisted quality is neither ranked, allowed nor good enough")
	}
	if p.Allowed(MP3192) {
		t.Fatal("a listed but disallowed tier is not allowed")
	}
	if !p.CutoffMet(FLAC) || !p.CutoffMet(FLAC24) || p.CutoffMet(MP3320) {
		t.Fatal("cutoff met at and above the cutoff tier only")
	}
	if !p.IsUpgrade(MP3320, FLAC) {
		t.Fatal("FLAC upgrades MP3-320 below a lossless cutoff")
	}
	if p.IsUpgrade(FLAC, FLAC24) {
		t.Fatal("nothing is an upgrade once the cutoff is met, and equal tiers never are")
	}
	if p.IsUpgrade(MP3320, MP3192) {
		t.Fatal("a disallowed or lower quality is not an upgrade")
	}
	if !p.IsUpgrade(MP3128, MP3320) {
		t.Fatal("an allowed quality upgrades an unlisted one")
	}
	p.UpgradeAllowed = false
	if p.IsUpgrade(MP3320, FLAC) {
		t.Fatal("with upgrades off nothing is an upgrade")
	}
	if got := lossless().CutoffQualities(); len(got) != 2 {
		t.Fatalf("CutoffQualities = %v", got)
	}
}

func TestAlbumCutoff(t *testing.T) {
	p := lossless()
	met, lowest, known := p.AlbumCutoff([]ID{FLAC, MP3320, FLAC24})
	if met || lowest != MP3320 || !known {
		t.Fatalf("one MP3 among FLACs: met=%v lowest=%q known=%v", met, lowest, known)
	}
	met, lowest, known = p.AlbumCutoff([]ID{FLAC, "", Unknown})
	if !met || lowest != FLAC || !known {
		t.Fatalf("unmeasured copies are left out: met=%v lowest=%q known=%v", met, lowest, known)
	}
	met, _, known = p.AlbumCutoff([]ID{Unknown, ""})
	if !met || known {
		t.Fatal("an album nobody measured is not flagged")
	}
	met, lowest, _ = p.AlbumCutoff([]ID{MP3128, MP3320})
	if met || lowest != MP3128 {
		t.Fatalf("an unlisted quality is the lowest: %q", lowest)
	}
}

func TestValidate(t *testing.T) {
	if err := lossless().Validate(); err != nil {
		t.Fatal(err)
	}
	bad := []Profile{
		{Tiers: Tiers{{Qualities: nil, Allowed: true}}},
		{Tiers: Tiers{{Qualities: []ID{"wav"}, Allowed: true}}},
		{Tiers: Tiers{{Qualities: []ID{FLAC}, Allowed: true}, {Qualities: []ID{FLAC}, Allowed: true}}},
		{Tiers: Tiers{{Qualities: []ID{FLAC}}}},
		{Tiers: Tiers{{Qualities: []ID{FLAC}, Allowed: true}}, Cutoff: 3},
		{Tiers: Tiers{{Qualities: []ID{MP3320}}, {Qualities: []ID{FLAC}, Allowed: true}}, Cutoff: 0},
	}
	for i, p := range bad {
		if err := p.Validate(); !errors.Is(err, ErrInvalidProfile) {
			t.Errorf("case %d: Validate = %v, want ErrInvalidProfile", i, err)
		}
	}
}

func TestPresetsAreValid(t *testing.T) {
	presets := Presets()
	if len(presets) != 3 {
		t.Fatalf("want 3 presets, got %d", len(presets))
	}
	for _, pr := range presets {
		if err := pr.Profile.Validate(); err != nil {
			t.Errorf("%s: %v", pr.Name, err)
		}
		// Every known quality is ranked by every preset, so nothing measured is unlisted.
		for _, d := range Ladder() {
			if pr.Profile.Rank(d.ID) < 0 {
				t.Errorf("%s does not rank %s", pr.Name, d.ID)
			}
		}
	}
	anyProfile := presets[2].Profile
	for _, d := range Ladder() {
		if d.ID != Unknown && !anyProfile.CutoffMet(d.ID) {
			t.Errorf("Any should be satisfied by %s", d.ID)
		}
	}
	hq := presets[1].Profile
	if hq.CutoffMet(MP3VBRV0) || !hq.CutoffMet(MP3320) || !hq.Allowed(MP3VBRV0) || hq.Allowed(MP3256) {
		t.Error("High quality lossy: V0 allowed below an MP3-320 cutoff, 256 not allowed")
	}
}

func TestTiersScanValue(t *testing.T) {
	in := lossless().Tiers
	v, err := in.Value()
	if err != nil {
		t.Fatal(err)
	}
	var out Tiers
	if err := out.Scan(v); err != nil {
		t.Fatal(err)
	}
	if len(out) != 3 || out[2].Qualities[1] != FLAC24 || !out[2].Allowed {
		t.Fatalf("round trip: %+v", out)
	}
	if err := out.Scan([]byte(`[]`)); err != nil || len(out) != 0 {
		t.Fatal("empty array")
	}
	if err := out.Scan(nil); err != nil || len(out) != 0 {
		t.Fatal("NULL reads as empty")
	}
	if err := out.Scan(42); err == nil {
		t.Fatal("an int is not tiers")
	}
	var nilTiers Tiers
	if v, _ := nilTiers.Value(); v != "[]" {
		t.Fatalf("nil stores as [], got %v", v)
	}
}
