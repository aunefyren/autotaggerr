package modules

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// File import: reading a folder of loose files into album-shaped groups.
//
// This half reads only. It never writes, moves or tags anything, and it answers in
// the shape the import review is built around — one group per album folder — so the
// person importing picks a release once per album rather than once per file, the
// same reason folder attach exists (docs/attach.md).

// MaxImportScanFiles bounds one source scan. Each file's tags are read for an embedded
// release ID, so pointing the scan at a whole download share would be minutes of I/O
// to render a page; past this the answer is to narrow the path.
const MaxImportScanFiles = 5000

// ErrImportSourceTooLarge is a source holding more audio files than one scan reads.
var ErrImportSourceTooLarge = fmt.Errorf("more than %d audio files under this path — choose a folder closer to the albums", MaxImportScanFiles)

// ImportGroup is one album's worth of loose files: the files of one folder, with a
// disc folder ("CD 02") folded into the album folder above it.
type ImportGroup struct {
	// Folder is the album folder, absolute.
	Folder string `json:"folder"`
	// Files are the group's audio files, absolute and sorted.
	Files []string `json:"files"`
	// Artist, Album and Year prefill the release search, and each says where it came
	// from (HintFromTags or HintFromFolder) so the page can say how much to trust it.
	// They are only ever a hint. See importHint for the order they are read in.
	Artist     string `json:"artist,omitempty"`
	ArtistFrom string `json:"artist_from,omitempty"`
	Album      string `json:"album,omitempty"`
	AlbumFrom  string `json:"album_from,omitempty"`
	Year       int    `json:"year,omitempty"`
	YearFrom   string `json:"year_from,omitempty"`
	// Discs is how many disc folders the group spans; 1 for a flat album folder.
	Discs int `json:"discs"`
	// ReleaseID is the MusicBrainz release every tagged file in the group agrees on,
	// when they carry one (a Picard-tagged rip does). Empty when none does, or when
	// they disagree — a folder mixing two releases is exactly the case a guess would
	// get wrong, so it is reported (ReleaseIDsDisagree) rather than resolved.
	ReleaseID          string `json:"release_id,omitempty"`
	ReleaseIDsDisagree bool   `json:"release_ids_disagree,omitempty"`
	// Edition is what the files' tags say about which pressing they are. Nil when
	// they say nothing. See ImportEditionClues.
	Edition *ImportEditionClues `json:"edition,omitempty"`
}

// ImportEditionClues are the tags that tell one edition of an album from another,
// each the value most of a group's files agree on. A tagged download often carries
// them even when it carries no MusicBrainz ID — the barcode especially, since a rip
// is tagged from the disc or the store page it came from. They are only ever marked
// against the editions MusicBrainz lists; the edition is still picked by a person.
type ImportEditionClues struct {
	Barcode       string `json:"barcode,omitempty"`
	CatalogNumber string `json:"catalog_number,omitempty"`
	Label         string `json:"label,omitempty"`
	Country       string `json:"country,omitempty"`
	Media         string `json:"media,omitempty"`
	Date          string `json:"date,omitempty"`
}

// ScanImportSource walks root and groups its audio files by album folder. root may
// also be a single audio file.
func ScanImportSource(root string) ([]ImportGroup, error) {
	root = filepath.Clean(root)
	info, err := os.Stat(root)
	if err != nil {
		return nil, err
	}

	var files []string
	if !info.IsDir() {
		if !IsSupportedAudioFile(root) {
			return nil, fmt.Errorf("%s is not a supported audio file", root)
		}
		files = []string{root}
	} else {
		err = filepath.WalkDir(root, func(path string, d fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				// An unreadable subfolder is skipped, not fatal: the rest of the
				// source can still be imported, and the root itself was readable.
				if d != nil && d.IsDir() && path != root {
					return fs.SkipDir
				}
				return walkErr
			}
			if d.IsDir() {
				// Hidden folders are other tools' state (.@__thumb, .stfolder), never
				// an album waiting to be imported.
				if path != root && strings.HasPrefix(d.Name(), ".") {
					return fs.SkipDir
				}
				return nil
			}
			if strings.HasPrefix(d.Name(), ".") || !IsSupportedAudioFile(path) {
				return nil
			}
			files = append(files, path)
			if len(files) > MaxImportScanFiles {
				return ErrImportSourceTooLarge
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
	}

	return GroupImportFiles(root, files, ""), nil
}

// GroupImportFiles groups audio files already known to lie under root by album
// folder, as ScanImportSource does for the files it walks. It is the entry point for a
// caller that has its own list — a download client, which knows which of a download's
// files are complete — rather than whatever happens to be on disk.
//
// name stands in for the album folder's name when the files sit directly above root:
// a single-file torrent's content is the file itself, and the folder it sits in is the
// category's, which names nothing. Empty means no such name.
func GroupImportFiles(root string, files []string, name string) []ImportGroup {
	root = filepath.Clean(root)
	byFolder := map[string][]string{}
	for _, file := range files {
		folder := importAlbumFolder(root, file)
		byFolder[folder] = append(byFolder[folder], file)
	}

	groups := make([]ImportGroup, 0, len(byFolder))
	for folder, members := range byFolder {
		sort.Strings(members)
		group := ImportGroup{Folder: folder, Files: members, Discs: discFolderCount(folder, members)}
		importHint(&group, root, name)
		group.ReleaseID, group.ReleaseIDsDisagree = agreedReleaseID(members)
		groups = append(groups, group)
	}
	sort.Slice(groups, func(i, j int) bool { return groups[i].Folder < groups[j].Folder })
	return groups
}

// ImportGroupFolders is the album folders GroupImportFiles would make of files, without
// reading any tags — what a caller needs to tell whether every album of a download has
// been imported.
func ImportGroupFolders(root string, files []string) []string {
	root = filepath.Clean(root)
	seen := map[string]bool{}
	var out []string
	for _, file := range files {
		if folder := importAlbumFolder(root, file); !seen[folder] {
			seen[folder] = true
			out = append(out, folder)
		}
	}
	sort.Strings(out)
	return out
}

// importAlbumFolder is the folder a file's album is: its own folder, or the one above
// it when its own is a disc folder. The root itself is never climbed out of — loose
// files directly under a disc-named root are still that root's.
func importAlbumFolder(root, file string) string {
	folder := filepath.Dir(file)
	if folder != root && discNumberFromFolderName(filepath.Base(folder)) > 0 {
		if parent := filepath.Dir(folder); parent != folder {
			return parent
		}
	}
	return folder
}

// Where an import hint came from.
const (
	HintFromTags   = "tags"
	HintFromFolder = "folder"
)

// importHint fills a group's artist, album and year, most trustworthy source first:
//
//  1. **The files' own tags.** A download is nearly always tagged, and the tags name
//     the album artist outright — no folder convention needed.
//  2. **The album folder's own name**, in the shapes downloads come in:
//     "Artist - Album (Year) [FLAC]", "Artist - Year - Album", "Artist.Album.Year.WEB",
//     "Artist-Album-WEB-2014-GROUP".
//  3. **The parent folder, only inside the scanned root.** This is the library layout's
//     rule (<ARTIST>/<ALBUM>), and it is right only for a tree someone laid out that
//     way under the folder they pointed at. Above the root the parent is wherever the
//     downloads happen to live — "downloads", "complete", "config" — which is what the
//     search used to be prefilled with.
//
// name, when given, is read in place of the album folder's name for a group whose
// folder lies outside root (see GroupImportFiles).
func importHint(group *ImportGroup, root, name string) {
	artist, album, year, edition := tagHint(group.Files)
	group.Edition = edition
	if artist != "" {
		group.Artist, group.ArtistFrom = artist, HintFromTags
	}
	if album != "" {
		group.Album, group.AlbumFrom = album, HintFromTags
	}
	if year > 0 {
		group.Year, group.YearFrom = year, HintFromTags
	}

	folderName := filepath.Base(group.Folder)
	if name != "" && !PathWithin(group.Folder, root) {
		folderName = name
	}
	fArtist, fAlbum, fYear := ParseAlbumFolderName(folderName)
	if group.Artist == "" && fArtist == "" {
		if parent := filepath.Dir(group.Folder); parent != group.Folder && parent != filepath.Clean(root) && PathWithin(parent, root) {
			fArtist = filepath.Base(parent)
		}
	}
	if group.Artist == "" && fArtist != "" {
		group.Artist, group.ArtistFrom = fArtist, HintFromFolder
	}
	if group.Album == "" && fAlbum != "" {
		group.Album, group.AlbumFrom = fAlbum, HintFromFolder
	}
	if group.Year == 0 && fYear > 0 {
		group.Year, group.YearFrom = fYear, HintFromFolder
	}
}

// tagHint reads the album artist, album and year most of a group's files agree on,
// and the edition clues beside them. The most common value rather than the first, so
// one stray file does not name the album; ALBUMARTIST before ARTIST, so a
// compilation's track artists do not either.
func tagHint(files []string) (artist, album string, year int, edition *ImportEditionClues) {
	artists, albums, years := map[string]int{}, map[string]int{}, map[string]int{}
	barcodes, catalogNumbers, labels := map[string]int{}, map[string]int{}, map[string]int{}
	countries, media, dates := map[string]int{}, map[string]int{}, map[string]int{}
	count := func(into map[string]int, value string) {
		if value != "" {
			into[value]++
		}
	}
	for _, file := range files {
		tags, err := ReadFileTagMap(file)
		if err != nil {
			continue
		}
		count(artists, firstOfTags(tags, "ALBUMARTIST", "ALBUM ARTIST", "ARTIST"))
		count(albums, firstOfTags(tags, "ALBUM"))
		count(years, yearOf(firstOfTags(tags, "ORIGINALDATE", "TDOR", "ORIGINALYEAR", "DATE", "YEAR")))
		// MP3 spells three of these as their ID3 frame (TPUB, TMED) or Picard's TXXX
		// description; FLAC as the Vorbis comment.
		count(barcodes, firstOfTags(tags, "BARCODE", "UPC", "EAN"))
		count(catalogNumbers, firstOfTags(tags, "CATALOGNUMBER", "CATALOG NUMBER", "CATALOG #"))
		count(labels, firstOfTags(tags, "LABEL", "PUBLISHER", "ORGANIZATION"))
		count(countries, firstOfTags(tags, "RELEASECOUNTRY", "MUSICBRAINZ ALBUM RELEASE COUNTRY"))
		count(media, firstOfTags(tags, "MEDIA", "TMED"))
		// The release's own date, not the original: the original is the album's, and
		// every edition shares it.
		count(dates, firstOfTags(tags, "RELEASEDATE", "DATE"))
	}
	year, _ = strconv.Atoi(mostCommon(years))

	clues := ImportEditionClues{
		Barcode:       mostCommon(barcodes),
		CatalogNumber: mostCommon(catalogNumbers),
		Label:         mostCommon(labels),
		Country:       mostCommon(countries),
		Media:         mostCommon(media),
		Date:          mostCommon(dates),
	}
	if clues != (ImportEditionClues{}) {
		edition = &clues
	}
	return mostCommon(artists), mostCommon(albums), year, edition
}

func firstOfTags(tags map[string][]string, keys ...string) string {
	for _, key := range keys {
		for _, v := range tags[key] {
			if v = strings.TrimSpace(v); v != "" {
				return v
			}
		}
	}
	return ""
}

func mostCommon(counts map[string]int) string {
	best, bestN := "", 0
	for value, n := range counts {
		if n > bestN || (n == bestN && value < best) {
			best, bestN = value, n
		}
	}
	return best
}

// ReadFileTagMap reads a file's tags as the canonical upper-case keys both engines use.
func ReadFileTagMap(path string) (map[string][]string, error) {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".flac":
		return getFlacTagsMap(path)
	case ".mp3":
		return GetMP3Tags(path)
	}
	return nil, errors.New("unsupported file type")
}

var (
	folderYear       = regexp.MustCompile(`(?:^|[\s._\-(\[])((?:19|20)\d{2})(?:$|[\s._\-)\]])`)
	folderBracketed  = regexp.MustCompile(`\s*(?:\[[^\]]*\]|\{[^}]*\}|\([^)]*\))`)
	folderOnlyYear   = regexp.MustCompile(`^(?:19|20)\d{2}$`)
	folderSceneSplit = regexp.MustCompile(`[._]+`)
)

// ParseAlbumFolderName reads artist, album and year out of the name a download's
// folder usually has. Bracketed extras ("[FLAC]", "(WEB)", "{Deluxe}") are dropped.
// A name with no " - " is the album alone — unless it is a scene name ("Artist.Album.
// 1991.WEB", "Artist-Album-WEB-2014-GRP"), whose separators are dots, underscores and
// single hyphens, and where everything from the year on is release-group noise.
func ParseAlbumFolderName(name string) (artist, album string, year int) {
	if m := folderYear.FindStringSubmatch(name); m != nil {
		year, _ = strconv.Atoi(m[1])
	}
	clean := strings.TrimSpace(folderBracketed.ReplaceAllString(name, ""))

	var parts []string
	scene := !strings.Contains(clean, " ")
	switch {
	case strings.Contains(clean, " - "):
		parts = strings.Split(clean, " - ")
	case scene && strings.Contains(clean, "-"):
		parts = strings.Split(folderSceneSplit.ReplaceAllString(clean, " "), "-")
	case scene:
		words := strings.Fields(folderSceneSplit.ReplaceAllString(clean, " "))
		for i, word := range words {
			if folderOnlyYear.MatchString(word) {
				words = words[:i]
				break
			}
		}
		return "", strings.Join(words, " "), year
	default:
		return "", clean, year
	}

	var kept []string
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if folderOnlyYear.MatchString(part) {
			if scene {
				break // the year and the noise after it
			}
			continue
		}
		kept = append(kept, part)
	}
	switch len(kept) {
	case 0:
		return "", "", year
	case 1:
		return "", kept[0], year
	}
	if scene {
		return kept[0], kept[1], year
	}
	return kept[0], strings.Join(kept[1:], " - "), year
}

// discFolderCount is how many distinct disc folders hold a group's files.
func discFolderCount(folder string, files []string) int {
	discs := map[string]bool{}
	for _, file := range files {
		if dir := filepath.Dir(file); dir != folder {
			discs[dir] = true
		}
	}
	if len(discs) == 0 {
		return 1
	}
	return len(discs)
}

// agreedReleaseID reads each file's embedded MusicBrainz release ID and returns it
// when every file that has one agrees.
func agreedReleaseID(files []string) (string, bool) {
	agreed := ""
	for _, file := range files {
		id, err := ExtractMusicBrainzReleaseID(file)
		if err != nil || strings.TrimSpace(id) == "" {
			continue
		}
		id = strings.TrimSpace(id)
		if agreed == "" {
			agreed = id
		} else if agreed != id {
			return "", true
		}
	}
	return agreed, false
}

// PathWithin reports whether path is root or lies beneath it. Both are cleaned; the
// comparison is by path segments, so /music2 is not within /music.
func PathWithin(path, root string) bool {
	rel, err := filepath.Rel(filepath.Clean(root), filepath.Clean(path))
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))
}

// TransferMode is how an import puts a file into the library.
type TransferMode string

const (
	// TransferMove moves the file; across filesystems it is a copy followed by
	// deleting the source.
	TransferMove TransferMode = "move"
	// TransferCopy leaves the source where it is. There is deliberately no hardlink
	// mode: the file is tagged once it lands, and tagging a hardlink rewrites the
	// other name's file too — a seeding torrent's, typically.
	TransferCopy TransferMode = "copy"
)

// TransferFile puts source at target, replacing a file already there. The new file
// appears at target atomically: it is written beside the target under a temporary
// name and renamed into place, so a failed copy never leaves a half-written file at
// the target — or loses the file it was replacing.
func TransferFile(source, target string, mode TransferMode) error {
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return err
	}
	if mode == TransferMove {
		// A rename is the whole move on one filesystem, and it replaces the target.
		// Any failure falls through to copy-then-delete: crossing devices is the
		// common one, and anything else (permissions) fails the copy with a clearer
		// error than the rename gave.
		if err := os.Rename(source, target); err == nil {
			return nil
		}
	}
	if err := copyIntoPlace(source, target); err != nil {
		return err
	}
	if mode == TransferMove {
		if err := os.Remove(source); err != nil {
			return fmt.Errorf("copied to %s, but could not remove the source: %w", target, err)
		}
	}
	return nil
}

func copyIntoPlace(source, target string) (err error) {
	in, err := os.Open(source)
	if err != nil {
		return err
	}
	defer in.Close()

	tmp, err := os.CreateTemp(filepath.Dir(target), "."+filepath.Base(target)+".importing-*")
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			_ = os.Remove(tmp.Name())
		}
	}()
	if _, err = tmp.ReadFrom(in); err != nil {
		tmp.Close()
		return err
	}
	if err = tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err = tmp.Close(); err != nil {
		return err
	}
	if info, statErr := in.Stat(); statErr == nil {
		_ = os.Chmod(tmp.Name(), info.Mode().Perm())
	}
	return os.Rename(tmp.Name(), target)
}

// RemoveEmptyDirs removes dir and each parent that is left empty, stopping at — and
// never removing — stop. A folder holding anything at all (a cover, a cue sheet) is
// left alone, which is what keeps this from being a delete of what an import did not
// take.
func RemoveEmptyDirs(dir, stop string) {
	stop = filepath.Clean(stop)
	for dir = filepath.Clean(dir); dir != stop && PathWithin(dir, stop); dir = filepath.Dir(dir) {
		if err := os.Remove(dir); err != nil {
			// Not empty, or not ours to remove: either way, nothing above it is
			// empty because of us.
			if !errors.Is(err, fs.ErrNotExist) {
				return
			}
		}
	}
}

// MaxFolderSuggestions bounds one autocomplete answer: enough to pick from, few enough
// that a folder holding ten thousand artists does not ship them all per keystroke.
const MaxFolderSuggestions = 50

// SuggestFolders completes a partly typed absolute path to the folders it could name.
// Ending in a separator lists the folder's children; otherwise it lists the siblings
// whose names start with the last segment, ignoring case. Hidden folders are left out
// unless the segment itself starts with a dot.
//
// A path that cannot be read — still being typed, relative, unreadable — completes to
// nothing rather than to an error: half a path is the normal state of the input.
func SuggestFolders(input string, limit int) []string {
	if input == "" || !filepath.IsAbs(input) {
		return nil
	}
	parent, prefix := input, ""
	if !strings.HasSuffix(input, string(filepath.Separator)) && !strings.HasSuffix(input, "/") {
		parent, prefix = filepath.Dir(input), filepath.Base(input)
	}
	entries, err := os.ReadDir(parent)
	if err != nil {
		return nil
	}
	lowered := strings.ToLower(prefix)
	var out []string
	for _, entry := range entries {
		name := entry.Name()
		if strings.HasPrefix(name, ".") && !strings.HasPrefix(prefix, ".") {
			continue
		}
		if !strings.HasPrefix(strings.ToLower(name), lowered) {
			continue
		}
		full := filepath.Join(parent, name)
		// A symlink to a folder is a folder to the person typing; follow it.
		if info, err := os.Stat(full); err != nil || !info.IsDir() {
			continue
		}
		out = append(out, full)
		if len(out) >= limit {
			break
		}
	}
	sort.Strings(out)
	return out
}
