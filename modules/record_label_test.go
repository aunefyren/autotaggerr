package modules

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/aunefyren/autotaggerr/models"
)

// TestSingleRecordLabel: every label by default, in MusicBrainz's order; only the first
// with the setting on — skipping an entry that carries a catalogue number and no label
// — and the catalogue numbers in full either way.
func TestSingleRecordLabel(t *testing.T) {
	var release models.MusicBrainzReleaseResponse
	if err := json.Unmarshal([]byte(`{"id":"rel","title":"Oxnard","artist-credit":[{"name":"Anderson .Paak","artist":{"id":"a1","name":"Anderson .Paak"}}],"label-info":[
		{"catalog-number":"B0029","label":null},
		{"catalog-number":"B0029-01","label":{"name":"Aftermath Entertainment"}},
		{"catalog-number":"","label":{"name":"12Tone Music"}},
		{"catalog-number":"","label":{"name":"OBE"}}],
		"media":[{"position":1,"tracks":[{"id":"t1","title":"The Chase","position":1}]}]}`), &release); err != nil {
		t.Fatal(err)
	}
	build := func(settings models.TaggerSettings) models.FileTags {
		t.Helper()
		tags, err := BuildFileTags(release.Media[0].Tracks[0], release.Media[0], release, settings)
		if err != nil {
			t.Fatal(err)
		}
		return tags
	}

	all := build(models.TaggerSettings{})
	if got := strings.Join(all.RecordLabels, "|"); got != "Aftermath Entertainment|12Tone Music|OBE" {
		t.Errorf("labels = %q, want all three in MusicBrainz's order", got)
	}
	one := build(models.TaggerSettings{SingleRecordLabel: true})
	if got := strings.Join(one.RecordLabels, "|"); got != "Aftermath Entertainment" {
		t.Errorf("single label = %q, want the first named one", got)
	}
	if got := strings.Join(one.CatalogNumbers, "|"); got != "B0029|B0029-01" {
		t.Errorf("catalogue numbers = %q, want both kept", got)
	}

	// One label is one label either way.
	release.LabelInfo = release.LabelInfo[3:]
	if got := build(models.TaggerSettings{SingleRecordLabel: true}).RecordLabels; len(got) != 1 || got[0] != "OBE" {
		t.Errorf("a single label = %v", got)
	}
}

// TestSingleRecordLabelIsNotInTheFingerprintWhileOff: settings added after profiles
// existed are invisible in what Tagger.Fingerprint hashes while they mean what files
// were already tagged with, so adding them did not change every existing profile's
// fingerprint and re-process the whole library. Turned on, they change it.
func TestSingleRecordLabelIsNotInTheFingerprintWhileOff(t *testing.T) {
	off, _ := json.Marshal(models.TaggerSettings{})
	for _, field := range []string{"SingleRecordLabel", "MusicBrainzReleaseType", "IgnoreRemixEditions", "OriginalDateAsDate"} {
		if strings.Contains(string(off), field) {
			t.Errorf("settings JSON at its zero value = %s, want %s absent", off, field)
		}
	}
	on, _ := json.Marshal(models.TaggerSettings{SingleRecordLabel: true, MusicBrainzReleaseType: true, IgnoreRemixEditions: true, OriginalDateAsDate: true})
	for _, field := range []string{"SingleRecordLabel", "MusicBrainzReleaseType", "IgnoreRemixEditions", "OriginalDateAsDate"} {
		if !strings.Contains(string(on), field) {
			t.Errorf("settings JSON with %s on = %s, want it present so files re-tag", field, on)
		}
	}
}
