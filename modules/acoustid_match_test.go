package modules

import "testing"

func candidate(score float64, release, title string, year, tracks int) AcoustIDCandidate {
	return AcoustIDCandidate{
		Score: score, RecordingMBID: "rec-" + release, ReleaseMBID: release,
		ReleaseTitle: title, ReleaseYear: year, TrackCount: tracks,
		Title: "Dreams", Artist: "Fleetwood Mac",
	}
}

// TestFailsClosedBelowTheFloor is the rule that matters most: a weak fingerprint
// match is dropped entirely rather than offered. A file left unmatched costs one
// manual attach; a confident wrong match writes the wrong album into the tags and
// then self-heals into looking correct forever after.
func TestFailsClosedBelowTheFloor(t *testing.T) {
	weak := []AcoustIDCandidate{candidate(0.2, "rel-1", "Rumours", 1977, 11)}
	if got := PickAcoustIDMatch(weak, MatchHint{Album: "Rumours", Year: 1977, Tracks: 11}); len(got) != 0 {
		t.Errorf("a 0.2 fingerprint score survived a perfect folder match: %+v", got)
	}

	// Folder agreement must never rescue a weak fingerprint. The folder says which
	// release a recording is from; it is no evidence that the audio matches at all.
	for _, score := range []float64{0.0, 0.1, 0.3, AcoustIDConfidenceFloor - 0.01} {
		got := PickAcoustIDMatch(
			[]AcoustIDCandidate{candidate(score, "rel-1", "Rumours", 1977, 11)},
			MatchHint{Album: "Rumours", Year: 1977, Tracks: 11},
		)
		if len(got) != 0 {
			t.Errorf("score %.2f survived on folder evidence alone: %.2f", score, got[0].Confidence)
		}
	}
}

// TestStrongFingerprintWithAWrongAlbumSurvives: the folder may simply be named
// differently (a compilation, a rip named by the artist). Such a candidate ranks
// below one that agrees, but it is still offered — the human decides.
func TestStrongFingerprintWithAWrongAlbumSurvives(t *testing.T) {
	got := PickAcoustIDMatch(
		[]AcoustIDCandidate{candidate(0.97, "rel-comp", "Greatest Hits", 1988, 16)},
		MatchHint{Album: "Rumours", Year: 1977, Tracks: 11},
	)
	if len(got) != 1 {
		t.Fatalf("a 0.97 audio match was dropped for disagreeing with the folder: %+v", got)
	}
	// Disagreement costs it confidence, so an agreeing candidate would outrank it.
	if got[0].Confidence >= 0.97 {
		t.Errorf("a disagreeing folder did not cost the candidate anything: %.2f", got[0].Confidence)
	}
}

// TestFolderPicksTheEdition: AcoustID identifies the *recording*, which sits on a
// dozen releases at the same fingerprint score. The folder is the only evidence of
// which one this file is, so it must decide the order.
func TestFolderPicksTheEdition(t *testing.T) {
	candidates := []AcoustIDCandidate{
		candidate(0.95, "rel-comp", "Greatest Hits", 1988, 16),
		candidate(0.95, "rel-77", "Rumours", 1977, 11),
		candidate(0.95, "rel-single", "Dreams", 1977, 2),
	}
	got := PickAcoustIDMatch(candidates, MatchHint{Album: "Rumours", Year: 1977, Tracks: 11})

	if len(got) == 0 {
		t.Fatal("no candidates survived")
	}
	if got[0].ReleaseMBID != "rel-77" {
		t.Errorf("top match = %q (%.2f), want rel-77: %+v", got[0].ReleaseMBID, got[0].Confidence, got)
	}
	// The reasons are shown in the UI, so a suggestion can be judged rather than
	// merely trusted.
	if len(got[0].Reasons) == 0 {
		t.Error("the top match explained nothing about why it ranked first")
	}
}

// TestRemasterVsOriginalUsesTheYear: same album title, two pressings — exactly the
// case pass C made visible. The folder's year is the discriminator.
func TestRemasterVsOriginalUsesTheYear(t *testing.T) {
	candidates := []AcoustIDCandidate{
		candidate(0.9, "rel-77", "Rumours", 1977, 11),
		candidate(0.9, "rel-17", "Rumours (2017 Remaster)", 2017, 11),
	}

	if got := PickAcoustIDMatch(candidates, MatchHint{Album: "Rumours", Year: 2017}); got[0].ReleaseMBID != "rel-17" {
		t.Errorf("2017 folder chose %q", got[0].ReleaseMBID)
	}
	if got := PickAcoustIDMatch(candidates, MatchHint{Album: "Rumours", Year: 1977}); got[0].ReleaseMBID != "rel-77" {
		t.Errorf("1977 folder chose %q", got[0].ReleaseMBID)
	}
}

// TestSongOnlyCandidateRanksLast: a recording with no release still identifies the
// song, which is useful — the user picks the release — but it must never outrank a
// candidate that names one.
func TestSongOnlyCandidateRanksLast(t *testing.T) {
	songOnly := AcoustIDCandidate{Score: 0.95, RecordingMBID: "rec-x", Title: "Dreams"}
	got := PickAcoustIDMatch(
		[]AcoustIDCandidate{songOnly, candidate(0.9, "rel-77", "Rumours", 1977, 11)},
		MatchHint{Album: "Rumours", Year: 1977},
	)
	if len(got) != 2 {
		t.Fatalf("got %d candidates, want 2", len(got))
	}
	if got[0].ReleaseMBID != "rel-77" {
		t.Errorf("song-only candidate outranked a full match: %+v", got)
	}
}

// TestDuplicateCandidatesCollapse: AcoustID repeats a (recording, release) pair
// across results; the picker must not offer the same choice twice.
func TestDuplicateCandidatesCollapse(t *testing.T) {
	c := candidate(0.9, "rel-77", "Rumours", 1977, 11)
	if got := PickAcoustIDMatch([]AcoustIDCandidate{c, c, c}, MatchHint{}); len(got) != 1 {
		t.Errorf("got %d candidates for one repeated pair", len(got))
	}
}

// TestRankingIsDeterministic: equal-confidence candidates must not reorder between
// calls, or the top suggestion changes under the user for no reason.
func TestRankingIsDeterministic(t *testing.T) {
	candidates := []AcoustIDCandidate{
		candidate(0.9, "rel-b", "Something Else", 1990, 10),
		candidate(0.9, "rel-a", "Something Else", 1990, 10),
	}
	first := PickAcoustIDMatch(candidates, MatchHint{})
	for i := 0; i < 5; i++ {
		again := PickAcoustIDMatch(candidates, MatchHint{})
		for j := range first {
			if first[j].ReleaseMBID != again[j].ReleaseMBID {
				t.Fatalf("ranking reordered between calls: %v vs %v", first, again)
			}
		}
	}
}

// TestNoHintStillRanksByFingerprint: a file outside the library layout has no
// folder evidence, so the audio score is all there is.
func TestNoHintStillRanksByFingerprint(t *testing.T) {
	got := PickAcoustIDMatch([]AcoustIDCandidate{
		candidate(0.7, "rel-low", "Whatever", 0, 0),
		candidate(0.98, "rel-high", "Whatever", 0, 0),
	}, MatchHint{})

	if len(got) != 2 || got[0].ReleaseMBID != "rel-high" {
		t.Errorf("ranking without a hint = %+v", got)
	}
}

func TestHintFromPath(t *testing.T) {
	tests := []struct {
		path   string
		artist string
		album  string
		year   int
	}{
		{"/music/Fleetwood Mac/Rumours (1977)/01 Dreams.flac", "Fleetwood Mac", "Rumours", 1977},
		{"/music/Fleetwood Mac/Rumours (1977)/CD2/01 Dreams.flac", "Fleetwood Mac", "Rumours", 1977},
		{`C:\music\Fleetwood Mac\Rumours\01 Dreams.flac`, "Fleetwood Mac", "Rumours", 0},
		{"/music/Rumours/01 Dreams.flac", "music", "Rumours", 0},
		{"track.flac", "", "", 0},
	}
	for _, tt := range tests {
		got := HintFromPath(tt.path)
		if got.Artist != tt.artist || got.Album != tt.album || got.Year != tt.year {
			t.Errorf("HintFromPath(%q) = %+v, want %s / %s / %d", tt.path, got, tt.artist, tt.album, tt.year)
		}
	}
}

// TestTitleSimilarityIgnoresEditionNoise: a folder is named "Rumours", the release
// is "Rumours (Deluxe Edition)". Treating those as unrelated would drop the right
// answer to the bottom.
func TestTitleSimilarityIgnoresEditionNoise(t *testing.T) {
	if got := titleSimilarity("Rumours", "Rumours (Deluxe Edition)"); got < 0.95 {
		t.Errorf("similarity with edition noise = %.2f, want ~1", got)
	}
	if got := titleSimilarity("Rumours", "Tusk"); got != 0 {
		t.Errorf("unrelated titles = %.2f, want 0", got)
	}
	if got := titleSimilarity("The Wall", "the wall!"); got < 0.95 {
		t.Errorf("case/punctuation changed similarity: %.2f", got)
	}
}

// TestFlattenAcoustIDPairsRecordingsWithReleases: a file is attached to a
// (recording, release) pair, so the response has to be flattened to that shape —
// including the recording that names no release at all.
func TestFlattenAcoustIDPairsRecordingsWithReleases(t *testing.T) {
	var resp acoustidResponse
	resp.Results = append(resp.Results, struct {
		ID         string  `json:"id"`
		Score      float64 `json:"score"`
		Recordings []struct {
			ID       string `json:"id"`
			Title    string `json:"title"`
			Duration int    `json:"duration"`
			Artists  []struct {
				Name string `json:"name"`
			} `json:"artists"`
			Releases []struct {
				ID         string `json:"id"`
				Title      string `json:"title"`
				TrackCount int    `json:"track_count"`
				Date       struct {
					Year int `json:"year"`
				} `json:"date"`
			} `json:"releases"`
		} `json:"recordings"`
	}{})
	resp.Results[0].Score = 0.9

	rec := struct {
		ID       string `json:"id"`
		Title    string `json:"title"`
		Duration int    `json:"duration"`
		Artists  []struct {
			Name string `json:"name"`
		} `json:"artists"`
		Releases []struct {
			ID         string `json:"id"`
			Title      string `json:"title"`
			TrackCount int    `json:"track_count"`
			Date       struct {
				Year int `json:"year"`
			} `json:"date"`
		} `json:"releases"`
	}{ID: "rec-1", Title: "Dreams"}
	rec.Releases = append(rec.Releases, struct {
		ID         string `json:"id"`
		Title      string `json:"title"`
		TrackCount int    `json:"track_count"`
		Date       struct {
			Year int `json:"year"`
		} `json:"date"`
	}{ID: "rel-1", Title: "Rumours", TrackCount: 11})
	rec.Releases = append(rec.Releases, struct {
		ID         string `json:"id"`
		Title      string `json:"title"`
		TrackCount int    `json:"track_count"`
		Date       struct {
			Year int `json:"year"`
		} `json:"date"`
	}{ID: "rel-2", Title: "Greatest Hits", TrackCount: 16})
	resp.Results[0].Recordings = append(resp.Results[0].Recordings, rec)

	got := flattenAcoustID(resp)
	if len(got) != 2 {
		t.Fatalf("got %d candidates, want one per release", len(got))
	}
	if got[0].RecordingMBID != "rec-1" || got[0].ReleaseMBID != "rel-1" || got[0].Score != 0.9 {
		t.Errorf("candidate = %+v", got[0])
	}
}
