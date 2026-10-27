package modules

import (
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/aunefyren/autotaggerr/models"
	"golang.org/x/text/unicode/norm"
)

// Naming: where a file belongs under its library root.
//
// One pure function, RenderLibraryPath, turns a release, one of its tracks and a
// profile's templates into the four segments of the library layout. File import and
// an organise verb are both consumers of it, and so is the item detail's PATH row —
// so there is one renderer and the preview cannot disagree with what a move would do.
//
// Token names and their meanings are Lidarr's wherever Lidarr has the token, so a
// Lidarr naming format pastes in and renders the same paths. That is also why
// {Release Year} is the *album's* first-release year (Lidarr's album is a
// release-group) and the edition's own year is a separate token. Character cleaning
// follows Lidarr's defaults for the same reason: a library Lidarr named should
// preview as unchanged.

// NamingField identifies one of the four templates. The values are the JSON keys of
// models.NamingTemplates, so a check result can be placed under its own input.
type NamingField string

const (
	NamingArtistFolder NamingField = "artist_folder"
	NamingAlbumFolder  NamingField = "album_folder"
	NamingMediaFolder  NamingField = "media_folder"
	NamingTrackFile    NamingField = "track_file"
)

// namingFields is the path order, which is also the order of the levels: a template
// may use a token of its own level or of any level before it.
var namingFields = []NamingField{NamingArtistFolder, NamingAlbumFolder, NamingMediaFolder, NamingTrackFile}

func (f NamingField) level() int {
	for i, field := range namingFields {
		if field == f {
			return i
		}
	}
	return len(namingFields)
}

func (f NamingField) label() string {
	return strings.ReplaceAll(string(f), "_", " ")
}

func (f NamingField) template(n models.NamingTemplates) string {
	switch f {
	case NamingArtistFolder:
		return n.ArtistFolder
	case NamingAlbumFolder:
		return n.AlbumFolder
	case NamingMediaFolder:
		return n.MediaFolder
	default:
		return n.TrackFile
	}
}

// namingContext is what a token reads: the release, the medium the track is on, the
// track, and the file being named.
type namingContext struct {
	release models.MusicBrainzReleaseResponse
	media   models.MusicBrainzMedia
	track   models.Track
	file    NamingFile
}

// NamingFile is what a template reads from the file being named rather than from
// MusicBrainz: its extension, what its audio is, and the release group of the
// download it came from.
//
// A value that is not known renders empty, exactly like one that is known to be
// empty — Lidarr's rule, and the one a Lidarr naming format is written for. A file
// that arrived before release groups were recorded, or whose headers could not be
// read, is named without the token and its brackets collapse, the same as an MP3's
// bit depth. That does drop "-GRP" from a name that carries it when nothing recorded
// the group; refusing to rename such files instead left Organise unable to apply a
// template to most of a native library.
type NamingFile struct {
	Ext   string
	Audio AudioProperties
	// ReleaseGroup is the group that put out the download ("GRP" in
	// "Artist-Album-WEB-FLAC-2014-GRP"), "" for none or not recorded.
	ReleaseGroup string
}

// NamingFileFor reads what a file's name may need to know about it: its extension and
// its audio from the file, and the release group from wherever the caller recorded it
// (nil: never recorded, which names it as none). The audio is only read when audio is
// true — a template that uses no MediaInfo token should not cost a file open — and a
// file whose headers cannot be read is named as having no codec or depth.
func NamingFileFor(path string, readAudio bool, releaseGroup *string) NamingFile {
	file := NamingFile{Ext: filepath.Ext(path)}
	if readAudio {
		if audio, err := ReadAudioProperties(path); err == nil {
			file.Audio = audio
		}
	}
	if releaseGroup != nil {
		file.ReleaseGroup = strings.TrimSpace(*releaseGroup)
	}
	return file
}

// NamingUsesAudio reports whether any of the templates reads the file's audio, so a
// caller naming many files opens them only when a token needs it.
func NamingUsesAudio(templates models.NamingTemplates) bool {
	return namingUses(templates, func(t NamingToken) bool { return t.audio })
}

func namingUses(templates models.NamingTemplates, match func(NamingToken) bool) bool {
	templates = templates.WithDefaults()
	for _, field := range namingFields {
		parts, err := parseNamingTemplate(field.template(templates))
		if err != nil {
			continue
		}
		for _, part := range parts {
			if part.token != nil && match(*part.token) {
				return true
			}
		}
	}
	return false
}

// albumArtist is the artist an album belongs to, which is the release-group's credit
// first and the release's only when the group carries none — the same authority the
// collection uses for "whose album it is" (docs/collection.md). Reading the release's
// credit would put a pressing still carrying an old credit under a different artist
// folder from the rest of the album during an upstream migration.
func (c namingContext) albumArtist() models.Artist {
	if credit := c.release.ReleaseGroup.ArtistCredit; len(credit) > 0 {
		return creditArtist(credit[0])
	}
	if len(c.release.ArtistCredit) > 0 {
		return creditArtist(c.release.ArtistCredit[0])
	}
	return models.Artist{}
}

// albumTitle is the release-group's title, falling back to the release's.
func (c namingContext) albumTitle() string {
	if c.release.ReleaseGroup.Title != "" {
		return c.release.ReleaseGroup.Title
	}
	return c.release.Title
}

func creditArtist(credit models.ArtistCredit) models.Artist {
	artist := credit.Artist
	if artist.Name == "" {
		artist.Name = credit.Name
	}
	return artist
}

// NamingToken is one placeholder a template may use.
type NamingToken struct {
	// Name is the canonical spelling, written as {Name} in a template.
	Name string `json:"name"`
	// Field is the first template that may use it: an album token cannot appear in
	// the artist folder, because one artist folder holds every album.
	Field       NamingField `json:"field"`
	Description string      `json:"description"`
	// Numeric tokens take a padding format, {track:00}.
	Numeric bool `json:"numeric"`

	text   func(namingContext) string
	number func(namingContext) int
	// audio marks the tokens read from the file's audio headers.
	audio bool
}

func (t NamingToken) render(c namingContext, format string) string {
	if t.Numeric {
		n := t.number(c)
		if n <= 0 {
			return ""
		}
		return fmt.Sprintf("%0*d", len(format), n)
	}
	return t.text(c)
}

// YearOf is the year of a MusicBrainz date ("1977", "1977-02", "1977-02-04"), or "".
func YearOf(date string) string { return yearOf(date) }

func yearOf(date string) string {
	if len(date) >= 4 {
		if _, err := strconv.Atoi(date[:4]); err == nil {
			return date[:4]
		}
	}
	return ""
}

func creditString(credits []models.ArtistCredit) string {
	var b strings.Builder
	for _, credit := range credits {
		b.WriteString(credit.Name)
		b.WriteString(credit.Joinphrase)
	}
	return b.String()
}

// cleanTitle is Lidarr's *Clean* form of a name (CleanName, CleanTitle): "&" spelled
// "and", slashes as spaces, brackets and apostrophes gone, and the punctuation that
// separates words — a colon or comma before a space, a dash standing alone — dropped.
// Letters, including accented ones, are kept. Taken from the Sonarr-family
// FileNameBuilder rather than from a Lidarr library side by side, like the cleaning
// rules below; if a Lidarr-named library previews moves for Clean tokens, this is the
// function to correct.
func cleanTitle(title string) string {
	title = strings.ReplaceAll(title, "&", "and")
	title = strings.NewReplacer("/", " ", `\`, " ").Replace(title)
	title = strings.Map(func(r rune) rune {
		switch r {
		case '(', ')', '[', ']', '{', '}', '\'', '`', '’', '"':
			return -1
		}
		return r
	}, title)

	words := strings.Fields(title)
	out := words[:0]
	for _, w := range words {
		// A word that is only punctuation is a separator: "A - B" is "A B".
		if strings.TrimFunc(w, isCleanPunct) == "" {
			continue
		}
		// Punctuation ending a word ends a clause: "Title: Subtitle", "Yes, No", "Why?".
		out = append(out, strings.TrimRightFunc(w, isCleanPunct))
	}
	return strings.Join(out, " ")
}

func isCleanPunct(r rune) bool {
	return strings.ContainsRune(`,<>;:|~!?@$%^*-_=.`, r)
}

// titleThe is Lidarr's *The* form: a leading article moved to the end, ahead of any
// trailing parenthetical — "The Beatles" is "Beatles, The", "A Tribe Called Quest
// (live)" is "Tribe Called Quest, A (live)".
func titleThe(title string) string {
	m := leadingArticle.FindStringSubmatch(title)
	if m == nil {
		return title
	}
	return m[2] + ", " + m[1] + m[3]
}

var leadingArticle = regexp.MustCompile(`(?i)^(the|a|an)\s+(.*?)((?: *\([^)]+\))*)$`)

// editionFormat summarises a release's media: consecutive identical formats collapse
// to a count, a mixed set is listed. ASCII "x", not "×", because this is a path, and
// a path gets typed into a shell.
func editionFormat(release models.MusicBrainzReleaseResponse) string {
	var parts []string
	count, current := 0, ""
	flush := func() {
		if count == 0 {
			return
		}
		name := current
		if name == "" {
			name = "Unknown"
		}
		if count > 1 {
			name = fmt.Sprintf("%dx%s", count, name)
		}
		parts = append(parts, name)
	}
	for _, m := range release.Media {
		if m.Format != current || count == 0 {
			flush()
			current, count = m.Format, 0
		}
		count++
	}
	flush()
	return strings.Join(parts, " + ")
}

// editionCatalogNumber is the first catalogue number the release's labels carry.
// MusicBrainz writes "[none]" for a release that has none, which is not a name.
func editionCatalogNumber(release models.MusicBrainzReleaseResponse) string {
	for _, li := range release.LabelInfo {
		if n := strings.TrimSpace(li.CatalogNumber); n != "" && !strings.EqualFold(n, "[none]") {
			return n
		}
	}
	return ""
}

// namingTokens is every token, in the order the profile editor lists them.
var namingTokens = []NamingToken{
	{Name: "Artist Name", Field: NamingArtistFolder, Description: "The album's first credited artist, by their current MusicBrainz name",
		text: func(c namingContext) string { return c.albumArtist().Name }},
	{Name: "Artist SortName", Field: NamingArtistFolder, Description: "The same artist's sort name (\"Beatles, The\")",
		text: func(c namingContext) string { return c.albumArtist().SortName }},
	{Name: "Artist CleanName", Field: NamingArtistFolder, Description: "The artist's name without punctuation (\"Guns N Roses\")",
		text: func(c namingContext) string { return cleanTitle(c.albumArtist().Name) }},
	{Name: "Artist NameThe", Field: NamingArtistFolder, Description: "The artist's name with a leading The, A or An moved to the end (\"Beatles, The\")",
		text: func(c namingContext) string { return titleThe(c.albumArtist().Name) }},
	{Name: "Artist Disambiguation", Field: NamingArtistFolder, Description: "MusicBrainz's comment telling same-named artists apart",
		text: func(c namingContext) string { return c.albumArtist().Disambiguation }},
	{Name: "Artist MbId", Field: NamingArtistFolder, Description: "The artist's MusicBrainz ID",
		text: func(c namingContext) string { return c.albumArtist().ID }},

	{Name: "Album Title", Field: NamingAlbumFolder, Description: "The album's title (the release-group's)",
		text: func(c namingContext) string { return c.albumTitle() }},
	{Name: "Album CleanTitle", Field: NamingAlbumFolder, Description: "The album's title without punctuation",
		text: func(c namingContext) string { return cleanTitle(c.albumTitle()) }},
	{Name: "Album TitleThe", Field: NamingAlbumFolder, Description: "The album's title with a leading The, A or An moved to the end",
		text: func(c namingContext) string { return titleThe(c.albumTitle()) }},
	{Name: "Album Type", Field: NamingAlbumFolder, Description: "Album, Single, EP, Broadcast or Other",
		text: func(c namingContext) string { return c.release.ReleaseGroup.PrimaryType }},
	{Name: "Album Disambiguation", Field: NamingAlbumFolder, Description: "MusicBrainz's comment on the album",
		text: func(c namingContext) string { return c.release.ReleaseGroup.Disambiguation }},
	{Name: "Album MbId", Field: NamingAlbumFolder, Description: "The release-group's MusicBrainz ID",
		text: func(c namingContext) string { return c.release.ReleaseGroup.ID }},
	{Name: "Release Year", Field: NamingAlbumFolder, Description: "The year the album first came out, whichever edition this is",
		text: func(c namingContext) string {
			if year := yearOf(c.release.ReleaseGroup.FirstReleaseDate); year != "" {
				return year
			}
			return yearOf(c.release.Date)
		}},
	{Name: "Edition Title", Field: NamingAlbumFolder, Description: "This edition's own title, which can differ from the album's",
		text: func(c namingContext) string { return c.release.Title }},
	{Name: "Edition Year", Field: NamingAlbumFolder, Description: "The year this edition came out",
		text: func(c namingContext) string { return yearOf(c.release.Date) }},
	{Name: "Edition Disambiguation", Field: NamingAlbumFolder, Description: "MusicBrainz's comment on this edition (\"deluxe\", \"remaster\")",
		text: func(c namingContext) string { return c.release.Disambiguation }},
	{Name: "Edition CleanTitle", Field: NamingAlbumFolder, Description: "This edition's title without punctuation",
		text: func(c namingContext) string { return cleanTitle(c.release.Title) }},
	{Name: "Edition Country", Field: NamingAlbumFolder, Description: "Where this edition was released, as MusicBrainz's country code (\"US\", \"XE\")",
		text: func(c namingContext) string { return c.release.Country }},
	{Name: "Edition Format", Field: NamingAlbumFolder, Description: "This edition's media (\"CD\", \"2xCD\", \"CD + DVD\", \"Digital Media\")",
		text: func(c namingContext) string { return editionFormat(c.release) }},
	{Name: "Edition CatalogNumber", Field: NamingAlbumFolder, Description: "This edition's catalogue number, from its first label",
		text: func(c namingContext) string { return editionCatalogNumber(c.release) }},
	{Name: "Edition MbId", Field: NamingAlbumFolder, Description: "The release's MusicBrainz ID",
		text: func(c namingContext) string { return c.release.ID }},

	{Name: "medium", Field: NamingMediaFolder, Description: "The disc number", Numeric: true,
		number: func(c namingContext) int { return c.media.Position }},
	{Name: "Medium Format", Field: NamingMediaFolder, Description: "CD, Vinyl, Digital Media…",
		text: func(c namingContext) string { return c.media.Format }},

	{Name: "track", Field: NamingTrackFile, Description: "The track's position on its disc", Numeric: true,
		number: func(c namingContext) int { return c.track.Position }},
	{Name: "Track Title", Field: NamingTrackFile, Description: "The track's title",
		text: func(c namingContext) string { return c.track.Title }},
	{Name: "Track CleanTitle", Field: NamingTrackFile, Description: "The track's title without punctuation",
		text: func(c namingContext) string { return cleanTitle(c.track.Title) }},
	{Name: "Track ArtistName", Field: NamingTrackFile, Description: "The track's own credit, featured artists included",
		text: func(c namingContext) string { return creditString(c.track.ArtistCredit) }},

	// Read from the file, not from MusicBrainz, so they belong to the track file only:
	// the files of one album folder can differ in all three.
	{Name: "MediaInfo AudioCodec", Field: NamingTrackFile, Description: "The file's audio codec, read from the file (\"FLAC\", \"MP3\")",
		text: func(c namingContext) string { return c.file.Audio.Codec }, audio: true},
	{Name: "MediaInfo AudioBitsPerSample", Field: NamingTrackFile, Description: "The file's sample depth (\"16\", \"24\"); empty for MP3, which has none",
		text: func(c namingContext) string {
			if c.file.Audio.BitsPerSample <= 0 {
				return ""
			}
			return strconv.Itoa(c.file.Audio.BitsPerSample)
		},
		audio: true},
	{Name: "Release Group", Field: NamingTrackFile, Description: "The group that put out the download the file came from (\"GRP\" in Artist-Album-WEB-FLAC-2014-GRP) — not MusicBrainz's release group, which is {Album Title}",
		text: func(c namingContext) string { return c.file.ReleaseGroup }},
}

// NamingTokens lists every token a template may use.
func NamingTokens() []NamingToken {
	out := make([]NamingToken, len(namingTokens))
	copy(out, namingTokens)
	return out
}

// tokenKey folds a token's spelling to the form it is looked up by: Lidarr accepts
// {Artist Name}, {Artist.Name}, {artist_name} and {ARTIST NAME} as one token.
func tokenKey(words []string) string {
	return strings.ToLower(strings.Join(words, " "))
}

var namingTokensByKey = func() map[string]NamingToken {
	m := make(map[string]NamingToken, len(namingTokens))
	for _, t := range namingTokens {
		m[tokenKey(strings.Fields(t.Name))] = t
	}
	return m
}()

// tokenBody is what sits between the braces: an optional prefix and suffix that are
// written only when the value is not empty ("{ (Album Disambiguation)}"), one or two
// words joined by a separator, and a padding format for numbers.
var tokenBody = regexp.MustCompile(`^([- ._\[(]*)([A-Za-z0-9]+)(?:([- ._]+)([A-Za-z0-9]+))?(?::([0-9]+))?([- ._)\]]*)$`)

type namingPart struct {
	literal string
	token   *NamingToken
	raw     string // the token as written, for error messages
	prefix  string
	suffix  string
	sep     string // the separator between the token's words
	casing  string // "lower", "upper" or ""
	format  string
}

func (p namingPart) render(c namingContext) string {
	if p.token == nil {
		return p.literal
	}
	value := strings.TrimSpace(p.token.render(c, p.format))
	if value == "" {
		return ""
	}
	if p.sep != "" && p.sep != " " {
		value = strings.Join(strings.Fields(value), p.sep)
	}
	switch p.casing {
	case "lower":
		value = strings.ToLower(value)
	case "upper":
		value = strings.ToUpper(value)
	}
	return p.prefix + value + p.suffix
}

func parseNamingTemplate(template string) ([]namingPart, error) {
	var parts []namingPart
	rest := template
	for rest != "" {
		open := strings.IndexAny(rest, "{}")
		if open < 0 {
			parts = append(parts, namingPart{literal: rest})
			break
		}
		if rest[open] == '}' {
			return nil, errors.New("has a } with no { before it")
		}
		if open > 0 {
			parts = append(parts, namingPart{literal: rest[:open]})
		}
		end := strings.IndexAny(rest[open+1:], "{}")
		if end < 0 || rest[open+1+end] == '{' {
			return nil, fmt.Errorf("has a { with no } after it")
		}
		raw := rest[open : open+1+end+1]
		part, err := parseNamingToken(raw)
		if err != nil {
			return nil, err
		}
		parts = append(parts, part)
		rest = rest[open+1+end+1:]
	}
	for _, part := range parts {
		if part.token == nil && strings.ContainsAny(part.literal, `/\`) {
			return nil, errors.New("contains a path separator — each template is one folder level, so use the template for the next level instead")
		}
	}
	return parts, nil
}

func parseNamingToken(raw string) (namingPart, error) {
	m := tokenBody.FindStringSubmatch(raw[1 : len(raw)-1])
	if m == nil {
		return namingPart{}, fmt.Errorf("%s is not a naming token", raw)
	}
	words := []string{m[2]}
	if m[4] != "" {
		words = append(words, m[4])
	}
	token, ok := namingTokensByKey[tokenKey(words)]
	if !ok {
		return namingPart{}, fmt.Errorf("%s is not a naming token", raw)
	}
	part := namingPart{token: &token, raw: raw, prefix: m[1], sep: m[3], format: m[5], suffix: m[6]}
	if part.format != "" {
		if !token.Numeric {
			return namingPart{}, fmt.Errorf("%s: only a number takes a format like :00", raw)
		}
		if strings.Trim(part.format, "0") != "" {
			return namingPart{}, fmt.Errorf("%s: a number's format is zeros, one per digit (:00)", raw)
		}
	}
	// Lidarr's casing rule: a token written all in lower case renders lower case, all
	// in upper case renders upper case. Numbers are written in lower case by
	// convention ({track:00}) and have no case to change.
	letters := strings.Join(words, "")
	if !token.Numeric {
		switch {
		case letters == strings.ToLower(letters) && letters != strings.ToUpper(letters):
			part.casing = "lower"
		case letters == strings.ToUpper(letters) && letters != strings.ToLower(letters):
			part.casing = "upper"
		}
	}
	return part, nil
}

// LibraryPath is a file's place under its library root, one field per level of the
// layout. Media is empty for a release with a single audio medium.
type LibraryPath struct {
	Artist string `json:"artist"`
	Album  string `json:"album"`
	Media  string `json:"media,omitempty"`
	File   string `json:"file"`
}

// Rel joins the segments with the OS separator — the path relative to the library
// root.
func (p LibraryPath) Rel() string {
	segments := []string{p.Artist, p.Album}
	if p.Media != "" {
		segments = append(segments, p.Media)
	}
	return filepath.Join(append(segments, p.File)...)
}

// ErrNamingTrackNotFound is returned when the track is not on the release.
var ErrNamingTrackNotFound = errors.New("track is not on this release")

// NamingEmptyError is a template that rendered nothing for a release — one made only
// of tokens the release does not have. It is never written as an empty path segment.
type NamingEmptyError struct {
	Field   NamingField
	Release string
}

func (e *NamingEmptyError) Error() string {
	return fmt.Sprintf("the %s template renders empty for %q", e.Field.label(), e.Release)
}

// RenderLibraryPath renders where one track of a release belongs, keeping the file's
// extension. The templates are assumed valid (ValidateNamingTemplates); an invalid one
// is still an error here rather than a panic, since a stored row predates nothing.
func RenderLibraryPath(templates models.NamingTemplates, release models.MusicBrainzReleaseResponse, trackID string, file NamingFile) (LibraryPath, error) {
	templates = templates.WithDefaults()
	ext := file.Ext

	ctx, found := namingContextFor(release, trackID)
	if !found {
		return LibraryPath{}, fmt.Errorf("%w: %s on %s", ErrNamingTrackNotFound, trackID, release.ID)
	}
	ctx.file = file

	render := func(field NamingField, maxBytes int) (string, error) {
		parts, err := parseNamingTemplate(field.template(templates))
		if err != nil {
			return "", fmt.Errorf("%s template %w", field.label(), err)
		}
		segment := renderNamingParts(parts, ctx, maxBytes)
		if segment == "" {
			return "", &NamingEmptyError{Field: field, Release: release.Title}
		}
		return segment, nil
	}

	var out LibraryPath
	var err error
	if out.Artist, err = render(NamingArtistFolder, maxSegmentBytes); err != nil {
		return LibraryPath{}, err
	}
	if out.Album, err = render(NamingAlbumFolder, maxSegmentBytes); err != nil {
		return LibraryPath{}, err
	}
	if needsMediaFolder(release, ctx.media) {
		if out.Media, err = render(NamingMediaFolder, maxSegmentBytes); err != nil {
			return LibraryPath{}, err
		}
	}
	stem, err := render(NamingTrackFile, maxSegmentBytes-len(ext))
	if err != nil {
		return LibraryPath{}, err
	}
	out.File = stem + ext
	return out, nil
}

func namingContextFor(release models.MusicBrainzReleaseResponse, trackID string) (namingContext, bool) {
	for _, media := range release.Media {
		for _, track := range media.Tracks {
			if track.ID == trackID {
				return namingContext{release: release, media: media, track: track}, true
			}
		}
	}
	return namingContext{}, false
}

// needsMediaFolder reports whether a release is split into disc folders: when more
// than one of its media carries audio. Video is not counted, by the same rule as the
// collection's track counts (models.Track.IsVideo) — a CD+DVD whose DVD is all video
// is a one-disc album to an audio library, and splitting it would put every track in
// a "CD 01" folder with nothing beside it. The track's own medium always counts, so a
// file attached to a video track still lands in a folder of its own.
func needsMediaFolder(release models.MusicBrainzReleaseResponse, own models.MusicBrainzMedia) bool {
	audio := 0
	for _, media := range release.Media {
		if media.Position == own.Position || mediumHasAudio(media) {
			audio++
		}
	}
	return audio > 1
}

func mediumHasAudio(media models.MusicBrainzMedia) bool {
	for _, track := range media.Tracks {
		if !track.IsVideo() {
			return true
		}
	}
	return false
}

// maxSegmentBytes is the longest name ext4, NTFS and APFS all accept for one path
// segment.
const maxSegmentBytes = 255

func renderNamingParts(parts []namingPart, ctx namingContext, maxBytes int) string {
	var b strings.Builder
	anyEmpty := false
	for _, part := range parts {
		value := part.render(ctx)
		if part.token != nil && value == "" {
			anyEmpty = true
		}
		b.WriteString(value)
	}
	return CleanPathSegment(b.String(), anyEmpty, maxBytes)
}

var (
	// namingBadChars is Lidarr's default replacement for characters a file name may
	// not hold on at least one of the systems a library lives on.
	namingBadChars = strings.NewReplacer(
		`\`, "+", "/", "+",
		"<", "", ">", "", "|", "", `"`, "",
		"?", "!", "*", "-",
	)
	namingEmptyBrackets = regexp.MustCompile(`\(\s*\)|\[\s*\]|\{\s*\}`)
	namingEdgeSeparator = regexp.MustCompile(`^[\s\-_]+|[\s\-_]+$`)
	// Windows refuses these as a name, with or without an extension.
	windowsReservedNames = map[string]bool{
		"CON": true, "PRN": true, "AUX": true, "NUL": true,
		"COM1": true, "COM2": true, "COM3": true, "COM4": true, "COM5": true, "COM6": true, "COM7": true, "COM8": true, "COM9": true,
		"LPT1": true, "LPT2": true, "LPT3": true, "LPT4": true, "LPT5": true, "LPT6": true, "LPT7": true, "LPT8": true, "LPT9": true,
	}
)

// CleanPathSegment makes one rendered segment safe to use as a file or folder name.
//
// tokenWasEmpty says whether a token in it rendered empty; only then are the empty
// brackets and dangling separators it left behind removed, because "()" or a trailing
// dash in a real title is the title.
//
// Leading dots are trimmed as well as trailing ones. Trailing ones Windows refuses;
// leading ones make the folder hidden on Linux and macOS, where Plex skips hidden
// folders — Metallica's "...And Justice for All" would vanish from the player. Lidarr
// trims both for the same reasons.
func CleanPathSegment(segment string, tokenWasEmpty bool, maxBytes int) string {
	segment = norm.NFC.String(segment)
	segment = strings.ReplaceAll(segment, ": ", " - ")
	segment = strings.ReplaceAll(segment, ":", "-")
	segment = namingBadChars.Replace(segment)
	segment = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, segment)
	if tokenWasEmpty {
		segment = namingEmptyBrackets.ReplaceAllString(segment, "")
		segment = namingEdgeSeparator.ReplaceAllString(segment, "")
	}
	segment = strings.Join(strings.Fields(segment), " ")
	segment = strings.Trim(segment, " .")

	if maxBytes > 0 && len(segment) > maxBytes {
		cut := maxBytes
		for cut > 0 && !utf8.RuneStart(segment[cut]) {
			cut--
		}
		segment = strings.Trim(segment[:cut], " .")
	}

	stem := segment
	if dot := strings.IndexByte(stem, '.'); dot >= 0 {
		stem = stem[:dot]
	}
	if windowsReservedNames[strings.ToUpper(strings.TrimSpace(stem))] {
		segment += "_"
	}
	return segment
}

// NamingCheck is the result of validating a set of templates: per template, the
// error that blocks saving it and the warning that does not.
type NamingCheck struct {
	Errors   map[NamingField]string `json:"errors,omitempty"`
	Warnings map[NamingField]string `json:"warnings,omitempty"`
}

// OK reports whether the templates may be saved.
func (c NamingCheck) OK() bool { return len(c.Errors) == 0 }

// Err is the first blocking problem, in path order, as one error.
func (c NamingCheck) Err() error {
	for _, field := range namingFields {
		if msg, ok := c.Errors[field]; ok {
			return fmt.Errorf("%s: %s", field.label(), msg)
		}
	}
	return nil
}

func (c *NamingCheck) fail(field NamingField, msg string) {
	if c.Errors == nil {
		c.Errors = map[NamingField]string{}
	}
	if _, already := c.Errors[field]; !already {
		c.Errors[field] = msg
	}
}

func (c *NamingCheck) warn(field NamingField, msg string) {
	if c.Warnings == nil {
		c.Warnings = map[NamingField]string{}
	}
	c.Warnings[field] = msg
}

// ValidateNamingTemplates checks that templates render a layout the rest of the app
// can read back. Empty templates are the defaults and are checked as such.
//
// The structural rules are what keep a template from breaking anything that reads
// the layout:
//
//   - a token may only appear at its own level or below, so every album of an artist
//     shares one artist folder and every track of an album one album folder;
//   - the disc folder must render something discNumberFromFolderName reads back as the
//     right disc — the same function Lidarr's disc matching, folder attach and the
//     disc guard use — and so must contain {medium};
//   - the track file must contain {track}, or two tracks of one disc share a name.
//
// Rendering against the sample releases catches the rest: a template made only of
// tokens a release may not have ({Artist Disambiguation}) renders empty.
func ValidateNamingTemplates(templates models.NamingTemplates) NamingCheck {
	templates = templates.WithDefaults()
	var check NamingCheck

	for _, field := range namingFields {
		parts, err := parseNamingTemplate(field.template(templates))
		if err != nil {
			check.fail(field, err.Error())
			continue
		}
		hasToken := map[string]bool{}
		for _, part := range parts {
			if part.token == nil {
				continue
			}
			hasToken[part.token.Name] = true
			if part.token.Field.level() > field.level() {
				check.fail(field, fmt.Sprintf("%s belongs to the %s, and the %s is shared by more than one of those",
					part.raw, part.token.Field.label(), field.label()))
			}
		}
		switch field {
		case NamingMediaFolder:
			if !hasToken["medium"] {
				check.fail(field, "needs {medium}, or every disc of an album gets the same folder")
			}
		case NamingTrackFile:
			if !hasToken["track"] {
				check.fail(field, "needs {track}, or two tracks on one disc get the same name")
			}
		}
	}
	if !check.OK() {
		return check
	}

	for _, sample := range NamingSamples() {
		_, err := RenderLibraryPath(templates, sample.Release, sample.TrackID, sample.File)
		var empty *NamingEmptyError
		if errors.As(err, &empty) {
			check.fail(empty.Field, fmt.Sprintf("renders empty for %s — add a token every release has", sample.Label))
		}
	}

	// The disc folder is read back by the same function every consumer of the layout
	// uses. Rendering it for a second disc, rather than the first, is what proves it
	// carries the number and not just a lucky "1".
	multi := namingDiscProbe()
	if path, err := RenderLibraryPath(templates, multi.Release, multi.TrackID, multi.File); err == nil {
		if got := discNumberFromFolderName(path.Media); got != 2 {
			check.fail(NamingMediaFolder, fmt.Sprintf("renders %q for disc 2, which is not read back as disc 2 — start it with CD, Disc, Vinyl or another medium name followed by {medium}", path.Media))
		}
		stem := strings.TrimSuffix(path.File, multi.File.Ext)
		if m := trackNumberPattern.FindStringSubmatch(stem); m == nil || strings.TrimLeft(m[2], "0") != "5" {
			check.warn(NamingTrackFile, "doesn't start with the track number, so folder attach will pair these files by sort order rather than by number")
		}
	}
	return check
}
