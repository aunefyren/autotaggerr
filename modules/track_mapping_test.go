package modules

import "testing"

// singleDisc / twoDisc build flattened tracklists the way ReleaseTracks would.
func singleDisc(n int) []ReleaseTrack {
	var tracks []ReleaseTrack
	for i := 1; i <= n; i++ {
		tracks = append(tracks, ReleaseTrack{
			TrackID:     string(rune('a'+i-1)) + "-id",
			RecordingID: string(rune('a'+i-1)) + "-rec",
			Position:    i,
			Number:      itoa(i),
			Medium:      1,
		})
	}
	return tracks
}

func twoDisc(first, second int) []ReleaseTrack {
	tracks := singleDisc(first)
	for i := 1; i <= second; i++ {
		tracks = append(tracks, ReleaseTrack{
			TrackID:  "d2-" + itoa(i),
			Position: i,
			Number:   itoa(i),
			Medium:   2,
		})
	}
	return tracks
}

func itoa(i int) string { return string(rune('0' + i)) }

// TestMapByLeadingTrackNumber is the common case: a tagged-by-hand folder where
// every filename starts with its track number.
func TestMapByLeadingTrackNumber(t *testing.T) {
	paths := []string{
		"/m/A/Album/03 - Third.flac",
		"/m/A/Album/01 - First.flac",
		"/m/A/Album/02. Second.mp3",
	}
	got := MapFilesToTracks(paths, singleDisc(3))

	// Input order is preserved: it is what the review table renders, and reordering
	// a mapping under the user is how the wrong track gets confirmed.
	want := []string{"c-id", "a-id", "b-id"}
	for i, id := range want {
		if got[i].TrackID != id {
			t.Errorf("paths[%d] = %q -> %q, want %q", i, paths[i], got[i].TrackID, id)
		}
		if got[i].How != MapByNumber {
			t.Errorf("paths[%d] how = %q, want %q", i, got[i].How, MapByNumber)
		}
	}
}

// TestMapDiscPrefixAcrossMedia: on a multi-disc release the track number alone is
// ambiguous (both discs have a track 1), so the disc must come from the filename.
func TestMapDiscPrefixAcrossMedia(t *testing.T) {
	paths := []string{"/m/A/Album/1-02 B.flac", "/m/A/Album/2-01 C.flac"}
	got := MapFilesToTracks(paths, twoDisc(3, 2))

	if got[0].TrackID != "b-id" || got[0].Medium != 1 {
		t.Errorf("disc-1 file mapped to %+v", got[0])
	}
	if got[1].TrackID != "d2-1" || got[1].Medium != 2 {
		t.Errorf("disc-2 file mapped to %+v", got[1])
	}
}

// TestMapDiscFromFolderName: many rips put the disc in the folder, not the file.
func TestMapDiscFromFolderName(t *testing.T) {
	paths := []string{"/m/A/Album/CD2/01 C.flac", "/m/A/Album/Disc 1/02 B.flac"}
	got := MapFilesToTracks(paths, twoDisc(3, 2))

	if got[0].TrackID != "d2-1" {
		t.Errorf("CD2 file mapped to %+v", got[0])
	}
	if got[1].TrackID != "b-id" {
		t.Errorf("Disc 1 file mapped to %+v", got[1])
	}
}

// TestAmbiguousMultiDiscFallsBackToOrder: numbered files with no disc information
// on a two-disc release are ambiguous. Mapping them by number anyway would look
// right and be wrong, so the strategy degrades to order — which the review step
// flags as the weaker signal.
func TestAmbiguousMultiDiscFallsBackToOrder(t *testing.T) {
	paths := []string{"/m/A/Album/01 a.flac", "/m/A/Album/02 b.flac"}
	got := MapFilesToTracks(paths, twoDisc(3, 2))

	for i := range got {
		if got[i].How != MapByOrder {
			t.Errorf("mapping[%d] how = %q, want %q", i, got[i].How, MapByOrder)
		}
	}
	if got[0].TrackID != "a-id" || got[1].TrackID != "b-id" {
		t.Errorf("order fallback = %+v", got)
	}
}

// TestUnnumberedFilesMapByOrder: filenames without numbers are the reason a review
// step is mandatory. They still get a proposal, sorted by path.
func TestUnnumberedFilesMapByOrder(t *testing.T) {
	paths := []string{"/m/A/Album/Zulu.flac", "/m/A/Album/Alpha.flac"}
	got := MapFilesToTracks(paths, singleDisc(3))

	if got[1].TrackID != "a-id" || got[1].How != MapByOrder {
		t.Errorf("Alpha (first alphabetically) mapped to %+v", got[1])
	}
	if got[0].TrackID != "b-id" {
		t.Errorf("Zulu mapped to %+v", got[0])
	}
}

// TestPartialNumbersDoNotMixStrategies: one un-numbered file among numbered ones
// means the filenames are not the reliable signal they looked like. Mixing the two
// strategies within one album is exactly how a plausible wrong mapping happens.
func TestPartialNumbersDoNotMixStrategies(t *testing.T) {
	paths := []string{"/m/A/Album/01 First.flac", "/m/A/Album/Bonus.flac"}
	got := MapFilesToTracks(paths, singleDisc(3))

	for i := range got {
		if got[i].How != MapByOrder {
			t.Errorf("mapping[%d] how = %q, want the order fallback", i, got[i].How)
		}
	}
}

// TestDuplicateNumbersFallBackToOrder: two files claiming track 1 cannot both be
// right, so the number strategy is abandoned rather than picking a winner.
func TestDuplicateNumbersFallBackToOrder(t *testing.T) {
	paths := []string{"/m/A/Album/01 a.flac", "/m/A/Album/01 b.flac"}
	got := MapFilesToTracks(paths, singleDisc(3))

	if got[0].How != MapByOrder || got[1].How != MapByOrder {
		t.Errorf("duplicate numbers did not fall back to order: %+v", got)
	}
}

// TestNumberOutsideTracklistFallsBackToOrder: a file numbered 12 against a 3-track
// release means the folder does not match the chosen release.
func TestNumberOutsideTracklistFallsBackToOrder(t *testing.T) {
	got := MapFilesToTracks([]string{"/m/A/Album/12 Late.flac"}, singleDisc(3))
	if got[0].How != MapByOrder {
		t.Errorf("out-of-range number = %+v", got[0])
	}
}

// TestMoreFilesThanTracksLeavesSurplusUnmapped: the surplus must be left for the
// user to resolve, never quietly pointed at the last track.
func TestMoreFilesThanTracksLeavesSurplusUnmapped(t *testing.T) {
	paths := []string{"/m/A/Album/x.flac", "/m/A/Album/y.flac", "/m/A/Album/z.flac"}
	got := MapFilesToTracks(paths, singleDisc(2))

	if got[2].TrackID != "" || got[2].How != MapUnmapped {
		t.Errorf("surplus file was mapped: %+v", got[2])
	}
}

// TestSingleDiscIgnoresDiscPrefix: "1-05" on a single-CD release still means track
// 5 — the prefix is how the ripper wrote it, not a second medium.
func TestSingleDiscIgnoresDiscPrefix(t *testing.T) {
	got := MapFilesToTracks([]string{"/m/A/Album/1-02 B.flac"}, singleDisc(3))
	if got[0].TrackID != "b-id" || got[0].How != MapByNumber {
		t.Errorf("mapping = %+v", got[0])
	}
}

func TestMapWithNoTracksOrNoFiles(t *testing.T) {
	if got := MapFilesToTracks([]string{"/m/a.flac"}, nil); len(got) != 1 || got[0].TrackID != "" {
		t.Errorf("no tracks = %+v", got)
	}
	if got := MapFilesToTracks(nil, singleDisc(2)); len(got) != 0 {
		t.Errorf("no files = %+v", got)
	}
}

// TestWindowsPathsMapToo: paths come from the index as the OS wrote them.
func TestWindowsPathsMapToo(t *testing.T) {
	got := MapFilesToTracks([]string{`C:\music\A\Album\02 - Second.flac`}, singleDisc(3))
	if got[0].TrackID != "b-id" || got[0].How != MapByNumber {
		t.Errorf("mapping = %+v", got[0])
	}
}

// withVideoDisc appends a video medium to an audio one, the shape of a CD+DVD
// edition (Frank Ocean's *Endless*: 19 audio, 22 videos).
func withVideoDisc(audio, video int) []ReleaseTrack {
	tracks := singleDisc(audio)
	for i := 1; i <= video; i++ {
		tracks = append(tracks, ReleaseTrack{
			TrackID:  "vid-" + itoa(i),
			Position: i,
			Number:   itoa(i),
			Medium:   2,
			Video:    true,
		})
	}
	return tracks
}

// TestMappingNeverProposesAVideoTrack: a bonus DVD is not a candidate pool for audio
// files. Beyond the obvious wrongness of pairing one, its presence used to wreck both
// strategies — the release looked multi-medium, so a bare "05" was ambiguous between
// the discs and the number strategy bailed, and the sort-order fallback then zipped
// the audio files against a tracklist twice the length.
func TestMappingNeverProposesAVideoTrack(t *testing.T) {
	paths := []string{
		"/m/A/Endless/01 First.flac",
		"/m/A/Endless/02 Second.flac",
		"/m/A/Endless/03 Third.flac",
	}
	got := MapFilesToTracks(paths, withVideoDisc(3, 4))

	// The audio disc maps by number, exactly as it would if the DVD were not there.
	want := []string{"a-id", "b-id", "c-id"}
	for i, id := range want {
		if got[i].TrackID != id {
			t.Errorf("paths[%d] -> %q, want %q", i, got[i].TrackID, id)
		}
		if got[i].How != MapByNumber {
			t.Errorf("paths[%d] how = %q, want %q — the DVD must not make this ambiguous", i, got[i].How, MapByNumber)
		}
	}
}

// TestMappingByOrderSkipsVideoTracks: the fallback zips against the tracklist, so a
// video medium sitting in it would shift every pairing after the audio ran out.
func TestMappingByOrderSkipsVideoTracks(t *testing.T) {
	paths := []string{
		"/m/A/Endless/First.flac",
		"/m/A/Endless/Second.flac",
		"/m/A/Endless/Third.flac",
	}
	got := MapFilesToTracks(paths, withVideoDisc(2, 4))

	if got[0].How != MapByOrder {
		t.Fatalf("expected the sort-order fallback, got %q", got[0].How)
	}
	// Two audio tracks for three files: the third is left unmapped rather than
	// pointed at the first video.
	if got[0].TrackID != "a-id" || got[1].TrackID != "b-id" {
		t.Errorf("mapped = %q, %q; want a-id, b-id", got[0].TrackID, got[1].TrackID)
	}
	if got[2].TrackID != "" || got[2].How != MapUnmapped {
		t.Errorf("third file = %q (%s), want unmapped — a video track is not a candidate",
			got[2].TrackID, got[2].How)
	}
}

// TestMappingAllVideoReleaseMapsNothing: a release with no audio at all has no
// candidates, and must say so rather than falling through to the videos.
func TestMappingAllVideoReleaseMapsNothing(t *testing.T) {
	got := MapFilesToTracks([]string{"/m/A/DVD/01 Clip.flac"}, withVideoDisc(0, 3))
	if got[0].How != MapUnmapped || got[0].TrackID != "" {
		t.Errorf("mapping = %+v, want unmapped", got[0])
	}
}
