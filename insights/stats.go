package insights

import (
	"sort"
	"strings"

	"github.com/aunefyren/autotaggerr/models"
	"github.com/aunefyren/autotaggerr/modules"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

// Bucket is one value of a breakdown: how many files have it, and how much space
// they take.
type Bucket struct {
	Key   string `json:"key"`
	Files int    `json:"files"`
	Bytes int64  `json:"bytes"`
}

// Coverage counts files by what is known about them. Every count is of files, out of
// Stats.Files (or Stats.Identified, for the ones that need an identity).
type Coverage struct {
	Unmatched int `json:"unmatched"`
	Errors    int `json:"errors"`
	Pinned    int `json:"pinned"`
	PinsHeld  int `json:"pins_held"`
	ISRC      int `json:"isrc"`
	// Artwork is identified files whose album (or release) has a front cover in the
	// local artwork cache. Covers are never embedded in files, so this is what the
	// browsing pages can show, not what a player reads.
	Artwork    int `json:"artwork"`
	ReplayGain int `json:"replaygain"`
	BPM        int `json:"bpm"`
}

// CatalogueGap compares what the catalogue wants with what is on disk. It is
// collection-wide: the collection tables are not split by library.
type CatalogueGap struct {
	// Wanted is monitored catalogue albums with no file on disk.
	Wanted int `json:"wanted"`
	// Incomplete is albums on disk with fewer tracks than the catalogue lists.
	Incomplete int `json:"incomplete"`
	// Unlisted is albums on disk that no manager or discography lists.
	Unlisted int `json:"unlisted"`
}

// Stats is the answer to GET /insights/stats.
type Stats struct {
	// Files is every indexed file in scope; Identified is the ones with an identity
	// (the disk view). Breakdowns that need a release count identified files only.
	Files      int   `json:"files"`
	Identified int   `json:"identified"`
	Bytes      int64 `json:"bytes"`
	// DurationMS is the summed track length of identified files whose release is
	// cached. Uncached is how many identified files that leaves out — every breakdown
	// read from a release misses them too.
	DurationMS int64 `json:"duration_ms"`
	Uncached   int   `json:"uncached"`

	Recordings int `json:"recordings"`
	Releases   int `json:"releases"`
	Albums     int `json:"albums"`
	Artists    int `json:"artists"`

	Formats      []Bucket `json:"formats"`
	ReleaseTypes []Bucket `json:"release_types"`
	Decades      []Bucket `json:"decades"`
	Genres       []Bucket `json:"genres"`
	Countries    []Bucket `json:"countries"`
	Labels       []Bucket `json:"labels"`

	Coverage  Coverage      `json:"coverage"`
	Catalogue *CatalogueGap `json:"catalogue,omitempty"`
}

// topBuckets is how many values a long breakdown keeps; the rest are summed into
// one "Other" bucket so the totals still add up.
const topBuckets = 12

// unknownKey is the bucket for files whose release does not say.
const unknownKey = "Unknown"

// releaseFacts is what Stats reads from one cached release, parsed once.
type releaseFacts struct {
	album, albumType, decade, genre, country, label, artist string
	// tracks by release-track ID, recordings by recording ID: a file is matched by
	// its track when it has one, its recording otherwise.
	tracks     map[string]trackFacts
	recordings map[string]trackFacts
}

type trackFacts struct {
	lengthMS int
	isrc     bool
}

func parseRelease(release models.MusicBrainzReleaseResponse) releaseFacts {
	facts := releaseFacts{
		album:      release.ReleaseGroup.ID,
		albumType:  release.ReleaseGroup.PrimaryType,
		country:    release.Country,
		tracks:     map[string]trackFacts{},
		recordings: map[string]trackFacts{},
	}
	date := release.ReleaseGroup.FirstReleaseDate
	if date == "" {
		date = release.Date
	}
	if len(date) >= 4 && date[:4] != "0000" {
		facts.decade = date[:3] + "0s"
	}
	// The album's strongest genre, so each file is counted once and the breakdown
	// adds up to the files it covers.
	best := 0
	for _, g := range release.ReleaseGroup.Genres {
		if g.Count > best {
			best, facts.genre = g.Count, g.Name
		}
	}
	for _, l := range release.LabelInfo {
		if l.Label.Name != "" {
			facts.label = l.Label.Name
			break
		}
	}
	credit := release.ReleaseGroup.ArtistCredit
	if len(credit) == 0 {
		credit = release.ArtistCredit
	}
	if len(credit) > 0 {
		facts.artist = credit[0].Artist.ID
	}
	for _, medium := range release.Media {
		for _, track := range medium.Tracks {
			length := track.Length
			if length == 0 {
				length = track.Recording.Length
			}
			t := trackFacts{lengthMS: length, isrc: len(track.Recording.ISRCs) > 0}
			facts.tracks[track.ID] = t
			facts.recordings[track.Recording.ID] = t
		}
	}
	return facts
}

// tally accumulates one breakdown.
type tally map[string]*Bucket

func (t tally) add(key string, size int64) {
	if key == "" {
		key = unknownKey
	}
	b, ok := t[key]
	if !ok {
		b = &Bucket{Key: key}
		t[key] = b
	}
	b.Files++
	b.Bytes += size
}

// byFiles returns the buckets largest first, keeping the top limit and summing the
// rest into "Other" (limit 0 keeps them all). Unknown always goes last.
func (t tally) byFiles(limit int) []Bucket {
	out := make([]Bucket, 0, len(t))
	var unknown *Bucket
	for _, b := range t {
		if b.Key == unknownKey {
			unknown = b
			continue
		}
		out = append(out, *b)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Files != out[j].Files {
			return out[i].Files > out[j].Files
		}
		return out[i].Key < out[j].Key
	})
	if limit > 0 && len(out) > limit {
		other := Bucket{Key: "Other"}
		for _, b := range out[limit:] {
			other.Files += b.Files
			other.Bytes += b.Bytes
		}
		out = append(out[:limit], other)
	}
	if unknown != nil {
		out = append(out, *unknown)
	}
	return out
}

// byKey returns the buckets in key order (decades read as a timeline), Unknown last.
func (t tally) byKey() []Bucket {
	out := t.byFiles(0)
	sort.SliceStable(out, func(i, j int) bool {
		if (out[i].Key == unknownKey) != (out[j].Key == unknownKey) {
			return out[j].Key == unknownKey
		}
		return out[i].Key < out[j].Key
	})
	return out
}

// LibraryStats describes the files in enabled libraries — one library's when library
// is set. Reads the index and the release cache only.
func LibraryStats(db *gorm.DB, library *uuid.UUID) (Stats, error) {
	scoped := func() *gorm.DB {
		q := db.Model(&models.LibraryItem{}).Scopes(models.InEnabledLibrary)
		if library != nil {
			q = q.Where("library_items.library_id = ?", *library)
		}
		return q
	}

	var items []models.LibraryItem
	if err := scoped().Select("id", "path", "size", "mb_release_id", "mb_recording_id", "mb_release_track_id",
		"status", "pinned", "pin_held", "last_error_transient").Find(&items).Error; err != nil {
		return Stats{}, err
	}
	var identified []uuid.UUID
	if err := scoped().Scopes(models.InDiskView).Pluck("library_items.id", &identified).Error; err != nil {
		return Stats{}, err
	}
	inDiskView := make(map[uuid.UUID]bool, len(identified))
	for _, id := range identified {
		inDiskView[id] = true
	}

	stats := Stats{Files: len(items), Identified: len(identified)}
	formats, types, decades, genres, countries, labels := tally{}, tally{}, tally{}, tally{}, tally{}, tally{}
	recordings, releases, albums, artists := map[string]bool{}, map[string]bool{}, map[string]bool{}, map[string]bool{}
	parsed := map[string]*releaseFacts{}
	covers, err := cachedCovers(db)
	if err != nil {
		return Stats{}, err
	}

	for _, item := range items {
		stats.Bytes += item.Size
		formats.add(fileFormat(item.Path), item.Size)
		switch item.Status {
		case models.LibraryItemStatusUnmatched:
			stats.Coverage.Unmatched++
		case models.LibraryItemStatusError:
			stats.Coverage.Errors++
		}
		if item.Pinned {
			stats.Coverage.Pinned++
		}
		if item.PinHeld {
			stats.Coverage.PinsHeld++
		}
		if !inDiskView[item.ID] {
			continue
		}

		releases[item.MBReleaseID] = true
		if item.MBRecordingID != "" {
			recordings[item.MBRecordingID] = true
		}
		facts, seen := parsed[item.MBReleaseID]
		if !seen {
			if release, ok := modules.CachedRelease(item.MBReleaseID); ok {
				f := parseRelease(release)
				facts = &f
			}
			parsed[item.MBReleaseID] = facts
		}
		if facts == nil {
			stats.Uncached++
			continue
		}
		if facts.album != "" {
			albums[facts.album] = true
		}
		if facts.artist != "" {
			artists[facts.artist] = true
		}
		types.add(facts.albumType, item.Size)
		decades.add(facts.decade, item.Size)
		genres.add(facts.genre, item.Size)
		countries.add(facts.country, item.Size)
		labels.add(facts.label, item.Size)

		if covers[facts.album] || covers[item.MBReleaseID] {
			stats.Coverage.Artwork++
		}

		track, ok := facts.tracks[item.MBReleaseTrackID]
		if !ok {
			track = facts.recordings[item.MBRecordingID]
		}
		stats.DurationMS += int64(track.lengthMS)
		if track.isrc {
			stats.Coverage.ISRC++
		}
	}

	stats.Recordings, stats.Releases, stats.Albums, stats.Artists = len(recordings), len(releases), len(albums), len(artists)
	stats.Formats = formats.byFiles(0)
	stats.ReleaseTypes = types.byFiles(0)
	stats.Decades = decades.byKey()
	stats.Genres = genres.byFiles(topBuckets)
	stats.Countries = countries.byFiles(topBuckets)
	stats.Labels = labels.byFiles(topBuckets)

	var n int64
	scoped().Where("library_items.path IN (?)",
		db.Model(&models.TrackLoudness{}).Select("path").Where("error = '' AND analyser <> ''")).Count(&n)
	stats.Coverage.ReplayGain = int(n)
	scoped().Where("library_items.path IN (?)",
		db.Model(&models.TrackTempo{}).Select("path").Where("bpm > 0")).Count(&n)
	stats.Coverage.BPM = int(n)

	if library == nil {
		gap, err := catalogueGap(db)
		if err != nil {
			return stats, err
		}
		stats.Catalogue = &gap
	}
	return stats, nil
}

// cachedCovers is the set of release-group and release MBIDs with a front cover image
// in the artwork cache. Keys are entity_mbid_kind_size (modules.artworkCacheKey); an
// entity name has no underscore and an MBID none either, so splitting is exact.
func cachedCovers(db *gorm.DB) (map[string]bool, error) {
	var keys []string
	err := db.Model(&models.ArtworkCacheEntry{}).Where("missing = ? AND key LIKE ?", false, "%front%").
		Pluck("key", &keys).Error
	out := map[string]bool{}
	for _, key := range keys {
		parts := strings.Split(key, "_")
		if len(parts) == 4 && parts[2] == modules.ArtworkKindFront &&
			(parts[0] == modules.ArtworkEntityReleaseGroup || parts[0] == modules.ArtworkEntityRelease) {
			out[parts[1]] = true
		}
	}
	return out, err
}

func catalogueGap(db *gorm.DB) (CatalogueGap, error) {
	var gap CatalogueGap
	count := func(where string, args ...any) (int, error) {
		var n int64
		err := db.Model(&models.CollectionReleaseGroup{}).Where(where, args...).Count(&n).Error
		return int(n), err
	}
	var err error
	if gap.Wanted, err = count("in_catalog = ? AND catalog_monitored = ? AND owned = ?", true, true, false); err != nil {
		return gap, err
	}
	if gap.Incomplete, err = count("owned = ? AND total_tracks > 0 AND owned_tracks < total_tracks", true); err != nil {
		return gap, err
	}
	gap.Unlisted, err = count("owned = ? AND in_catalog = ?", true, false)
	return gap, err
}
