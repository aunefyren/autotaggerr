// Package insights answers questions about the library as a whole: which files
// are the same song held more than once, and what the library is made of.
//
// Nothing here writes to an audio file or calls MusicBrainz. Everything it reports
// comes from the file index (library_items), the collection tables and the release
// cache. The one exception is VerifyDuplicates, which reads the audio of duplicate
// candidates so it can tell whether two copies are identical. See docs/insights.md.
package insights

import (
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/aunefyren/autotaggerr/models"
	"github.com/aunefyren/autotaggerr/modules"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

// Level is how a group of files sharing a recording relates, from the most to the
// least actionable.
type Level string

const (
	// LevelIdentical is the same audio, byte for byte, twice in one album. One copy
	// can go.
	LevelIdentical Level = "identical"
	// LevelMixedFormat is the same recording in one album in two formats. With FLAC
	// and MP3 the only formats read today, that is a lossy copy beside the lossless one.
	LevelMixedFormat Level = "mixed_format"
	// LevelSameFormat is the same recording in one album twice in one format, with
	// different audio (or audio not checked yet): a re-rip, or another master.
	LevelSameFormat Level = "same_format"
	// LevelSingleOnAlbum is a single whose track is also on an album you hold, as the
	// same version. The album has it, so the single is the copy that can go — the case
	// people open this page for, so it is shown by default.
	LevelSingleOnAlbum Level = "single_on_album"
	// LevelAcrossReleases is the same recording on two different albums otherwise: a
	// compilation, an EP, another version's edition. Usually intended.
	LevelAcrossReleases Level = "across_releases"
)

// levelRank orders levels for picking a group's most actionable one.
var levelRank = map[Level]int{LevelIdentical: 0, LevelMixedFormat: 1, LevelSameFormat: 2, LevelSingleOnAlbum: 3, LevelAcrossReleases: 4}

// Levels lists every level in display order.
var Levels = []Level{LevelIdentical, LevelMixedFormat, LevelSameFormat, LevelSingleOnAlbum, LevelAcrossReleases}

// DuplicateOptions narrows what Duplicates reports.
type DuplicateOptions struct {
	// Library limits the report to one library's files. Nil means every enabled one.
	Library *uuid.UUID
	// AcrossLibraries compares files in different libraries. Off by default: a
	// lossless library with a lossy mirror beside it is a duplicate of every file,
	// on purpose.
	AcrossLibraries bool
	// IncludeAcrossReleases lists LevelAcrossReleases groups, which are usually
	// intended and outnumber everything else.
	IncludeAcrossReleases bool
	// IncludeCompilations lists across-release groups where one of the albums is a
	// compilation. Only read when IncludeAcrossReleases is set.
	IncludeCompilations bool
	// Limit and Offset page the groups (Limit 0: all). A large library has thousands
	// of groups, and the page draws a hundred at a time.
	Limit, Offset int
}

// DuplicateFile is one file in a group.
type DuplicateFile struct {
	// IDs as strings, not uuid.UUID: the JSON is the same, and a schema derived from
	// the type (the MCP tools') would otherwise describe a byte array.
	ItemID      string `json:"item_id"`
	LibraryID   string `json:"library_id"`
	LibraryName string `json:"library_name"`
	Path        string `json:"path"`
	// RelativePath is Path below its library's root, which is how the page shows it;
	// empty when the file is not under the root.
	RelativePath     string `json:"relative_path"`
	Format           string `json:"format"`
	Lossless         bool   `json:"lossless"`
	Size             int64  `json:"size"`
	ReleaseMBID      string `json:"release_mb_id"`
	ReleaseTitle     string `json:"release_title"`
	ReleaseGroupMBID string `json:"release_group_mb_id"`
	ReleaseType      string `json:"release_type"`
	// Version is the kind of version the copy is ("remix", "demo", "live" …), from its
	// edition's title or MusicBrainz comment, or its own track title on that release;
	// empty for the plain version. Copies of different versions are never compared as
	// duplicates of each other (see versionOf).
	Version string `json:"version,omitempty"`
	// AudioHash is set only when it is current — taken from the file as it is now.
	AudioHash string `json:"audio_hash,omitempty"`
}

// DuplicateGroup is every file of one recording that the options compare.
type DuplicateGroup struct {
	RecordingMBID string          `json:"recording_mb_id"`
	Title         string          `json:"title"`
	Artist        string          `json:"artist"`
	Level         Level           `json:"level"`
	Files         []DuplicateFile `json:"files"`
	// AudioUnchecked marks a same-format group whose audio has not been hashed, so it
	// may yet turn out to be identical. Never set on other levels: two formats cannot
	// share a hash, and an identical group has been checked by definition.
	AudioUnchecked bool `json:"audio_unchecked"`
	// IdenticalAudio marks an across-release group where two of the copies are the
	// same audio — the single that is the album track, not an edit of it.
	IdenticalAudio bool `json:"identical_audio"`
	// Compilation marks a group where one of the albums is a compilation.
	Compilation bool `json:"compilation"`
	// Libraries is how many libraries the group's files are in.
	Libraries int `json:"libraries"`
}

// DuplicatesReport is the answer to GET /insights/duplicates.
type DuplicatesReport struct {
	Groups []DuplicateGroup `json:"groups"`
	// Matched is how many groups pass the filters, before paging.
	Matched int `json:"matched"`
	// Counts is every group by level, including the ones the options hide, so the
	// page can say what a filter is hiding.
	Counts map[Level]int `json:"counts"`
	// HiddenCompilations is how many across-release groups IncludeCompilations hides.
	HiddenCompilations int `json:"hidden_compilations"`
	// Unchecked is how many files VerifyDuplicates would hash: candidates across
	// every library, whatever the options, because that is what the job covers.
	Unchecked int `json:"unchecked"`
}

// losslessFormats are the formats a lossy copy beside them is worse than.
var losslessFormats = map[string]bool{"FLAC": true, "ALAC": true, "WAV": true, "AIFF": true, "APE": true, "WV": true}

// fileFormat is the file's format as its extension names it, upper-cased.
func fileFormat(path string) string {
	return strings.ToUpper(strings.TrimPrefix(filepath.Ext(path), "."))
}

// hashCurrent reports whether the item's stored audio hash was taken from the file
// as the index last saw it. A size or mtime that has moved since — a tag write
// included — means the audio is not checked, not that it differs.
func hashCurrent(item models.LibraryItem) bool {
	return item.AudioHash != "" && item.AudioHashSize == item.Size &&
		item.ModTime != nil && item.AudioHashModTime != nil && item.AudioHashModTime.Equal(*item.ModTime)
}

// candidateItems loads every file in an enabled library that shares its recording
// with another such file. One query for the recording IDs, one for the rows.
func candidateItems(db *gorm.DB, library *uuid.UUID) ([]models.LibraryItem, error) {
	sub := db.Model(&models.LibraryItem{}).Scopes(models.InDiskView, models.InEnabledLibrary).
		Where("library_items.mb_recording_id <> ''")
	if library != nil {
		sub = sub.Where("library_items.library_id = ?", *library)
	}
	sub = sub.Group("library_items.mb_recording_id").Having("COUNT(*) > 1").Select("library_items.mb_recording_id")

	var items []models.LibraryItem
	q := db.Model(&models.LibraryItem{}).Scopes(models.InDiskView, models.InEnabledLibrary).
		Where("library_items.mb_recording_id IN (?)", sub)
	if library != nil {
		q = q.Where("library_items.library_id = ?", *library)
	}
	err := q.Order("library_items.path").Find(&items).Error
	return items, err
}

// releaseInfo is what a group needs to know about the release a file is on.
type releaseInfo struct {
	title       string
	group       string // release-group MBID, or the release's own when unknown
	primaryType string
	compilation bool
	// version is the edition's kind of version, from its title or disambiguation.
	version string
}

// versionWords names the kinds of version a title can mark, each with what it counts
// as. A remix edition and a demo edition of one album share the album's release group
// in MusicBrainz, and often the original recordings too — but someone who holds both
// holds them on purpose. "Version" alone is left out: "Album Version" is the original.
var versionWords = []struct {
	re   *regexp.Regexp
	kind string
}{
	{regexp.MustCompile(`\b(re-?mix(es|ed)?|mix)\b`), "remix"},
	{regexp.MustCompile(`\bdemos?\b`), "demo"},
	{regexp.MustCompile(`\blive\b`), "live"},
	{regexp.MustCompile(`\b(acoustic|unplugged|stripped)\b`), "acoustic"},
	{regexp.MustCompile(`\binstrumentals?\b`), "instrumental"},
	{regexp.MustCompile(`\ba ?cappella\b`), "a cappella"},
	{regexp.MustCompile(`\bkaraoke\b`), "karaoke"},
	{regexp.MustCompile(`\b(radio |single |short )?edit\b`), "edit"},
}

// bracketed is the parenthesised and bracketed parts of a title: where a version is
// marked ("Song (Demo)", "Album [Remixes]"), as opposed to a title that happens to
// contain the word ("Live Forever").
var bracketed = regexp.MustCompile(`[(\[]([^)\]]*)[)\]]`)

// versionKind is the kind of version text marks, or "". whole reads all of it (a
// MusicBrainz disambiguation is nothing but such a note); otherwise only its
// bracketed parts count.
func versionKind(text string, whole bool) string {
	text = strings.ToLower(text)
	parts := []string{text}
	if !whole {
		parts = parts[:0]
		for _, m := range bracketed.FindAllStringSubmatch(text, -1) {
			parts = append(parts, m[1])
		}
	}
	for _, part := range parts {
		for _, w := range versionWords {
			if w.re.MatchString(part) {
				return w.kind
			}
		}
	}
	return ""
}

// releaseInfos resolves each release to its album, from the collection tables first
// (always present for an owned file) and the release cache second.
func releaseInfos(db *gorm.DB, releaseIDs []string) map[string]releaseInfo {
	out := make(map[string]releaseInfo, len(releaseIDs))
	if len(releaseIDs) == 0 {
		return out
	}

	// Chunked: one bound variable per ID, and SQLite caps how many a statement takes.
	var releases []models.CollectionRelease
	for _, ids := range chunk(releaseIDs, 500) {
		var part []models.CollectionRelease
		db.Where("mb_id IN ?", ids).Find(&part)
		releases = append(releases, part...)
	}
	groupIDs := make([]string, 0, len(releases))
	for _, r := range releases {
		out[r.MBID] = releaseInfo{title: r.Title, group: r.ReleaseGroupMBID,
			version: firstNonEmpty(versionKind(r.Title, false), versionKind(r.Disambiguation, true))}
		groupIDs = append(groupIDs, r.ReleaseGroupMBID)
	}
	var groups []models.CollectionReleaseGroup
	for _, ids := range chunk(groupIDs, 500) {
		var part []models.CollectionReleaseGroup
		db.Where("mb_id IN ?", ids).Find(&part)
		groups = append(groups, part...)
	}
	byGroup := make(map[string]models.CollectionReleaseGroup, len(groups))
	for _, g := range groups {
		byGroup[g.MBID] = g
	}

	for _, id := range releaseIDs {
		info, known := out[id]
		if g, ok := byGroup[info.group]; known && ok {
			info.primaryType = g.PrimaryType
			info.compilation = strings.Contains(strings.ToLower(g.SecondaryTypes), "compilation")
		}
		if cached, ok := modules.CachedRelease(id); ok {
			if info.title == "" {
				info.title = cached.Title
			}
			if info.version == "" {
				info.version = firstNonEmpty(versionKind(cached.Title, false), versionKind(cached.Disambiguation, true))
			}
			if info.group == "" {
				info.group = cached.ReleaseGroup.ID
			}
			if info.primaryType == "" {
				info.primaryType = cached.ReleaseGroup.PrimaryType
			}
			for _, t := range cached.ReleaseGroup.SecondaryTypes {
				if strings.EqualFold(t, "compilation") {
					info.compilation = true
				}
			}
		}
		if info.group == "" {
			info.group = "release:" + id
		}
		out[id] = info
	}
	return out
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

// trackVersion is the kind of version a file's own track title marks on its release
// ("Song (Demo)") — MusicBrainz sometimes credits a version to the plain recording, and
// the track title is then the only place that says so. "" when the release is not
// cached or the title marks none.
func trackVersion(releaseID, trackID string) string {
	cached, ok := modules.CachedRelease(releaseID)
	if !ok || trackID == "" {
		return ""
	}
	for _, medium := range cached.Media {
		for _, track := range medium.Tracks {
			if track.ID == trackID {
				return versionKind(track.Title, false)
			}
		}
	}
	return ""
}

// albumKey is what copies are compared within: the album, and the version. Two
// copies on one album in different versions are on different "albums" here.
func albumKey(f DuplicateFile) string {
	return f.ReleaseGroupMBID + "|" + f.Version
}

// recordingNames reads a recording's title and credited artist from the cached
// release a file is on. Empty when the release is not cached.
func recordingNames(releaseID, recordingID string) (title, artist string) {
	cached, ok := modules.CachedRelease(releaseID)
	if !ok {
		return "", ""
	}
	for _, medium := range cached.Media {
		for _, track := range medium.Tracks {
			if track.Recording.ID != recordingID {
				continue
			}
			credit := track.Recording.ArtistCredit
			if len(track.ArtistCredit) > 0 {
				credit = track.ArtistCredit
			}
			var b strings.Builder
			for _, c := range credit {
				b.WriteString(c.Name)
				b.WriteString(c.Joinphrase)
			}
			title = track.Title
			if title == "" {
				title = track.Recording.Title
			}
			return title, b.String()
		}
	}
	return "", ""
}

// libraryRows maps library IDs to their rows (name and root).
func libraryRows(db *gorm.DB) map[uuid.UUID]models.Library {
	var libraries []models.Library
	db.Select("id", "name", "path").Find(&libraries)
	out := make(map[uuid.UUID]models.Library, len(libraries))
	for _, l := range libraries {
		out[l.ID] = l
	}
	return out
}

// relativeTo is path below root, or "" when it is not under it.
func relativeTo(root, path string) string {
	if root == "" {
		return ""
	}
	rel, err := filepath.Rel(root, path)
	if err != nil || rel == "." || strings.HasPrefix(rel, "..") {
		return ""
	}
	return filepath.ToSlash(rel)
}

// classify decides a group's level from its files. Files are compared only with
// files on the same album (release group): the same recording on two albums is
// LevelAcrossReleases however alike the copies are, because deleting either loses
// an album. Within an album the most actionable pair decides.
func classify(group *DuplicateGroup) {
	byAlbum := map[string][]DuplicateFile{}
	for _, f := range group.Files {
		byAlbum[albumKey(f)] = append(byAlbum[albumKey(f)], f)
	}

	best := LevelAcrossReleases
	unchecked := false
	for _, files := range byAlbum {
		for i := 0; i < len(files); i++ {
			for j := i + 1; j < len(files); j++ {
				a, b := files[i], files[j]
				level := LevelSameFormat
				switch {
				case a.AudioHash != "" && a.AudioHash == b.AudioHash:
					level = LevelIdentical
				case a.Format != b.Format:
					level = LevelMixedFormat
				case a.AudioHash == "" || b.AudioHash == "":
					unchecked = true
				}
				if levelRank[level] < levelRank[best] {
					best = level
				}
			}
		}
	}
	if best == LevelAcrossReleases && singleOnAlbum(group.Files) {
		best = LevelSingleOnAlbum
	}
	group.Level = best
	group.AudioUnchecked = best == LevelSameFormat && unchecked

	if best == LevelAcrossReleases || best == LevelSingleOnAlbum {
		seen := map[string]bool{}
		for _, f := range group.Files {
			if f.AudioHash == "" {
				continue
			}
			if seen[f.AudioHash] {
				group.IdenticalAudio = true
			}
			seen[f.AudioHash] = true
		}
	}
}

// singleOnAlbum reports a copy on a single beside a copy of the same version on an
// album. A version marked on either side (a radio edit, a remix single) is not the
// album's track, so it does not count.
func singleOnAlbum(files []DuplicateFile) bool {
	for _, single := range files {
		if !strings.EqualFold(single.ReleaseType, "Single") {
			continue
		}
		for _, album := range files {
			if strings.EqualFold(album.ReleaseType, "Album") && album.Version == single.Version &&
				album.ReleaseGroupMBID != single.ReleaseGroupMBID {
				return true
			}
		}
	}
	return false
}

// groupItems builds the groups: one per recording, or one per recording and library
// when files in different libraries are not compared. A group needs two files.
func groupItems(db *gorm.DB, items []models.LibraryItem, acrossLibraries bool) []DuplicateGroup {
	releaseIDs := map[string]bool{}
	for _, item := range items {
		releaseIDs[item.MBReleaseID] = true
	}
	ids := make([]string, 0, len(releaseIDs))
	for id := range releaseIDs {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	releases := releaseInfos(db, ids)
	libraries := libraryRows(db)

	type key struct {
		recording string
		library   uuid.UUID
	}
	buckets := map[key][]models.LibraryItem{}
	var order []key
	for _, item := range items {
		k := key{recording: item.MBRecordingID}
		if !acrossLibraries {
			k.library = item.LibraryID
		}
		if _, ok := buckets[k]; !ok {
			order = append(order, k)
		}
		buckets[k] = append(buckets[k], item)
	}

	var groups []DuplicateGroup
	for _, k := range order {
		members := buckets[k]
		if len(members) < 2 {
			continue
		}
		group := DuplicateGroup{RecordingMBID: k.recording}
		inLibrary := map[uuid.UUID]bool{}
		for _, item := range members {
			info := releases[item.MBReleaseID]
			format := fileFormat(item.Path)
			file := DuplicateFile{
				ItemID:           item.ID.String(),
				LibraryID:        item.LibraryID.String(),
				LibraryName:      libraries[item.LibraryID].Name,
				Path:             item.Path,
				RelativePath:     relativeTo(libraries[item.LibraryID].Path, item.Path),
				Format:           format,
				Lossless:         losslessFormats[format],
				Size:             item.Size,
				ReleaseMBID:      item.MBReleaseID,
				ReleaseTitle:     info.title,
				ReleaseGroupMBID: info.group,
				ReleaseType:      info.primaryType,
				Version:          firstNonEmpty(info.version, trackVersion(item.MBReleaseID, item.MBReleaseTrackID)),
			}
			if hashCurrent(item) {
				file.AudioHash = item.AudioHash
			}
			if info.compilation {
				group.Compilation = true
			}
			inLibrary[item.LibraryID] = true
			if group.Title == "" {
				group.Title, group.Artist = recordingNames(item.MBReleaseID, item.MBRecordingID)
			}
			group.Files = append(group.Files, file)
		}
		group.Libraries = len(inLibrary)
		classify(&group)
		groups = append(groups, group)
	}
	return groups
}

// Duplicates reports the files that share a recording, grouped and classified.
// Groups are ordered by level, then by artist and title.
func Duplicates(db *gorm.DB, opts DuplicateOptions) (DuplicatesReport, error) {
	report := DuplicatesReport{Groups: []DuplicateGroup{}, Counts: map[Level]int{}}
	for _, level := range Levels {
		report.Counts[level] = 0
	}

	items, err := candidateItems(db, opts.Library)
	if err != nil {
		return report, err
	}
	groups := groupItems(db, items, opts.AcrossLibraries)
	for _, group := range groups {
		report.Counts[group.Level]++
		if group.Level == LevelAcrossReleases {
			if !opts.IncludeAcrossReleases {
				continue
			}
			if group.Compilation && !opts.IncludeCompilations {
				report.HiddenCompilations++
				continue
			}
		}
		report.Groups = append(report.Groups, group)
	}
	sort.SliceStable(report.Groups, func(i, j int) bool {
		a, b := report.Groups[i], report.Groups[j]
		if a.Level != b.Level {
			return levelRank[a.Level] < levelRank[b.Level]
		}
		if !strings.EqualFold(a.Artist, b.Artist) {
			return strings.ToLower(a.Artist) < strings.ToLower(b.Artist)
		}
		return strings.ToLower(a.Title) < strings.ToLower(b.Title)
	})

	report.Matched = len(report.Groups)
	if opts.Offset > 0 {
		report.Groups = report.Groups[min(opts.Offset, len(report.Groups)):]
	}
	if opts.Limit > 0 && len(report.Groups) > opts.Limit {
		report.Groups = report.Groups[:opts.Limit]
	}

	// The unchecked count is defined across every library. When these groups already
	// are that — every library compared, or only one to compare — they are reused
	// rather than grouped a second time.
	var enabled int64
	db.Model(&models.Library{}).Where("enabled = ?", true).Count(&enabled)
	if opts.Library == nil && (opts.AcrossLibraries || enabled <= 1) {
		report.Unchecked = len(uncheckedIn(groups, items))
		return report, nil
	}
	unchecked, err := UncheckedCandidates(db)
	if err != nil {
		return report, err
	}
	report.Unchecked = len(unchecked)
	return report, nil
}

// UncheckedCandidates lists the files whose audio hash would change what a group
// says: two or more files of one recording, on one album, in one format, compared
// across every library (a superset of any narrower view), whose hash is not current.
// A file whose only partner on its album is in another format is left out — two
// formats cannot share a hash — and so is the same recording on two albums.
func UncheckedCandidates(db *gorm.DB) ([]models.LibraryItem, error) {
	items, err := candidateItems(db, nil)
	if err != nil {
		return nil, err
	}
	return uncheckedIn(groupItems(db, items, true), items), nil
}

// uncheckedIn picks the unchecked candidates out of groups built from items.
func uncheckedIn(groups []DuplicateGroup, items []models.LibraryItem) []models.LibraryItem {
	byID := make(map[string]models.LibraryItem, len(items))
	for _, item := range items {
		byID[item.ID.String()] = item
	}

	var out []models.LibraryItem
	for _, group := range groups {
		type slot struct{ album, format string }
		counts := map[slot]int{}
		for _, f := range group.Files {
			counts[slot{albumKey(f), f.Format}]++
		}
		for _, f := range group.Files {
			if counts[slot{albumKey(f), f.Format}] < 2 || f.AudioHash != "" {
				continue
			}
			out = append(out, byID[f.ItemID])
		}
	}
	return out
}
