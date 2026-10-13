package modules

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/aunefyren/autotaggerr/models"
	"github.com/bogem/id3v2"
)

// --- which value, if any ----------------------------------------------------

func storeTempo(t *testing.T, path string, bpm, confidence float64) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	hash, err := AudioPayloadHash(path)
	if err != nil {
		t.Fatal(err)
	}
	mod := info.ModTime()
	row := models.TrackTempo{
		Path: path, Size: info.Size(), ModTime: &mod, AudioHash: hash,
		BPM: bpm, Confidence: confidence, Beats: 400,
		Detector: TempoDetector, DetectorVersion: TempoDetectorVersion(), AnalysedAt: time.Now(),
	}
	if err := cacheDB.Save(&row).Error; err != nil {
		t.Fatal(err)
	}
}

var writeTempo = models.TaggerSettings{BPMWrite: models.BPMWriteOverwrite}

func TestTempoTagValueRoundsTheStoredTempo(t *testing.T) {
	dbForCache(t)
	path := writeFile(t, "a.mp3", audioFrames)
	storeTempo(t, path, 104.96, 0.97)

	if got := TempoTagValue(path, writeTempo); got != "105" {
		t.Fatalf("TempoTagValue = %q, want the whole number 105", got)
	}
}

func TestTempoTagValueHasNothingToSay(t *testing.T) {
	dbForCache(t)
	path := writeFile(t, "a.mp3", audioFrames)
	unanalysed := writeFile(t, "b.mp3", audioFrames)
	storeTempo(t, path, 128, 0.9)

	cases := map[string]struct {
		path   string
		tagger models.TaggerSettings
	}{
		"profile does not write tempo": {path, models.TaggerSettings{}},
		"file never analysed":          {unanalysed, writeTempo},
		"below the profile's floor":    {path, models.TaggerSettings{BPMWrite: models.BPMWriteOverwrite, BPMMinConfidence: 0.95}},
	}
	for name, tc := range cases {
		if got := TempoTagValue(tc.path, tc.tagger); got != "" {
			t.Errorf("%s: TempoTagValue = %q, want nothing", name, got)
		}
	}
}

// A profile row predating the column has a zero floor, which means the default — not
// "write everything, garbage included".
func TestTempoTagValueDefaultFloor(t *testing.T) {
	dbForCache(t)
	weak := writeFile(t, "weak.mp3", audioFrames)
	storeTempo(t, weak, 133.7, models.DefaultBPMMinConfidence-0.01)
	if got := TempoTagValue(weak, writeTempo); got != "" {
		t.Fatalf("a result below the default floor was written: %q", got)
	}

	// No measurable tempo is stored as zero and is never written, whatever the floor.
	silent := writeFile(t, "silent.mp3", append([]byte{0}, audioFrames...))
	storeTempo(t, silent, 0, 0)
	if got := TempoTagValue(silent, models.TaggerSettings{BPMWrite: models.BPMWriteOverwrite, BPMMinConfidence: 0.0001}); got != "" {
		t.Fatalf("a zero tempo was written: %q", got)
	}
}

// Tags written since the analysis change the file's stamp, not its audio: the value
// still stands. Replaced audio does not get the old file's tempo.
func TestTempoTagValueChecksTheAudioWhenTheStampMoved(t *testing.T) {
	dbForCache(t)
	path := writeFile(t, "a.mp3", audioFrames)
	storeTempo(t, path, 160, 0.9)

	retagged := append(id3v2Tag(make([]byte, 200), false), audioFrames...)
	if err := os.WriteFile(path, retagged, 0o644); err != nil {
		t.Fatal(err)
	}
	if got := TempoTagValue(path, writeTempo); got != "160" {
		t.Fatalf("after a tag write: TempoTagValue = %q, want 160", got)
	}
	// Confirmed once, then re-stamped: the next tagging run must not hash the file
	// again to reach the same answer.
	var row models.TrackTempo
	cacheDB.Where("path = ?", path).First(&row)
	info, _ := os.Stat(path)
	if row.Size != info.Size() || row.ModTime == nil || !row.ModTime.Equal(info.ModTime()) {
		t.Errorf("stamp not moved onto the file: stored %d/%v, file %d/%v", row.Size, row.ModTime, info.Size(), info.ModTime())
	}

	replaced := append(append([]byte(nil), audioFrames...), 0x01, 0x02)
	if err := os.WriteFile(path, replaced, 0o644); err != nil {
		t.Fatal(err)
	}
	if got := TempoTagValue(path, writeTempo); got != "" {
		t.Fatalf("after the audio was replaced: TempoTagValue = %q, want nothing until it is re-analysed", got)
	}
}

// --- the desired-tag maps ---------------------------------------------------

// No value, no key — which is what keeps remove_values from clearing a BPM another
// tool wrote.
func TestBuildersOnlyWantBPMWhenThereIsOne(t *testing.T) {
	for name, build := range map[string]func(models.FileTags) map[string][]string{
		"flac": buildFLACDesiredTags, "mp3": buildMP3DesiredTags,
	} {
		if _, wanted := build(models.FileTags{Title: "x"})[TempoTagKey]; wanted {
			t.Errorf("%s: BPM wanted with no value", name)
		}
		if got := build(models.FileTags{BPM: "128"})[TempoTagKey]; len(got) != 1 || got[0] != "128" {
			t.Errorf("%s: BPM = %v, want [128]", name, got)
		}
	}
	if frame := id3TextFrameForKey[TempoTagKey]; frame != "TBPM" {
		t.Fatalf("MP3 BPM goes to %q, want the standard TBPM frame", frame)
	}
}

func TestApplyTempoPolicy(t *testing.T) {
	fill := models.TaggerSettings{BPMWrite: models.BPMWriteFillEmpty}
	cases := []struct {
		name     string
		tagger   models.TaggerSettings
		existing []string
		kept     bool
	}{
		{"fill: file has none", fill, nil, true},
		{"fill: file has one", fill, []string{"100"}, false},
		{"fill: zero means not analysed", fill, []string{"0"}, true},
		{"fill: blank is empty", fill, []string{" "}, true},
		{"overwrite: file has one", writeTempo, []string{"100"}, true},
	}
	for _, tc := range cases {
		desired := map[string][]string{TempoTagKey: {"128"}, "TITLE": {"x"}}
		existing := map[string][]string{}
		if tc.existing != nil {
			existing[TempoTagKey] = tc.existing
		}
		applyTempoPolicy(desired, existing, tc.tagger)
		if _, kept := desired[TempoTagKey]; kept != tc.kept {
			t.Errorf("%s: BPM kept = %v, want %v", tc.name, kept, tc.kept)
		}
		if _, ok := desired["TITLE"]; !ok {
			t.Errorf("%s: another tag was dropped", tc.name)
		}
	}
}

// --- round trips on real files ----------------------------------------------

func tempoFixture(t *testing.T, ext string) string {
	t.Helper()
	if ext == ".flac" {
		requireTool(t, "metaflac")
	}
	return synthAudio(t, ext)
}

func readBPM(t *testing.T, path string) []string {
	t.Helper()
	var tags map[string][]string
	var err error
	if filepath.Ext(path) == ".flac" {
		tags, err = getFlacTagsMap(path)
	} else {
		tags, err = GetMP3Tags(path)
	}
	if err != nil {
		t.Fatal(err)
	}
	return tags[TempoTagKey]
}

// Set it, read it back, and the next write has nothing to do — the property every
// tag in docs/tagging.md is held to.
func TestBPMRoundTrip(t *testing.T) {
	for _, ext := range []string{".flac", ".mp3"} {
		t.Run(ext, func(t *testing.T) {
			path := tempoFixture(t, ext)
			meta := models.FileTags{Title: "Stayin’ Alive", BPM: "105"}

			unchanged, _, changes, err := SetFileTags(path, meta, writeTempo)
			if err != nil || unchanged {
				t.Fatalf("first write: unchanged=%v err=%v", unchanged, err)
			}
			if got := readBPM(t, path); len(got) != 1 || got[0] != "105" {
				t.Fatalf("BPM read back as %v", got)
			}
			found := false
			for _, change := range changes {
				found = found || (change.Field == TempoTagKey && change.New == "105")
			}
			if !found {
				t.Fatalf("the reported changes do not include BPM: %+v", changes)
			}

			unchanged, _, _, err = SetFileTags(path, meta, writeTempo)
			if err != nil || !unchanged {
				t.Fatalf("second write: unchanged=%v err=%v — BPM does not converge", unchanged, err)
			}
		})
	}
}

// On MP3 the value is in the TBPM frame itself, where every other reader looks — not
// in a TXXX frame described "BPM".
func TestMP3BPMIsTheStandardFrame(t *testing.T) {
	path := tempoFixture(t, ".mp3")
	if _, _, _, err := SetMP3Tags(path, models.FileTags{BPM: "174"}, writeTempo); err != nil {
		t.Fatal(err)
	}
	tag, err := id3v2.Open(path, id3v2.Options{Parse: true})
	if err != nil {
		t.Fatal(err)
	}
	defer tag.Close()
	if got := tag.GetTextFrame("TBPM").Text; got != "174" {
		t.Fatalf("TBPM = %q, want 174", got)
	}
	for _, frame := range tag.GetFrames(tag.CommonID("User defined text information frame")) {
		if udf, ok := frame.(id3v2.UserDefinedTextFrame); ok && udf.Description == TempoTagKey {
			t.Fatal("BPM was also written as a TXXX frame")
		}
	}
}

// Fill-empty leaves another tool's BPM alone; overwrite replaces it; and with
// remove_values on, having nothing to write never clears it.
func TestBPMWriteModesOnRealFiles(t *testing.T) {
	for _, ext := range []string{".flac", ".mp3"} {
		t.Run(ext, func(t *testing.T) {
			path := tempoFixture(t, ext)
			if _, _, _, err := SetFileTags(path, models.FileTags{BPM: "100"}, writeTempo); err != nil {
				t.Fatal(err)
			}

			fill := models.TaggerSettings{BPMWrite: models.BPMWriteFillEmpty}
			unchanged, _, _, err := SetFileTags(path, models.FileTags{BPM: "128"}, fill)
			if err != nil || !unchanged {
				t.Fatalf("fill-empty over an existing BPM: unchanged=%v err=%v", unchanged, err)
			}
			if got := readBPM(t, path); len(got) != 1 || got[0] != "100" {
				t.Fatalf("fill-empty replaced the existing BPM: %v", got)
			}

			removing := models.TaggerSettings{RemoveValues: true}
			if _, _, _, err := SetFileTags(path, models.FileTags{Title: "x"}, removing); err != nil {
				t.Fatal(err)
			}
			if got := readBPM(t, path); len(got) != 1 || got[0] != "100" {
				t.Fatalf("remove_values cleared a BPM nobody asked about: %v", got)
			}

			if _, _, _, err := SetFileTags(path, models.FileTags{Title: "x", BPM: "128"}, writeTempo); err != nil {
				t.Fatal(err)
			}
			if got := readBPM(t, path); len(got) != 1 || got[0] != "128" {
				t.Fatalf("overwrite left %v, want 128", got)
			}
		})
	}
}

// The preview shows the BPM a write would make, under the same rules.
func TestDiffFileTagsPreviewsBPM(t *testing.T) {
	path := tempoFixture(t, ".mp3")
	entries, err := DiffFileTags(path, models.FileTags{BPM: "160"}, writeTempo)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.Key == TempoTagKey {
			if !entry.Changed || entry.Desired != "160" {
				t.Fatalf("BPM preview = %+v", entry)
			}
			return
		}
	}
	t.Fatalf("no BPM row in the preview: %+v", entries)
}

// TestRestampTempoAfterWrite: Autotaggerr's own tag write keeps the stored tempo
// matching the file, so the BPM it just wrote does not cost a full audio hash on
// every tagging run after.
func TestRestampTempoAfterWrite(t *testing.T) {
	dbForCache(t)
	path := writeFile(t, "a.mp3", audioFrames)
	storeTempo(t, path, 120, 0.9)
	later := time.Now().Add(time.Hour).Truncate(time.Second)
	if err := os.WriteFile(path, append(id3v2Tag(make([]byte, 50), false), audioFrames...), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, later, later); err != nil {
		t.Fatal(err)
	}

	RestampTempoAfterWrite(path)

	var row models.TrackTempo
	cacheDB.Where("path = ?", path).First(&row)
	info, _ := os.Stat(path)
	if row.Size != info.Size() || row.ModTime == nil || !row.ModTime.Equal(info.ModTime()) {
		t.Errorf("stored %d/%v, file %d/%v", row.Size, row.ModTime, info.Size(), info.ModTime())
	}
}
