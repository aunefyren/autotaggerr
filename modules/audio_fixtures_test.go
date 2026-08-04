package modules

import (
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/aunefyren/autotaggerr/models"
)

// These tests exercise the real metaflac/ffmpeg/ffprobe read+write paths against
// tiny synthesized fixture files. They self-skip when the tools are absent so a
// plain `go test` never fails on a machine without them (CI installs them).

func requireTool(t *testing.T, tool string) {
	t.Helper()
	if _, err := exec.LookPath(tool); err != nil {
		t.Skipf("%q not on PATH; skipping audio fixture test", tool)
	}
}

// synthAudio creates a ~0.1s silent file of the given extension in a temp dir.
func synthAudio(t *testing.T, ext string) string {
	t.Helper()
	requireTool(t, "ffmpeg")
	path := filepath.Join(t.TempDir(), "track"+ext)
	cmd := exec.Command("ffmpeg", "-nostdin", "-loglevel", "error",
		"-f", "lavfi", "-i", "anullsrc=r=44100:cl=mono", "-t", "0.1", path)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("ffmpeg synth %s failed: %v\n%s", ext, err, out)
	}
	return path
}

func firstTag(m map[string][]string, key string) string {
	if v := m[key]; len(v) > 0 {
		return v[0]
	}
	return ""
}

// --- FLAC -------------------------------------------------------------------

func TestFlacSetReadRoundTrip(t *testing.T) {
	requireTool(t, "metaflac")
	path := synthAudio(t, ".flac")

	meta := models.FileTags{
		Artist:        "Compton’s Most Wanted", // curly apostrophe must survive
		Album:         "Music to Driveby",
		Title:         "Intro",
		Track:         "1",
		Genres:        []string{"Hip Hop", "Rap"}, // FLAC writes only the first
		MBAlbumID:     "album-id-123",
		MBRecordingID: "rec-id-456",
	}

	unchanged, written, _, err := SetFlacTags(path, meta, models.ConfigStruct{})
	if err != nil {
		t.Fatalf("SetFlacTags: %v", err)
	}
	if unchanged || written == 0 {
		t.Fatalf("expected changes on first write, got unchanged=%v written=%d", unchanged, written)
	}

	tags, err := getFlacTagsMap(path)
	if err != nil {
		t.Fatalf("getFlacTagsMap: %v", err)
	}
	checks := map[string]string{
		"ARTIST":              "Compton’s Most Wanted",
		"ALBUM":               "Music to Driveby",
		"TITLE":               "Intro",
		"GENRE":               "Hip Hop", // first genre only
		"MUSICBRAINZ_ALBUMID": "album-id-123",
		"MUSICBRAINZ_TRACKID": "rec-id-456", // recording id -> TRACKID
	}
	for key, want := range checks {
		if got := firstTag(tags, key); got != want {
			t.Errorf("FLAC tag %s = %q, want %q", key, got, want)
		}
	}
}

func TestFlacIdempotentWrite(t *testing.T) {
	requireTool(t, "metaflac")
	path := synthAudio(t, ".flac")
	meta := models.FileTags{Artist: "Artist", Album: "Album", Title: "Title", Track: "1"}

	if _, _, _, err := SetFlacTags(path, meta, models.ConfigStruct{}); err != nil {
		t.Fatalf("first SetFlacTags: %v", err)
	}
	unchanged, written, _, err := SetFlacTags(path, meta, models.ConfigStruct{})
	if err != nil {
		t.Fatalf("second SetFlacTags: %v", err)
	}
	if !unchanged || written != 0 {
		t.Errorf("second identical write should be a no-op, got unchanged=%v written=%d", unchanged, written)
	}
}

func TestExtractFLACTag(t *testing.T) {
	requireTool(t, "metaflac")
	path := synthAudio(t, ".flac")
	meta := models.FileTags{Artist: "A", Album: "B", Title: "C", MBAlbumID: "the-album-id"}
	if _, _, _, err := SetFlacTags(path, meta, models.ConfigStruct{}); err != nil {
		t.Fatalf("SetFlacTags: %v", err)
	}

	// resolve by metadataType (release -> MUSICBRAINZ_ALBUMID)
	got, err := ExtractFLACTag(path, "", "release")
	if err != nil {
		t.Fatalf("ExtractFLACTag: %v", err)
	}
	if got != "the-album-id" {
		t.Errorf("ExtractFLACTag(release) = %q, want the-album-id", got)
	}
}

// --- MP3 --------------------------------------------------------------------

func TestMP3SetReadRoundTrip(t *testing.T) {
	requireTool(t, "ffprobe")
	path := synthAudio(t, ".mp3")

	meta := models.FileTags{
		Artist: "Kendrick Lamar",
		Album:  "Welcome to Compton",
		Title:  "Intro",
		Genres: []string{"Hip Hop"},
	}
	unchanged, written, _, err := SetMP3Tags(path, meta, models.ConfigStruct{})
	if err != nil {
		t.Fatalf("SetMP3Tags: %v", err)
	}
	if unchanged || written == 0 {
		t.Fatalf("expected changes on first write, got unchanged=%v written=%d", unchanged, written)
	}

	tags, err := GetMP3Tags(path)
	if err != nil {
		t.Fatalf("GetMP3Tags: %v", err)
	}
	checks := map[string]string{
		"ARTIST": "Kendrick Lamar",
		"ALBUM":  "Welcome to Compton",
		"TITLE":  "Intro",
		"GENRE":  "Hip Hop",
	}
	for key, want := range checks {
		if got := firstTag(tags, key); got != want {
			t.Errorf("MP3 tag %s = %q, want %q", key, got, want)
		}
	}
}

func TestMP3IdempotentWrite(t *testing.T) {
	requireTool(t, "ffprobe")
	path := synthAudio(t, ".mp3")
	meta := models.FileTags{Artist: "Artist", Album: "Album", Title: "Title"}

	if _, _, _, err := SetMP3Tags(path, meta, models.ConfigStruct{}); err != nil {
		t.Fatalf("first SetMP3Tags: %v", err)
	}
	unchanged, written, _, err := SetMP3Tags(path, meta, models.ConfigStruct{})
	if err != nil {
		t.Fatalf("second SetMP3Tags: %v", err)
	}
	if !unchanged || written != 0 {
		t.Errorf("second identical write should be a no-op, got unchanged=%v written=%d", unchanged, written)
	}
}

// TestMP3MultiISRCIdempotent guards a round-trip bug where a "; "-joined ISRC
// (tracks with more than one ISRC, common on singles/features) was written into a
// single frame but read back as only its first value, so the diff never converged
// and the file was re-tagged on every scan. The ISRC must survive read-back intact.
func TestDiffFileTags(t *testing.T) {
	requireTool(t, "metaflac")
	path := synthAudio(t, ".flac")

	// Seed the file with an initial set of tags.
	initial := models.FileTags{Album: "Old Album", Title: "Song", MBAlbumID: "rel-1"}
	if _, _, _, err := SetFlacTags(path, initial, models.ConfigStruct{}); err != nil {
		t.Fatalf("SetFlacTags: %v", err)
	}

	// Desired changes only the album.
	desired := models.FileTags{Album: "New Album", Title: "Song", MBAlbumID: "rel-1"}
	entries, err := DiffFileTags(path, desired, models.ConfigStruct{})
	if err != nil {
		t.Fatalf("DiffFileTags: %v", err)
	}

	byKey := map[string]models.TagDiffEntry{}
	for _, e := range entries {
		byKey[e.Key] = e
	}

	album := byKey["ALBUM"]
	if !album.Changed || album.Current != "Old Album" || album.Desired != "New Album" {
		t.Errorf("ALBUM diff wrong: %+v", album)
	}
	title := byKey["TITLE"]
	if title.Changed || title.Current != "Song" || title.Desired != "Song" {
		t.Errorf("TITLE should be unchanged: %+v", title)
	}
	if id, ok := byKey["MUSICBRAINZ_ALBUMID"]; !ok || id.Changed {
		t.Errorf("MUSICBRAINZ_ALBUMID should be present and unchanged: %+v", id)
	}
}

func TestMP3MultiISRCIdempotent(t *testing.T) {
	requireTool(t, "ffprobe")
	path := synthAudio(t, ".mp3")
	meta := models.FileTags{
		Artist: "Kendrick Lamar",
		Album:  "The Heart Pt. 3 (Will You Let It Die?)",
		Title:  "The Heart Pt. 3 (Will You Let It Die?)",
		ISRC:   "USUM72108711; USUM72108712",
	}

	if _, _, _, err := SetMP3Tags(path, meta, models.ConfigStruct{}); err != nil {
		t.Fatalf("first SetMP3Tags: %v", err)
	}

	tags, err := GetMP3Tags(path)
	if err != nil {
		t.Fatalf("GetMP3Tags: %v", err)
	}
	if got := firstTag(tags, "ISRC"); got != meta.ISRC {
		t.Errorf("ISRC did not round-trip: got %q, want %q", got, meta.ISRC)
	}

	unchanged, written, _, err := SetMP3Tags(path, meta, models.ConfigStruct{})
	if err != nil {
		t.Fatalf("second SetMP3Tags: %v", err)
	}
	if !unchanged || written != 0 {
		t.Errorf("multi-ISRC second write should be a no-op, got unchanged=%v written=%d", unchanged, written)
	}
}
