package modules

import (
	"errors"
	"strings"
	"testing"

	"github.com/aunefyren/autotaggerr/models"
)

// TestResolveCorrelationNoFallbackUnmatched checks that with tag fallback refused and
// no manager match (nil client), resolution reports ErrUnmatched rather than reading
// the file's own tags. The empty correlation short-circuits before any disk access, so
// the path need not exist.
func TestResolveCorrelationNoFallbackUnmatched(t *testing.T) {
	_, err := ResolveCorrelation("/does/not/exist/01 Track.flac", nil, "/does/not/exist", false)
	if !errors.Is(err, ErrUnmatched) {
		t.Fatalf("expected ErrUnmatched, got %v", err)
	}
}

// TestPlexReleaseType pins the Plex-shaped value exactly as it has always been
// written, so the default profile's files do not re-tag.
func TestPlexReleaseType(t *testing.T) {
	rg := func(primary string, secondary ...string) models.ReleaseGroup {
		return models.ReleaseGroup{PrimaryType: primary, SecondaryTypes: secondary}
	}

	tests := []struct {
		name    string
		release models.MusicBrainzReleaseResponse
		want    string
	}{
		{
			name:    "remix in title short-circuits",
			release: models.MusicBrainzReleaseResponse{Title: "Discovery (Remix)", ReleaseGroup: rg("Album")},
			want:    "album; remix",
		},
		{
			name:    "remix in disambiguation short-circuits",
			release: models.MusicBrainzReleaseResponse{Disambiguation: "remix edition", ReleaseGroup: rg("Album")},
			want:    "album; remix",
		},
		{
			name:    "plain album, no secondary types",
			release: models.MusicBrainzReleaseResponse{ReleaseGroup: rg("Album")},
			want:    "album",
		},
		{
			name:    "soundtrack secondary type",
			release: models.MusicBrainzReleaseResponse{ReleaseGroup: rg("Album", "Soundtrack")},
			want:    "album; soundtrack",
		},
		{
			name:    "compilation secondary type",
			release: models.MusicBrainzReleaseResponse{ReleaseGroup: rg("Album", "Compilation")},
			want:    "album; compilation",
		},
		{
			name:    "live secondary type",
			release: models.MusicBrainzReleaseResponse{ReleaseGroup: rg("Album", "Live")},
			want:    "album; live",
		},
		{
			name:    "EP primary, no secondary",
			release: models.MusicBrainzReleaseResponse{ReleaseGroup: rg("EP")},
			want:    "ep",
		},
		{
			name:    "single primary, no secondary",
			release: models.MusicBrainzReleaseResponse{ReleaseGroup: rg("Single")},
			want:    "single",
		},
		{
			name:    "unknown primary with no secondary returns lowercased primary",
			release: models.MusicBrainzReleaseResponse{ReleaseGroup: rg("Broadcast")},
			want:    "broadcast",
		},
		{
			name:    "unknown primary with unhandled secondary returns empty",
			release: models.MusicBrainzReleaseResponse{ReleaseGroup: rg("Other", "Mixtape/Street")},
			want:    "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := PlexReleaseType(tt.release, true); got != tt.want {
				t.Errorf("PlexReleaseType() = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestReleaseTypes: the default is the one Plex-shaped value; MusicBrainz's form is the
// primary and every secondary type as separate values; the remix-edition rule is on
// by default in either form, adds "remix" once, and can be turned off.
func TestReleaseTypes(t *testing.T) {
	liveEP := models.MusicBrainzReleaseResponse{ReleaseGroup: models.ReleaseGroup{PrimaryType: "EP", SecondaryTypes: []string{"Live", "DJ-mix"}}}
	remixed := models.MusicBrainzReleaseResponse{Title: "Album (Remixes)", ReleaseGroup: models.ReleaseGroup{PrimaryType: "Album"}}
	alreadyRemix := models.MusicBrainzReleaseResponse{Title: "Remixed", ReleaseGroup: models.ReleaseGroup{PrimaryType: "Album", SecondaryTypes: []string{"Remix"}}}
	mb := models.TaggerSettings{MusicBrainzReleaseType: true}

	cases := []struct {
		name     string
		release  models.MusicBrainzReleaseResponse
		settings models.TaggerSettings
		want     string
	}{
		{"plex: a live EP is a live album", liveEP, models.TaggerSettings{}, "album; live"},
		{"musicbrainz: every type, as values", liveEP, mb, "ep|live|dj-mix"},
		{"plex: a remix edition", remixed, models.TaggerSettings{}, "album; remix"},
		{"plex: remix rule off", remixed, models.TaggerSettings{IgnoreRemixEditions: true}, "album"},
		{"musicbrainz: a remix edition", remixed, mb, "album|remix"},
		{"musicbrainz: remix rule off", remixed, models.TaggerSettings{MusicBrainzReleaseType: true, IgnoreRemixEditions: true}, "album"},
		{"musicbrainz: remix once", alreadyRemix, mb, "album|remix"},
	}
	for _, tc := range cases {
		if got := strings.Join(ReleaseTypes(tc.release, tc.settings), "|"); got != tc.want {
			t.Errorf("%s = %q, want %q", tc.name, got, tc.want)
		}
	}
}
