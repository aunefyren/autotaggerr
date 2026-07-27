package modules

import (
	"testing"

	"github.com/aunefyren/autotaggerr/models"
)

func TestReleaseToAlbumType(t *testing.T) {
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
			if got := ReleaseToAlbumType(tt.release); got != tt.want {
				t.Errorf("ReleaseToAlbumType() = %q, want %q", got, tt.want)
			}
		})
	}
}
