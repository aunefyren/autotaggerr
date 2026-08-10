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

// TestUnkeyedTXXXFrameIsClearedButNeverCausesAWrite covers the wreckage a foreign
// tagger can leave: a TXXX frame with no description, which is a value with no key.
//
// Both halves matter. It must not be a reason to rewrite a file — a library full of
// them would otherwise be rewritten on the next scan for something no user asked about
// — and it must not survive a rewrite that happens anyway, because nothing else in the
// engine can see it or address it.
func TestUnkeyedTXXXFrameIsClearedButNeverCausesAWrite(t *testing.T) {
	path := synthAudio(t, ".mp3")
	meta := models.FileTags{Artist: "Jerry Goldsmith", Album: "Alien", Title: "Main Title"}

	if _, _, _, err := SetMP3Tags(path, meta, models.TaggerSettings{}); err != nil {
		t.Fatalf("first SetMP3Tags: %v", err)
	}

	// The frame as Windows leaves it: the description shifted into the value.
	addUnkeyed := func() {
		tag, err := id3v2.Open(path, id3v2.Options{Parse: true})
		if err != nil {
			t.Fatalf("open for seeding: %v", err)
		}
		tag.AddUserDefinedTextFrame(id3v2.UserDefinedTextFrame{
			Encoding: id3v2.EncodingUTF8, Description: "", Value: "MusicBrainz Recording Id",
		})
		if err := tag.Save(); err != nil {
			t.Fatalf("save seeded frame: %v", err)
		}
		tag.Close()
	}
	unkeyedFrames := func() int {
		tag, err := id3v2.Open(path, id3v2.Options{Parse: true})
		if err != nil {
			t.Fatalf("open for counting: %v", err)
		}
		defer tag.Close()
		n := 0
		for _, frame := range tag.GetFrames("TXXX") {
			if userDefined, ok := frame.(id3v2.UserDefinedTextFrame); ok && userDefined.Description == "" {
				n++
			}
		}
		return n
	}

	addUnkeyed()
	if unkeyedFrames() != 1 {
		t.Fatalf("fixture did not take: %d unkeyed frames", unkeyedFrames())
	}

	// Nothing else changed, so nothing may be written — and the frame stays.
	unchanged, written, _, err := SetMP3Tags(path, meta, models.TaggerSettings{})
	if err != nil {
		t.Fatalf("second SetMP3Tags: %v", err)
	}
	if !unchanged || written != 0 {
		t.Errorf("an unkeyed frame forced a rewrite (unchanged=%v written=%d)", unchanged, written)
	}
	if unkeyedFrames() != 1 {
		t.Errorf("the file was rewritten when it should not have been")
	}

	// A real change rewrites the tag, and takes the wreckage with it.
	meta.Title = "Main Title (alternate)"
	if _, written, _, err = SetMP3Tags(path, meta, models.TaggerSettings{}); err != nil {
		t.Fatalf("third SetMP3Tags: %v", err)
	}
	if unkeyedFrames() != 0 {
		t.Errorf("the unkeyed frame survived a rewrite")
	}
	// One key changed and one frame was dropped, so the writer's count exceeds the
	// diff — the property that lets a cleanup happen without inventing a diff row.
	if written < 2 {
		t.Errorf("tags written = %d, want the title change plus the dropped frame", written)
	}

	// And the tags it was supposed to write are still right.
	tags, err := GetMP3Tags(path)
	if err != nil {
		t.Fatalf("GetMP3Tags: %v", err)
	}
	if got := firstTag(tags, "TITLE"); got != "Main Title (alternate)" {
		t.Errorf("TITLE = %q, want the new title", got)
	}
}

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

// TestLegacyFfmpegFilesConvergeAfterOneRewrite is the property that makes the UFID and
// TSRC pass safe to ship. It replaces an older test that asserted a legacy file needed
// *no* rewrite at all — true of the engine swap, which was supposed to be invisible,
// and deliberately not true of this change, which adds a frame and moves another.
//
// What must still hold is that the cost is paid once. A file the ffmpeg-era writer
// tagged is rewritten exactly one time, and reads as correct forever after; a field
// that failed to converge would re-tag every MP3 in every library on every scan, which
// is the failure this guards.
//
// The first rewrite must also be *only* the intended fields. Anything else in the diff
// means this pass is dragging an unrelated regression along with it.
func TestLegacyFfmpegFilesConvergeAfterOneRewrite(t *testing.T) {
	path := synthAudio(t, ".mp3")
	meta := fullFileTags()
	writeLegacyMP3Tags(t, path, meta)

	unchanged, written, changes, err := SetMP3Tags(path, meta, models.TaggerSettings{})
	if err != nil {
		t.Fatalf("SetMP3Tags: %v", err)
	}
	if unchanged || written == 0 {
		t.Fatalf("a legacy file must be migrated once, got unchanged=%v written=%d", unchanged, written)
	}
	// The ISRC moves frames without changing value, so it is not in the diff — the
	// migration is silent by design. UFID is the only field whose value is new.
	for _, change := range changes {
		if change.Field != ufidTagKey {
			t.Errorf("unexpected field in the migration diff: %s (%q -> %q)",
				change.Field, change.Old, change.New)
		}
	}

	// The frames themselves: the artefact gone, the standard ones in its place.
	frames := dumpID3Frames(t, path)
	isrc := utilities.JoinTagValues(meta.ISRCs)
	if contains(frames, fmt.Sprintf("TXXX[%s]=%s", legacyISRCFrameDescription, packISRCFrameValue(isrc))) {
		t.Error("the legacy ISRC frame survived the migration")
	}
	if !contains(frames, "TSRC="+isrc) {
		t.Errorf("ISRC did not reach TSRC; frames: %v", frames)
	}
	if !contains(frames, fmt.Sprintf("UFID[%s]=%s", musicBrainzUFIDOwner, meta.MBRecordingID)) {
		t.Errorf("UFID not written; frames: %v", frames)
	}

	// And it settles: the second write is a no-op.
	unchanged, written, changes, err = SetMP3Tags(path, meta, models.TaggerSettings{})
	if err != nil {
		t.Fatalf("second SetMP3Tags: %v", err)
	}
	if !unchanged || written != 0 {
		for _, change := range changes {
			t.Errorf("would rewrite again: %s %q -> %q", change.Field, change.Old, change.New)
		}
		t.Fatalf("a migrated file must be stable, got unchanged=%v written=%d", unchanged, written)
	}
}

// TestMigratedISRCSurvivesWhenTheReleaseNoLongerSuppliesOne is the case that separates
// a frame migration from a deletion. A file carrying the legacy ISRC frame, rewritten
// while the desired tags have no ISRC at all, must keep the one it has: the value is
// read off the file, not out of the desired map.
//
// Getting this wrong loses an ISRC on any track whose MusicBrainz data thinned out —
// silently, and only on files that were tagged before the move.
func TestMigratedISRCSurvivesWhenTheReleaseNoLongerSuppliesOne(t *testing.T) {
	path := synthAudio(t, ".mp3")
	meta := fullFileTags()
	writeLegacyMP3Tags(t, path, meta)
	isrc := utilities.JoinTagValues(meta.ISRCs)

	// remove_values stays off, so an empty desired ISRC is "nothing to say", not
	// "delete it" — which is exactly when the value must be carried across.
	thinned := meta
	thinned.ISRCs = nil
	if _, _, _, err := SetMP3Tags(path, thinned, models.TaggerSettings{}); err != nil {
		t.Fatalf("SetMP3Tags: %v", err)
	}

	frames := dumpID3Frames(t, path)
	if !contains(frames, "TSRC="+isrc) {
		t.Errorf("the migration dropped an ISRC the file already had; frames: %v", frames)
	}

	tags, err := GetMP3Tags(path)
	if err != nil {
		t.Fatalf("GetMP3Tags: %v", err)
	}
	if got := utilities.JoinTagValues(tags["ISRC"]); got != isrc {
		t.Errorf("ISRC reads back as %q, want %q", got, isrc)
	}
}

// TestForeignUFIDFramesAreLeftAlone: the UFID frame ID is shared by every tagger that
// writes one, and only the MusicBrainz owner is ours. Deleting by frame ID would throw
// away identifiers belonging to tools Autotaggerr knows nothing about.
func TestForeignUFIDFramesAreLeftAlone(t *testing.T) {
	path := synthAudio(t, ".mp3")
	meta := fullFileTags()

	tag, err := id3v2.Open(path, id3v2.Options{Parse: true})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	tag.SetVersion(4)
	tag.AddUFIDFrame(id3v2.UFIDFrame{
		OwnerIdentifier: "http://example.org/other-tagger",
		Identifier:      []byte("not-ours"),
	})
	if err := tag.Save(); err != nil {
		t.Fatalf("save: %v", err)
	}
	_ = tag.Close()

	// Write twice: the second pass is where a delete-by-ID bug shows up, because by
	// then our own frame exists too and the foreign one is no longer the only UFID.
	for i := 0; i < 2; i++ {
		if _, _, _, err := SetMP3Tags(path, meta, models.TaggerSettings{}); err != nil {
			t.Fatalf("SetMP3Tags pass %d: %v", i+1, err)
		}
	}

	frames := dumpID3Frames(t, path)
	if !contains(frames, "UFID[http://example.org/other-tagger]=not-ours") {
		t.Errorf("a foreign UFID frame was removed; frames: %v", frames)
	}
	if !contains(frames, fmt.Sprintf("UFID[%s]=%s", musicBrainzUFIDOwner, meta.MBRecordingID)) {
		t.Errorf("our own UFID frame is missing; frames: %v", frames)
	}

	// Ours must appear exactly once — AddUFIDFrame appends, so a missing delete
	// would stack a new frame on every write.
	ours := 0
	for _, line := range frames {
		if strings.HasPrefix(line, "UFID["+musicBrainzUFIDOwner+"]") {
			ours++
		}
	}
	if ours != 1 {
		t.Errorf("wrote %d MusicBrainz UFID frames, want exactly 1", ours)
	}
}

// TestUFIDIsReadBackFromEitherScheme: some taggers write the owner over https. A file
// carrying that form already has the right identifier, and reporting it as drift would
// rewrite it on every scan without ever settling.
func TestUFIDIsReadBackFromEitherScheme(t *testing.T) {
	path := synthAudio(t, ".mp3")
	meta := fullFileTags()
	if _, _, _, err := SetMP3Tags(path, meta, models.TaggerSettings{}); err != nil {
		t.Fatalf("SetMP3Tags: %v", err)
	}

	// Replace ours with the https spelling, leaving everything else settled.
	tag, err := id3v2.Open(path, id3v2.Options{Parse: true})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	tag.SetVersion(4)
	tag.DeleteFrames("UFID")
	tag.AddUFIDFrame(id3v2.UFIDFrame{
		OwnerIdentifier: musicBrainzUFIDOwnerAlt,
		Identifier:      []byte(meta.MBRecordingID),
	})
	if err := tag.Save(); err != nil {
		t.Fatalf("save: %v", err)
	}
	_ = tag.Close()

	unchanged, _, changes, err := SetMP3Tags(path, meta, models.TaggerSettings{})
	if err != nil {
		t.Fatalf("SetMP3Tags: %v", err)
	}
	if !unchanged {
		for _, change := range changes {
			t.Errorf("would rewrite %s: %q -> %q", change.Field, change.Old, change.New)
		}
		t.Error("an https-owner UFID carrying the right MBID must not read as drift")
	}
}

// TestNewWriterReproducesTheLegacyFrames is the other half: not just "no diff", but
// the same frames on disk. A reader that happened to be forgiving in the same way the
// writer was wrong would pass the convergence test above; this one compares the actual
// tag.
//
// The three intended departures from the ffmpeg-era tag are listed explicitly rather
// than the comparison being loosened, so this keeps catching what it was written for:
// any *fourth* difference is a regression, not a feature. Drop an entry here when a
// frame stops being intentional and the test starts failing again, which is the point.
//
// TSSE is excluded because ffmpeg stamps its own encoder name into every file it
// rewrites, which is exactly the kind of gratuitous change this migration removes.
func TestNewWriterReproducesTheLegacyFrames(t *testing.T) {
	meta := fullFileTags()

	legacyPath := synthAudio(t, ".mp3")
	writeLegacyMP3Tags(t, legacyPath, meta)

	newPath := synthAudio(t, ".mp3")
	if _, _, _, err := SetMP3Tags(newPath, meta, models.TaggerSettings{}); err != nil {
		t.Fatalf("SetMP3Tags: %v", err)
	}

	legacy := dumpID3Frames(t, legacyPath)
	current := dumpID3Frames(t, newPath)

	isrc := utilities.JoinTagValues(meta.ISRCs)
	// Retired: the ISRC artefact, a TXXX frame described "TXXX".
	retired := []string{
		fmt.Sprintf("TXXX[%s]=%s", legacyISRCFrameDescription, packISRCFrameValue(isrc)),
	}
	// Added: the standard ISRC frame, and Picard's home for the recording MBID.
	added := []string{
		"TSRC=" + isrc,
		fmt.Sprintf("UFID[%s]=%s", musicBrainzUFIDOwner, meta.MBRecordingID),
	}

	for _, frame := range legacy {
		if !contains(current, frame) && !contains(retired, frame) {
			t.Errorf("the new writer did not produce: %s", frame)
		}
	}
	for _, frame := range current {
		if !contains(legacy, frame) && !contains(added, frame) {
			t.Errorf("the new writer produced something ffmpeg did not: %s", frame)
		}
	}

	// The intended departures must actually have happened — otherwise this test would
	// keep passing if the migration silently stopped working.
	for _, frame := range retired {
		if contains(current, frame) {
			t.Errorf("expected %s to be retired, but the new writer still produces it", frame)
		}
	}
	for _, frame := range added {
		if !contains(current, frame) {
			t.Errorf("expected the new writer to produce %s", frame)
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
			case id3v2.UFIDFrame:
				// Rendered by owner and payload: a UFID compared as "<id3v2.UFIDFrame>"
				// would match any other tagger's identifier, which is the one thing the
				// owner string exists to distinguish.
				lines = append(lines, fmt.Sprintf("UFID[%s]=%s", typed.OwnerIdentifier, typed.Identifier))
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

	if _, _, _, err := SetMP3Tags(path, meta, models.TaggerSettings{}); err != nil {
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
	joined := models.TaggerSettings{}
	multiValue := models.TaggerSettings{MP3MultiValueTags: true}
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
	joined := models.TaggerSettings{}
	multiValue := models.TaggerSettings{MP3MultiValueTags: true}

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
	tagger models.TaggerSettings,
	write func(string, models.FileTags, models.TaggerSettings) (bool, int, []models.TagChange, error),
) {
	t.Helper()
	unchanged, written, changes, err := write(path, meta, tagger)
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
