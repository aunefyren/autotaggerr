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
