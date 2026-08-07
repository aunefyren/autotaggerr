package modules

import (
	"fmt"
	"os"
	"os/exec"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/aunefyren/autotaggerr/models"
	"github.com/aunefyren/autotaggerr/utilities"
	"github.com/bogem/id3v2"
)

// writeLegacyMP3Tags writes tags the way SetMP3Tags did before it moved onto
// github.com/bogem/id3v2: one `ffmpeg -codec copy` pass over the whole file. Every
// MP3 in every library Autotaggerr has already touched looks like this, so it is the
// fixture the migration has to be measured against.
func writeLegacyMP3Tags(t *testing.T, path string, meta models.FileTags) {
	t.Helper()
	requireTool(t, "ffmpeg")

	args := []string{
		"-nostdin", "-loglevel", "error",
		"-i", path, "-y",
		"-map_metadata", "0",
		"-codec", "copy",
		"-write_id3v1", "1",
		"-id3v2_version", "4",
	}
	add := func(key, value string) {
		args = append(args, "-metadata", key+"="+value)
	}

	add("artist", meta.Artist)
	add("ARTISTS", utilities.JoinTagValues(meta.Artists))
	add("album_artist", meta.AlbumArtist)
	add("ALBUMARTISTS", utilities.JoinTagValues(meta.AlbumArtists))
	add("genre", utilities.JoinTagValues(meta.Genres))
	add("date", meta.ReleaseDate)
	add("year", meta.ReleaseYear)
	add("TDOR", meta.OriginalDate)
	add("originaldate", meta.OriginalDate)
	add("originalyear", meta.OriginalYear)
	add("album", meta.Album)
	add("title", meta.Title)
	add("SCRIPT", meta.Script)
	add("TMED", meta.Media)
	add("publisher", utilities.JoinTagValues(meta.RecordLabels))
	add("BARCODE", meta.Barcode)
	add("CATALOGNUMBER", utilities.JoinTagValues(meta.CatalogNumbers))
	add("MusicBrainz Album Status", meta.MBAlbumStatus)
	add("MusicBrainz Album Type", meta.MBAlbumType)
	add("MusicBrainz Album Release Country", meta.MBAlbumReleaseCountry)
	add("MusicBrainz Album Id", meta.MBAlbumID)
	add("MusicBrainz Artist Id", utilities.JoinTagValues(meta.MBArtistIDs))
	add("MusicBrainz Album Artist Id", utilities.JoinTagValues(meta.MBAlbumArtistIDs))
	add("MusicBrainz Release Group Id", meta.MBReleaseGroupID)
	add("MusicBrainz Release Track Id", meta.MBReleaseTrackID)
	add("MusicBrainz Recording Id", meta.MBRecordingID)
	add("track", pairedFrameValue(meta.Track, meta.TrackTotal))
	add("disc", pairedFrameValue(meta.DiscNumber, meta.DiscTotal))
	if isrc := utilities.JoinTagValues(meta.ISRCs); isrc != "" {
		add("TXXX", "ISRC:"+isrc)
	}

	temp := path + ".legacy.mp3"
	args = append(args, temp)
	if out, err := exec.Command("ffmpeg", args...).CombinedOutput(); err != nil {
		t.Fatalf("legacy ffmpeg write failed: %v\n%s", err, out)
	}
	if err := os.Rename(temp, path); err != nil {
		t.Fatalf("replace fixture: %v", err)
	}
}

// TestLegacyFfmpegFilesNeedNoRewrite is the property that makes the engine swap safe
// to ship: a file the old writer tagged must read back through the new reader as
// already correct. If it does not, changing the engine silently re-tags every MP3 in
// every library — hours of writes, and a modification time on every file, for a
// change that was supposed to be invisible.
func TestLegacyFfmpegFilesNeedNoRewrite(t *testing.T) {
	path := synthAudio(t, ".mp3")
	meta := fullFileTags()
	writeLegacyMP3Tags(t, path, meta)

	unchanged, written, changes, err := SetMP3Tags(path, meta, models.ConfigStruct{})
	if err != nil {
		t.Fatalf("SetMP3Tags: %v", err)
	}
	if !unchanged || written != 0 {
		for _, change := range changes {
			t.Errorf("would rewrite %s: %q -> %q", change.Field, change.Old, change.New)
		}
		t.Fatalf("a file written by the ffmpeg writer must need no rewrite, got unchanged=%v written=%d",
			unchanged, written)
	}
}

// TestNewWriterReproducesTheLegacyFrames is the other half: not just "no diff", but
// the same frames on disk. A reader that happened to be forgiving in the same way the
// writer was wrong would pass the test above; this one compares the actual tag.
//
// TSSE is excluded because ffmpeg stamps its own encoder name into every file it
// rewrites, which is exactly the kind of gratuitous change this migration removes.
func TestNewWriterReproducesTheLegacyFrames(t *testing.T) {
	meta := fullFileTags()

	legacyPath := synthAudio(t, ".mp3")
	writeLegacyMP3Tags(t, legacyPath, meta)

	newPath := synthAudio(t, ".mp3")
	if _, _, _, err := SetMP3Tags(newPath, meta, models.ConfigStruct{}); err != nil {
		t.Fatalf("SetMP3Tags: %v", err)
	}

	legacy := dumpID3Frames(t, legacyPath)
	current := dumpID3Frames(t, newPath)

	for _, frame := range legacy {
		if !contains(current, frame) {
			t.Errorf("the new writer did not produce: %s", frame)
		}
	}
	for _, frame := range current {
		if !contains(legacy, frame) {
			t.Errorf("the new writer produced something ffmpeg did not: %s", frame)
		}
	}
}

// dumpID3Frames renders a file's tag as sorted, comparable lines.
func dumpID3Frames(t *testing.T, path string) []string {
	t.Helper()
	tag, err := id3v2.Open(path, id3v2.Options{Parse: true})
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	defer tag.Close()

	if version := tag.Version(); version != 4 {
		t.Errorf("%s is ID3v2.%d, want 2.4", path, version)
	}

	lines := []string{}
	for frameID, frames := range tag.AllFrames() {
		if frameID == "TSSE" {
			continue
		}
		for _, frame := range frames {
			switch typed := frame.(type) {
			case id3v2.TextFrame:
				lines = append(lines, fmt.Sprintf("%s=%s", frameID, typed.Text))
			case id3v2.UserDefinedTextFrame:
				lines = append(lines, fmt.Sprintf("TXXX[%s]=%s", typed.Description, typed.Value))
			default:
				lines = append(lines, fmt.Sprintf("%s=<%T>", frameID, frame))
			}
		}
	}
	sort.Strings(lines)
	return lines
}

func contains(lines []string, want string) bool {
	for _, line := range lines {
		if line == want {
			return true
		}
	}
	return false
}

// TestMP3TagsSurviveWithoutFfmpeg guards the dependency change: reading and writing
// ID3 is now pure Go, so neither path may shell out. The test forces an empty PATH,
// which makes any exec.Command fail.
func TestMP3TagsSurviveWithoutFfmpeg(t *testing.T) {
	path := synthAudio(t, ".mp3")
	meta := models.FileTags{Artist: "A", Album: "B", Title: "C", Genres: []string{"jazz"}}

	t.Setenv("PATH", "")

	if _, _, _, err := SetMP3Tags(path, meta, models.ConfigStruct{}); err != nil {
		t.Fatalf("SetMP3Tags without ffmpeg on PATH: %v", err)
	}
	tags, err := GetMP3Tags(path)
	if err != nil {
		t.Fatalf("GetMP3Tags without ffprobe on PATH: %v", err)
	}
	if got := firstTag(tags, "GENRE"); got != "jazz" {
		t.Errorf("GENRE = %q, want jazz", got)
	}
	if !strings.EqualFold(firstTag(tags, "ARTIST"), "A") {
		t.Errorf("ARTIST = %q, want A", firstTag(tags, "ARTIST"))
	}
}

// TestMP3MultiValueSetting covers the profile switch that decides how an MP3 says a
// field has several values. Both forms have to round-trip and both have to converge,
// because a representation that does not read back as it was written re-tags the file
// on every scan forever.
func TestMP3MultiValueSetting(t *testing.T) {
	joined := models.ConfigStruct{}
	multiValue := models.ConfigStruct{AutotaggerrMP3MultiValueTags: true}
	meta := models.FileTags{
		Artist: "A", Album: "B", Title: "C",
		Genres: []string{"hip hop", "rap", "jazz rap"},
	}

	t.Run("joined", func(t *testing.T) {
		path := synthAudio(t, ".mp3")
		if _, _, _, err := SetMP3Tags(path, meta, joined); err != nil {
			t.Fatalf("write: %v", err)
		}
		if got := frameText(t, path, "TCON"); got != "hip hop; rap; jazz rap" {
			t.Errorf("TCON = %q, want one joined value", got)
		}
		assertSecondWriteIsNoOpWith(t, path, meta, joined, SetMP3Tags)
	})

	t.Run("null separated", func(t *testing.T) {
		path := synthAudio(t, ".mp3")
		if _, _, _, err := SetMP3Tags(path, meta, multiValue); err != nil {
			t.Fatalf("write: %v", err)
		}
		if got := frameText(t, path, "TCON"); got != "hip hop\x00rap\x00jazz rap" {
			t.Errorf("TCON = %q, want the values separated by null bytes", got)
		}
		// And the reader has to see them as separate values, not one string.
		tags, err := GetMP3Tags(path)
		if err != nil {
			t.Fatalf("GetMP3Tags: %v", err)
		}
		if !slices.Equal(tags["GENRE"], meta.Genres) {
			t.Errorf("GENRE = %v, want %v", tags["GENRE"], meta.Genres)
		}
		assertSecondWriteIsNoOpWith(t, path, meta, multiValue, SetMP3Tags)
	})
}

// TestMP3MultiValueSettingConvergesAfterAFlip is the property that makes the setting
// safe to change on a live library: flipping it rewrites each file once and then
// stops. The reader splits on the null byte whatever the setting says, which is what
// lets a half-converted library read correctly in both directions.
func TestMP3MultiValueSettingConvergesAfterAFlip(t *testing.T) {
	meta := fullFileTags()
	joined := models.ConfigStruct{}
	multiValue := models.ConfigStruct{AutotaggerrMP3MultiValueTags: true}

	path := synthAudio(t, ".mp3")
	if _, _, _, err := SetMP3Tags(path, meta, joined); err != nil {
		t.Fatalf("initial write: %v", err)
	}

	// Flipping it on is a change...
	unchanged, _, _, err := SetMP3Tags(path, meta, multiValue)
	if err != nil {
		t.Fatalf("write after flip: %v", err)
	}
	if unchanged {
		t.Error("turning multi-value on should rewrite a joined file")
	}
	assertSecondWriteIsNoOpWith(t, path, meta, multiValue, SetMP3Tags)

	// ...and so is flipping it back, exactly once.
	if unchanged, _, _, err := SetMP3Tags(path, meta, joined); err != nil {
		t.Fatalf("write after flip back: %v", err)
	} else if unchanged {
		t.Error("turning multi-value off again should rewrite the file")
	}
	assertSecondWriteIsNoOpWith(t, path, meta, joined, SetMP3Tags)
}

// frameText returns one text frame's raw payload, null bytes and all.
func frameText(t *testing.T, path, frameID string) string {
	t.Helper()
	tag, err := id3v2.Open(path, id3v2.Options{Parse: true})
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	defer tag.Close()
	return tag.GetTextFrame(frameID).Text
}

func assertSecondWriteIsNoOpWith(
	t *testing.T,
	path string,
	meta models.FileTags,
	configFile models.ConfigStruct,
	write func(string, models.FileTags, models.ConfigStruct) (bool, int, []models.TagChange, error),
) {
	t.Helper()
	unchanged, written, changes, err := write(path, meta, configFile)
	if err != nil {
		t.Fatalf("second write: %v", err)
	}
	if !unchanged || written != 0 {
		for _, change := range changes {
			t.Errorf("did not converge on %s: %q -> %q", change.Field, change.Old, change.New)
		}
		t.Fatalf("second identical write must be a no-op, got unchanged=%v written=%d", unchanged, written)
	}
}
