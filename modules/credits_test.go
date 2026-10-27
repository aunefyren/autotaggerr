package modules

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/aunefyren/autotaggerr/models"
)

// creditsRelease is a two-disc release with relationships the way the lookup returns
// them with Picard's includes: a producer on the release, performers, a remixer and a
// disc-scoped DJ-mixer on the recording, and the performed work's composer and
// lyricist — one credited under another name.
const creditsReleaseJSON = `{"id":"rel","title":"Album","relations":[
  {"type":"producer","target-type":"artist","direction":"backward","artist":{"id":"p1","name":"Rick Rubin","sort-name":"Rubin, Rick"}}],
 "artist-credit":[{"name":"Band","artist":{"id":"b","name":"Band","sort-name":"Band"}}],
 "media":[{"position":1,"tracks":[{"id":"t1","position":1,"title":"Song","recording":{"id":"r1","title":"Song","relations":[
   {"type":"instrument","target-type":"artist","attributes":["guest","bass guitar"],"artist":{"id":"m1","name":"Flea","sort-name":"Flea"}},
   {"type":"vocal","target-type":"artist","attributes":["lead vocals"],"artist":{"id":"m2","name":"Anthony","sort-name":"Kiedis, Anthony"}},
   {"type":"vocal","target-type":"artist","attributes":[],"artist":{"id":"m3","name":"Choir","sort-name":"Choir"}},
   {"type":"producer","target-type":"artist","artist":{"id":"p2","name":"Second Producer","sort-name":"Producer, Second"}},
   {"type":"remixer","target-type":"artist","artist":{"id":"x1","name":"Remixer","sort-name":"Remixer"}},
   {"type":"mix-DJ","target-type":"artist","attributes":["medium 2"],"artist":{"id":"d1","name":"DJ Two","sort-name":"Two, DJ"}},
   {"type":"mastering","target-type":"artist","artist":{"id":"e1","name":"Mastering Engineer"}},
   {"type":"performance","target-type":"work","attributes":[],"work":{"id":"w1","title":"Song (work)","relations":[
     {"type":"composer","target-type":"artist","target-credit":"J. Composer","artist":{"id":"c1","name":"Jane Composer","sort-name":"Composer, Jane"}},
     {"type":"lyricist","target-type":"artist","artist":{"id":"l1","name":"Lyric Writer","sort-name":"Writer, Lyric"}},
     {"type":"composer","target-type":"artist","artist":{"id":"c1","name":"Jane Composer","sort-name":"Composer, Jane"}}]}}]}}]}],
 "autotaggerr_relations_included":true}`

func creditsRelease(t *testing.T) models.MusicBrainzReleaseResponse {
	t.Helper()
	var release models.MusicBrainzReleaseResponse
	if err := json.Unmarshal([]byte(creditsReleaseJSON), &release); err != nil {
		t.Fatal(err)
	}
	return release
}

func TestTrackCredits(t *testing.T) {
	release := creditsRelease(t)
	track := release.Media[0].Tracks[0]

	c := TrackCredits(release, release.Media[0], track, models.TaggerSettings{UseCurrentArtistName: true})
	join := func(v []string) string { return strings.Join(v, "|") }

	// The release's producer first, then the recording's — Picard's order.
	if got := join(c.Producers); got != "Rick Rubin|Second Producer" {
		t.Errorf("producers = %q", got)
	}
	// The same composer credited twice on the work is one composer, by current name.
	if join(c.Composers) != "Jane Composer" || join(c.ComposerSorts) != "Composer, Jane" || join(c.ComposerIDs) != "c1" {
		t.Errorf("composer = %q / %q / %q", c.Composers, c.ComposerSorts, c.ComposerIDs)
	}
	if join(c.Lyricists) != "Lyric Writer" || join(c.Remixers) != "Remixer" {
		t.Errorf("lyricist %q, remixer %q", c.Lyricists, c.Remixers)
	}
	// "medium 2" scopes the DJ-mixer to disc 2; this track is on disc 1. Picard skips
	// "mastering" too.
	if len(c.DJMixers) != 0 || len(c.Engineers) != 0 {
		t.Errorf("djmixers %q, engineers %q, want none", c.DJMixers, c.Engineers)
	}
	wantPerformers := []models.Performer{
		{Role: "guest bass guitar", Name: "Flea"},
		{Role: "lead vocals", Name: "Anthony"},
		{Role: "vocals", Name: "Choir"},
	}
	if len(c.Performers) != len(wantPerformers) {
		t.Fatalf("performers = %+v", c.Performers)
	}
	for i, p := range wantPerformers {
		if c.Performers[i] != p {
			t.Errorf("performer %d = %+v, want %+v", i, c.Performers[i], p)
		}
	}
	if join(c.WorkIDs) != "w1" || join(c.Works) != "Song (work)" {
		t.Errorf("work = %q / %q", c.WorkIDs, c.Works)
	}

	// The name the relationship credits, when the profile does not use current names.
	if credited := TrackCredits(release, release.Media[0], track, models.TaggerSettings{}); join(credited.Composers) != "J. Composer|Jane Composer" {
		t.Errorf("credited composers = %q", credited.Composers)
	}

	// On disc 2 the DJ-mixer applies.
	disc2 := models.MusicBrainzMedia{Position: 2}
	if got := TrackCredits(release, disc2, track, models.TaggerSettings{UseCurrentArtistName: true}); join(got.DJMixers) != "DJ Two" {
		t.Errorf("disc 2 djmixers = %q", got.DJMixers)
	}

	// An instrumental performance of the work takes no lyricist.
	release.Media[0].Tracks[0].Recording.Relations[len(track.Recording.Relations)-1].Attributes = []string{"instrumental"}
	if got := TrackCredits(release, release.Media[0], release.Media[0].Tracks[0], models.TaggerSettings{}); len(got.Lyricists) != 0 {
		t.Errorf("instrumental lyricists = %q, want none", got.Lyricists)
	}
}

// TestCreditsOnlyFromARelationshipFetch: a release cached before relationships were
// fetched writes no credit keys at all, so remove_values cannot clear another tagger's
// composer on the strength of data that was never asked for.
func TestCreditsOnlyFromARelationshipFetch(t *testing.T) {
	release := creditsRelease(t)
	build := func() models.FileTags {
		t.Helper()
		tags, err := BuildFileTags(release.Media[0].Tracks[0], release.Media[0], release, models.TaggerSettings{UseCurrentArtistName: true})
		if err != nil {
			t.Fatal(err)
		}
		return tags
	}

	flac := buildFLACDesiredTags(build())
	if got := strings.Join(flac["PERFORMER"], "|"); got != "Flea (guest bass guitar)|Anthony (lead vocals)|Choir (vocals)" {
		t.Errorf("PERFORMER = %q", got)
	}
	if got := strings.Join(flac["COMPOSER"], "|"); got != "Jane Composer" {
		t.Errorf("COMPOSER = %q", got)
	}
	mp3 := buildMP3DesiredTags(build())
	if got := strings.Join(mp3["TIPL"], "|"); got != "producer\x00Rick Rubin|producer\x00Second Producer" {
		t.Errorf("TIPL = %q, want both producers as two pairs", got)
	}

	release.RelationsIncluded = false
	for _, key := range []string{"COMPOSER", "PERFORMER", "PRODUCER", "MUSICBRAINZ_WORKID"} {
		if _, present := buildFLACDesiredTags(build())[key]; present {
			t.Errorf("FLAC %s present for a release fetched without relationships", key)
		}
	}
	if _, present := buildMP3DesiredTags(build())["TIPL"]; present {
		t.Error("MP3 TIPL present for a release fetched without relationships")
	}
}

// TestCreditsRoundTrip writes credits to a real MP3 and FLAC: TIPL and TMCL come back
// as the same role-and-name pairs — two producers stay two — and a second write is a
// no-op on both engines, with or without the MP3 multi-value setting.
func TestCreditsRoundTrip(t *testing.T) {
	meta := fullFileTags()
	meta.HasCredits = true
	meta.Credits = models.Credits{
		Composers: []string{"Jane Composer", "John Composer"}, ComposerSorts: []string{"Composer, Jane", "Composer, John"},
		Lyricists: []string{"Lyric Writer"}, Producers: []string{"Rick Rubin", "Second Producer"}, Engineers: []string{"Engineer"},
		Performers: []models.Performer{{Role: "bass guitar", Name: "Flea"}, {Role: "lead vocals", Name: "Anthony"}},
		WorkIDs:    []string{"w1"}, Works: []string{"Song"},
	}

	for _, multi := range []bool{false, true} {
		settings := models.TaggerSettings{MP3MultiValueTags: multi}
		path := synthAudio(t, ".mp3")
		if _, _, _, err := SetMP3Tags(path, meta, settings); err != nil {
			t.Fatal(err)
		}
		tags, err := GetMP3Tags(path)
		if err != nil {
			t.Fatal(err)
		}
		if got := strings.Join(tags["TIPL"], "|"); got != "engineer\x00Engineer|producer\x00Rick Rubin|producer\x00Second Producer" {
			t.Errorf("multi=%v TIPL = %q", multi, got)
		}
		if got := strings.Join(tags["TMCL"], "|"); got != "bass guitar\x00Flea|lead vocals\x00Anthony" {
			t.Errorf("multi=%v TMCL = %q", multi, got)
		}
		frames := dumpID3Frames(t, path)
		for _, frame := range []string{"TEXT=Lyric Writer", "TXXX[MusicBrainz Work Id]=w1", "TXXX[WORK]=Song"} {
			if !contains(frames, frame) {
				t.Errorf("multi=%v frames lack %s: %v", multi, frame, frames)
			}
		}
		if unchanged, written, changes, err := SetMP3Tags(path, meta, settings); err != nil || !unchanged || written != 0 {
			t.Errorf("multi=%v second MP3 write = unchanged %v, written %d, changes %+v, %v", multi, unchanged, written, changes, err)
		}
	}

	requireTool(t, "metaflac")
	path := synthAudio(t, ".flac")
	if _, _, _, err := SetFlacTags(path, meta, models.TaggerSettings{}); err != nil {
		t.Fatal(err)
	}
	tags, err := ReadFileTagMap(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(tags["COMPOSER"], "|"); got != "Jane Composer|John Composer" {
		t.Errorf("FLAC COMPOSER = %q", got)
	}
	if got := strings.Join(tags["PERFORMER"], "|"); got != "Flea (bass guitar)|Anthony (lead vocals)" {
		t.Errorf("FLAC PERFORMER = %q", got)
	}
	if unchanged, _, _, err := SetFlacTags(path, meta, models.TaggerSettings{}); err != nil || !unchanged {
		t.Errorf("second FLAC write changed something: %v", err)
	}
}
