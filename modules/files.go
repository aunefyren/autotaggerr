package modules

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/aunefyren/autotaggerr/logger"
	"github.com/aunefyren/autotaggerr/models"
	"github.com/aunefyren/autotaggerr/utilities"
)

// defaultProcessConcurrency is the fallback worker count when the config value is
// unset or invalid. Kept modest because FLAC rewrites are disk-bound.
const defaultProcessConcurrency = 4

// ErrTrackNotInRelease means the resolved release does not contain the resolved
// track: the release ID and track ID disagree about which edition the file belongs
// to. It is its own error, not a generic tag failure, because the fix is specific —
// the manager's release selection and track mapping have drifted apart (common
// mid-migration), and a force re-correlate is what reconciles them. Callers use
// errors.Is to tell this apart from an unreadable file or a missing tool.
var ErrTrackNotInRelease = errors.New("track not found in release data")

// ErrDiscMismatch means the resolved track sits on a different medium than the file's
// own disc folder says, *and* the medium the folder names carries a track that cannot
// be told apart from the resolved one (same position, same recording or title). That
// is the multi-disc ambiguity: a release that repeats a track across two mediums has
// two equally plausible answers, the folder is the only evidence which one this file
// is, and picking the other one writes disc/track-total tags that never converge — so
// every scan rewrites the file (the endless-retag loop). Refusing is deliberate: the
// file is surfaced as a failure to fix (in the manager, or by renaming the folder)
// rather than silently churning forever. See docs/tagging.md.
var ErrDiscMismatch = errors.New("resolved track is on a different disc than the file's folder")

// ErrUnmatched means the manager owns identity for this file but could not match it,
// and tag fallback was refused. It is not a processing failure — the file is readable
// and the tools are present — so callers record it as an unmatched item rather than an
// error. It exists so a Lidarr-managed file Lidarr does not know about drops out of the
// collection instead of being tagged from its own (possibly stale) embedded tags.
var ErrUnmatched = errors.New("no manager match for file")

// AlbumRefreshSet is a concurrency-safe collection of albums (album name -> Plex
// album key) that changed during a scan and therefore need a Plex metadata
// refresh. It replaces the previous pass-a-map-and-return-it threading so that
// multiple files can be processed in parallel.
type AlbumRefreshSet struct {
	mu sync.Mutex
	m  map[string]string
}

// NewAlbumRefreshSet returns a set pre-seeded with the given entries (may be nil).
func NewAlbumRefreshSet(seed map[string]string) *AlbumRefreshSet {
	m := map[string]string{}
	for k, v := range seed {
		m[k] = v
	}
	return &AlbumRefreshSet{m: m}
}

func (s *AlbumRefreshSet) Add(albumName, albumKey string) {
	// A nil set means "nobody is collecting refreshes"; drop silently rather than
	// panicking. The Plex refresh is a non-fatal follow-up to a write that already
	// succeeded, so a missing collector must never take down the tagging path.
	if s == nil {
		return
	}
	s.mu.Lock()
	s.m[albumName] = albumKey
	s.mu.Unlock()
}

// Snapshot returns a copy of the current entries, safe to iterate after the scan.
func (s *AlbumRefreshSet) Snapshot() map[string]string {
	if s == nil {
		return map[string]string{}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make(map[string]string, len(s.m))
	for k, v := range s.m {
		out[k] = v
	}
	return out
}

// List of allowed audio file extensions
var supportedExtensions = map[string]bool{
	".flac": true,
	".mp3":  true,
	".m4a":  false,
	".ogg":  false,
	".wav":  false,
}

// extractMusicBrainzReleaseID extracts the MusicBrainz Album ID from either MP3 (ID3v2) or FLAC (Vorbis)
func ExtractMusicBrainzReleaseID(filePath string) (string, error) {
	ext := strings.ToLower(filepath.Ext(filePath))

	switch ext {
	case ".mp3":
		return extractFromID3v2(filePath, "release")
	case ".flac":
		return ExtractFLACTag(filePath, "", "release")
	default:
		return "", errors.New("unsupported file type")
	}
}

// extractMusicBrainzReleaseID extracts the MusicBrainz Track ID from either MP3 (ID3v2) or FLAC (Vorbis)
func ExtractMusicBrainzTrackID(filePath string) (string, error) {
	ext := strings.ToLower(filepath.Ext(filePath))

	switch ext {
	case ".mp3":
		return extractFromID3v2(filePath, "track")
	case ".flac":
		return ExtractFLACTag(filePath, "", "track")
	default:
		return "", errors.New("unsupported file type")
	}
}

// extractMusicBrainzReleaseID extracts the MusicBrainz Recording ID from either MP3 (ID3v2) or FLAC (Vorbis)
func ExtractMusicBrainzRecordingID(filePath string) (string, error) {
	ext := strings.ToLower(filepath.Ext(filePath))

	switch ext {
	case ".mp3":
		return extractFromID3v2(filePath, "recording")
	case ".flac":
		return ExtractFLACTag(filePath, "", "recording")
	default:
		return "", errors.New("unsupported file type")
	}
}

// extractMusicBrainzReleaseID extracts the track titlle from either MP3 (ID3v2) or FLAC (Vorbis)
func ExtractTrackTitle(filePath string) (string, error) {
	ext := strings.ToLower(filepath.Ext(filePath))

	switch ext {
	case ".mp3":
		return extractFromID3v2(filePath, "title")
	case ".flac":
		return ExtractFLACTag(filePath, "title", "")
	default:
		return "", errors.New("unsupported file type")
	}
}

func SetFileTags(filePath string, metadata models.FileTags, configFile models.ConfigStruct) (unchanged bool, tagsWritten int, changed []models.TagChange, err error) {
	ext := strings.ToLower(filepath.Ext(filePath))

	switch ext {
	case ".mp3":
		return SetMP3Tags(filePath, metadata, configFile)
	case ".flac":
		return SetFlacTags(filePath, metadata, configFile)
	default:
		return false, 0, nil, errors.New("unsupported file type")
	}
}

// ProcessTrackFile resolves a file's MusicBrainz correlation and writes tags. It
// is kept as the low-level single-file engine (used by the CLI path and tests);
// the component pipeline reuses ResolveCorrelation + TagResolvedFile directly.
func ProcessTrackFile(filePath string, lidarrClient *LidarrClient, plexClient *PlexClient, refreshSet *AlbumRefreshSet, rootDir string, configFile models.ConfigStruct) (unchanged bool, tagsWritten int, err error) {
	correlation, err := ResolveCorrelation(filePath, lidarrClient, rootDir, true)
	if err != nil {
		return false, 0, err
	}
	unchanged, tagsWritten, _, err = TagResolvedFile(filePath, correlation, plexClient, refreshSet, rootDir, configFile)
	return unchanged, tagsWritten, err
}

// ResolveCorrelation determines the MusicBrainz release/track/recording IDs for a
// file. It prefers Lidarr (when a client is supplied) and, when allowTagFallback is
// set, falls back to the file's own embedded tags. A nil lidarrClient reproduces the
// native (tags-only) path.
//
// allowTagFallback is how "Lidarr owns identity" is enforced: the Lidarr manager passes
// false when it has a client to ask, so a file Lidarr does not know about returns
// ErrUnmatched instead of being tagged from its own possibly-stale tags. The native
// manager and the legacy single-file engine pass true, because embedded tags are the
// only source they have.
func ResolveCorrelation(filePath string, lidarrClient *LidarrClient, rootDir string, allowTagFallback bool) (models.Correlation, error) {
	correlation := models.Correlation{}

	logger.Log.Debugf("processing track file: %s", filePath)

	if lidarrClient != nil {
		logger.Log.Debug("trying to get metadata details from Lidarr...")
		lidarrTrackObject, err := ResolveMetadataDetailsFromLidarr(lidarrClient, filePath, rootDir)
		if err != nil {
			logger.Log.Errorf("failed to retrieve track details from Lidarr for '%s'. error: %s", filePath, err.Error())
			return correlation, fmt.Errorf("failed to retrieve track details from Lidarr for '%s'", filePath)
		} else if lidarrTrackObject == nil {
			logger.Log.Warnf("Lidarr successfully executed, but found nothing for %s", filePath)
		} else {
			logger.Log.Debugf("Lidarr successfully executed for %s", filePath)
		}

		if lidarrTrackObject != nil {
			correlation.MBReleaseID = lidarrTrackObject.MBReleaseID
			correlation.MBReleaseTrackID = lidarrTrackObject.MBTrackID
			correlation.MBRecordingID = lidarrTrackObject.MBRecordingID
			correlation.TrackTitle = lidarrTrackObject.TrackTitle
			correlation.Source = models.CorrelationSourceLidarr
		}
	}

	if correlation.MBReleaseTrackID == "" || correlation.MBReleaseID == "" {
		if !allowTagFallback {
			// The manager owns identity and did not match this file. Refuse to read the
			// file's own tags: they may name a release the manager has since moved off,
			// and tagging from them is exactly the stale-identity drift this guards. Left
			// unmatched, the file drops out of the collection until the manager knows it.
			logger.Log.Debugf("manager returned no match for %s; not falling back to embedded tags", filePath)
			return correlation, fmt.Errorf("%w: %s", ErrUnmatched, filepath.Base(filePath))
		}
		// fall back to the file's own embedded MusicBrainz tags
		var err error
		correlation.MBReleaseID, err = ExtractMusicBrainzReleaseID(filePath)
		if err != nil {
			logger.Log.Error("failed to extract MB release ID. error: " + err.Error())
			return correlation, errors.New("failed to extract MB release ID")
		}

		correlation.MBReleaseTrackID, err = ExtractMusicBrainzTrackID(filePath)
		if err != nil {
			logger.Log.Error("failed to extract track MB ID. error: " + err.Error())
			return correlation, errors.New("failed to extract track MB ID")
		}

		correlation.MBRecordingID, err = ExtractMusicBrainzRecordingID(filePath)
		if err != nil {
			logger.Log.Error("failed to extract recording MB ID. error: " + err.Error())
			return correlation, errors.New("failed to extract recording MB ID")
		}

		correlation.TrackTitle, err = ExtractTrackTitle(filePath)
		if err != nil {
			logger.Log.Error("failed to extract track title. error: " + err.Error())
			return correlation, errors.New("failed to extract track title")
		}
		correlation.Source = models.CorrelationSourceTags
	}

	logger.Log.Debug("MB release ID: " + correlation.MBReleaseID)
	logger.Log.Debug("MB track ID: " + correlation.MBReleaseTrackID)
	logger.Log.Debug("track title: " + correlation.TrackTitle)
	logger.Log.Debug("MB recording ID: " + correlation.MBRecordingID)

	if correlation.MBReleaseTrackID == "" || correlation.MBReleaseID == "" {
		return correlation, errors.New("MB track or release ID field empty")
	}

	return correlation, nil
}

// TagResolvedFile fetches the release for an already-resolved correlation, finds
// the matching track, and writes the file's tags (diffed). It is the back half of
// the per-file pipeline, shared by ProcessTrackFile and the component pipeline.
// TagResolvedFile writes tags for an already-correlated file. changed is the
// field-level diff applied, for the Activity feed's per-file detail.
func TagResolvedFile(filePath string, correlation models.Correlation, plexClient *PlexClient, refreshSet *AlbumRefreshSet, rootDir string, configFile models.ConfigStruct) (unchanged bool, tagsWritten int, changed []models.TagChange, err error) {
	// Get MB data from API
	response, err := GetMusicBrainzRelease(correlation.MBReleaseID)
	if err != nil {
		// wrap the cause; the scan/single-file caller logs this together with the
		// file path, so no separate log line is needed here
		return false, 0, nil, fmt.Errorf("failed to get MB release data: %w", err)
	}
	logger.Log.Debug("MB title response: " + response.Title)

	// Go through API response for information
	for _, media := range response.Media {
		for _, track := range media.Tracks {
			if track.ID == correlation.MBReleaseTrackID {
				logger.Log.Debug("release track ID found in MB response")
				if err := verifyDiscFolder(filePath, correlation, track, media, response); err != nil {
					logger.Log.Warnf("refusing to tag '%s': %s", filePath, err.Error())
					return false, 0, nil, err
				}
				return ProcessTrackFileAfterMatch(
					filePath,
					nil,
					plexClient,
					refreshSet,
					rootDir,
					configFile,
					track,
					media,
					response)
			}
		}
	}

	logger.Log.Errorf("failed to tag file, track (track ID %s, release ID %s, title %s) not found in release data for '%s'", correlation.MBReleaseTrackID, correlation.MBReleaseID, correlation.TrackTitle, response.ID)
	logger.Log.Warn("the manager's release selection and track mapping disagree; a force re-correlate may reconcile them")
	return false, 0, nil, fmt.Errorf("%w: track %s is not in release %s — the manager's release and track mapping disagree; force re-correlate this artist to reconcile", ErrTrackNotInRelease, correlation.MBReleaseTrackID, response.ID)
}

// verifyDiscFolder is the last check before a write: it refuses a correlation that
// puts the file on a different disc than its own media folder does, when the two
// discs are genuinely confusable.
//
// It is narrow on purpose. Folder numbering does not have to agree with MusicBrainz
// medium numbering in general — a release whose medium 1 is a bonus DVD legitimately
// has its "CD 1" folder on medium 2 — so a bare number disagreement is not evidence of
// anything and must keep tagging. What *is* evidence is the disagreement plus a
// look-alike: the medium the folder names holds a track at the same position that is
// the same recording (or, failing that, has the same title). Then the two candidates
// are indistinguishable by anything except the folder, one of them was picked without
// looking at it, and the disc/track-total tags written can never match the file's
// folder — which is exactly the state that makes every scan rewrite the file.
//
// A manual correlation is exempt: someone looked at this file and said which track it
// is, and that outranks the folder it happens to sit in.
func verifyDiscFolder(
	filePath string,
	correlation models.Correlation,
	track models.Track,
	media models.MusicBrainzMedia,
	response models.MusicBrainzReleaseResponse,
) error {
	if correlation.Source == models.CorrelationSourceManual {
		return nil
	}
	if len(response.Media) < 2 {
		return nil // single medium: the folder's disc number is noise
	}

	disc := discFromFolder(filePath)
	if disc == 0 || disc == media.Position {
		return nil
	}

	for _, other := range response.Media {
		if other.Position != disc {
			continue
		}
		for _, candidate := range other.Tracks {
			if candidate.ID == track.ID || candidate.Position != track.Position {
				continue
			}
			sameRecording := candidate.Recording.ID != "" && candidate.Recording.ID == track.Recording.ID
			if !sameRecording && !strings.EqualFold(candidate.Title, track.Title) {
				continue
			}
			return fmt.Errorf(
				"%w: the file sits in disc %d but was resolved to track %s on disc %d of release %s, "+
					"where disc %d carries the same track (%q) — correct the file's track in the manager, "+
					"or rename the folder to the disc it really is",
				ErrDiscMismatch, disc, track.ID, media.Position, response.ID, disc, track.Title)
		}
	}

	return nil
}

func ProcessTrackFileAfterMatch(
	filePath string,
	lidarrClient *LidarrClient,
	plexClient *PlexClient,
	refreshSet *AlbumRefreshSet,
	rootDir string, configFile models.ConfigStruct,
	track models.Track,
	media models.MusicBrainzMedia,
	response models.MusicBrainzReleaseResponse,
) (
	unchanged bool,
	tagsWritten int,
	changed []models.TagChange,
	err error,
) {
	metadata, err := BuildFileTags(track, media, response, configFile)
	if err != nil {
		return false, 0, nil, err
	}

	// re-tag file with new information
	unchanged, tagsWritten, changed, err = SetFileTags(filePath, metadata, configFile)
	if err != nil {
		logger.Log.Error("failed to set file tags. error: " + err.Error())
		return unchanged, tagsWritten, changed, errors.New("failed to set FLAC artist tags")
	} else {
		logger.Log.Debug("file tagger finished")
	}

	changeString := "unchanged"
	if !unchanged {
		changeString = "changed. tags written: " + strconv.Itoa(tagsWritten)
	}

	if plexClient != nil && !unchanged {
		err = PlexRefreshForFile(unchanged, tagsWritten, refreshSet, *plexClient, response.Title, metadata.AlbumArtist, track.Title)
		if err != nil {
			logger.Log.Warn("failed to prepare Plex refresh for album. error: " + err.Error())
		}
	}

	logger.Log.Debug("file processed. " + changeString + ". path: '" + filePath + "'")
	return unchanged, tagsWritten, changed, nil

}

// BuildFileTags maps a matched MusicBrainz track/release onto the FileTags we
// write. It is pure (no I/O), so it can be reused both by the tagging path and by
// the read-only tag-diff endpoint that shows current vs desired without writing.
func BuildFileTags(
	track models.Track,
	media models.MusicBrainzMedia,
	response models.MusicBrainzReleaseResponse,
	configFile models.ConfigStruct,
) (models.FileTags, error) {
	// determine release artist
	releaseArtist := ""
	if len(response.ArtistCredit) > 0 {
		if configFile.AutotaggerrUseCurrentArtistName {
			// use current artist name if configured
			releaseArtist = response.ArtistCredit[0].Artist.Name
		} else {
			// use original release artist name if configured
			releaseArtist = response.ArtistCredit[0].Name
		}
	} else {
		return models.FileTags{}, errors.New("failed to determine album artist")
	}

	trackArtist := MusicBrainzArtistsArrayToString(track.ArtistCredit, configFile) // change the array into string to be tagged
	// "Redundant" means the track credit says nothing the album artist does not
	// already say — so it is decided by comparing the two strings, not by counting
	// credits. Counting was the old rule, and it read "one credited artist" as
	// "same as the album artist": on a compilation, or on a release credited to one
	// artist with a track credited solely to another, it emptied ARTIST on a track
	// whose artist the album artist does not name at all. Loose comparison so
	// punctuation or accent differences between the two credits do not keep a
	// genuinely redundant string.
	if configFile.AutotaggerrIgnoreRedundantContributingArtists && utilities.EqLoose(trackArtist, releaseArtist) {
		trackArtist = ""
	}
	logger.Log.Trace("track artists: " + trackArtist)

	// creditedNames lists the names behind an artist credit, honouring the profile's
	// choice between the artist's current name and the name as credited on this
	// particular release. Credited order is preserved: it is the order the release
	// itself states, and the diff compares it.
	creditedNames := func(credits []models.ArtistCredit) []string {
		names := make([]string, 0, len(credits))
		for _, artistCredit := range credits {
			if configFile.AutotaggerrUseCurrentArtistName {
				names = append(names, artistCredit.Artist.Name)
			} else {
				names = append(names, artistCredit.Name)
			}
		}
		return utilities.NormalizeTagValues(names)
	}

	creditedIDs := func(credits []models.ArtistCredit) []string {
		ids := make([]string, 0, len(credits))
		for _, artistCredit := range credits {
			ids = append(ids, artistCredit.Artist.ID)
		}
		return utilities.NormalizeTagValues(ids)
	}

	trackArtists := creditedNames(track.ArtistCredit)
	releaseArtists := creditedNames(response.ArtistCredit)
	trackArtistIDs := creditedIDs(track.ArtistCredit)
	releaseArtistIDs := creditedIDs(response.ArtistCredit)

	releaseTime, err := MusicBrainzDateStringToDateTime(response.Date)
	releaseYear := ""
	releaseDate := ""
	if err == nil {
		releaseYear = strconv.Itoa(releaseTime.Year())
		releaseDate = releaseTime.Format("2006-01-02")
	}

	releaseGroupTime, err := MusicBrainzDateStringToDateTime(response.ReleaseGroup.FirstReleaseDate)
	releaseGroupYear := ""
	releaseGroupDate := ""
	if err == nil {
		releaseGroupYear = strconv.Itoa(releaseGroupTime.Year())
		releaseGroupDate = releaseGroupTime.Format("2006-01-02")
	}

	// A release's labels and catalogue numbers are two lists off one LabelInfo, and
	// either half can be blank on any given entry. Collecting them separately and
	// letting NormalizeTagValues drop the blanks is what keeps a label with no catalogue
	// number (or the reverse) from shifting the other list — the previous index-based
	// join emitted a leading "; " whenever the first entry was the empty one, which
	// then never matched on read-back.
	labelNames := make([]string, 0, len(response.LabelInfo))
	catalogNumbers := make([]string, 0, len(response.LabelInfo))
	for _, recordLabel := range response.LabelInfo {
		labelNames = append(labelNames, recordLabel.Label.Name)
		catalogNumbers = append(catalogNumbers, recordLabel.CatalogNumber)
	}

	metadata := models.FileTags{
		Artist:                trackArtist,
		Artists:               trackArtists,
		AlbumArtist:           releaseArtist,
		AlbumArtists:          releaseArtists,
		OriginalDate:          releaseGroupDate,
		OriginalYear:          releaseGroupYear,
		ReleaseDate:           releaseDate,
		ReleaseYear:           releaseYear,
		Album:                 response.Title,
		Title:                 track.Title,
		ISRCs:                 utilities.NormalizeTagValues(track.Recording.ISRCs),
		Track:                 strconv.Itoa(track.Position),
		TrackTotal:            strconv.Itoa(len(media.Tracks)),
		DiscNumber:            strconv.Itoa(media.Position),
		DiscTotal:             strconv.Itoa(len(response.Media)),
		MBAlbumStatus:         strings.ToLower(response.Status),
		MBAlbumType:           ReleaseToAlbumType(response),
		MBAlbumReleaseCountry: response.Country,
		MBAlbumID:             response.ID,
		MBArtistIDs:           trackArtistIDs,
		MBAlbumArtistIDs:      releaseArtistIDs,
		MBReleaseGroupID:      response.ReleaseGroup.ID,
		MBReleaseTrackID:      track.ID,
		MBRecordingID:         track.Recording.ID,
		Script:                response.TextRepresentation.Script,
		RecordLabels:          utilities.NormalizeTagValues(labelNames),
		Media:                 media.Format,
		Barcode:               response.Barcode,
		CatalogNumbers:        utilities.NormalizeTagValues(catalogNumbers),
	}

	genres := make([]models.MusicBrainzNamedCount, 0, len(response.ReleaseGroup.Genres))
	for _, genre := range response.ReleaseGroup.Genres {
		genres = append(genres, models.MusicBrainzNamedCount{Name: genre.Name, Count: genre.Count})
	}
	metadata.Genres = selectGenres(genres, configFile.AutotaggerrMaxGenres)

	return metadata, nil
}

// selectGenres ranks a release group's genres by community vote and returns at
// most limit names.
//
// The sort is what makes the cap meaningful: MusicBrainz returns genres
// unordered, so without it "the top genres" would be whichever ones the API
// happened to serialize first — and the truncation would discard the popular
// genre as readily as the obscure one. Name is the tie-break purely so a release
// group with several equally-voted genres tags identically on every fetch; an
// unstable order here would re-tag the file on every scan.
//
// Genres are written exactly as MusicBrainz spells them, which is lower case
// ("acid jazz", "afro-cuban jazz"). Title-casing them is a transformation away
// from the source that also mangles the names it cannot know about — "UK garage"
// becomes "Uk Garage", "R&B" becomes "R&b" — so it is deliberately not done.
func selectGenres(genres []models.MusicBrainzNamedCount, limit int) []string {
	if limit < 1 {
		limit = models.DefaultMaxGenres
	}

	ranked := make([]models.MusicBrainzNamedCount, len(genres))
	copy(ranked, genres)
	sort.SliceStable(ranked, func(i, j int) bool {
		if ranked[i].Count != ranked[j].Count {
			return ranked[i].Count > ranked[j].Count
		}
		return ranked[i].Name < ranked[j].Name
	})

	names := make([]string, 0, limit)
	for _, genre := range ranked {
		name := utilities.NormalizeTagValue(genre.Name)
		if name == "" {
			continue
		}
		names = append(names, name)
		if len(names) == limit {
			break
		}
	}
	return names
}

// DiffFileTags reports, per tag, the file's current value versus what Autotaggerr
// would write — without touching the file. It reuses the same desired-tag maps and
// diff logic as the writer, so "changed" matches exactly what a scan would do.
func DiffFileTags(filePath string, metadata models.FileTags, configFile models.ConfigStruct) ([]models.TagDiffEntry, error) {
	ext := strings.ToLower(filepath.Ext(filePath))

	var desired map[string][]string
	var existing map[string][]string
	var changed map[string][]string

	switch ext {
	case ".flac":
		// Rendered, not raw: the preview has to be the bytes a scan would write, or
		// the UI shows a change the writer would not make (or hides one it would).
		desired = renderFLACTags(buildFLACDesiredTags(metadata))
		m, err := getFlacTagsMap(filePath)
		if err != nil {
			return nil, err
		}
		existing = m
		changed, _ = utilities.DiffFlacTags(existing, desired, configFile)
	case ".mp3":
		desired = renderMP3Tags(buildMP3DesiredTags(metadata), configFile)
		m, err := GetMP3Tags(filePath)
		if err != nil {
			return nil, err
		}
		existing = m
		changed, _ = utilities.DiffID3Tags(existing, desired, configFile)
	default:
		return nil, errors.New("unsupported file type")
	}

	keys := make([]string, 0, len(desired))
	for k := range desired {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	entries := make([]models.TagDiffEntry, 0, len(keys))
	for _, k := range keys {
		up := strings.ToUpper(k)
		current := utilities.JoinTagValues(existing[up])
		want := utilities.JoinTagValues(desired[k])
		if current == "" && want == "" {
			continue // nothing to show for an empty-on-both tag
		}
		_, isChanged := changed[up]
		entries = append(entries, models.TagDiffEntry{Key: k, Current: current, Desired: want, Changed: isChanged})
	}
	return entries, nil
}

func ScanFolderRecursive(root string, lidarrClient *LidarrClient, plexClient *PlexClient, albumsWhoNeedMetadataRefreshSoFar map[string]string, configFile models.ConfigStruct) (
	counter int,
	unchangedFiles int,
	allTagsWritten int,
	errorFiles []string,
	albumsWhoNeedMetadataRefresh map[string]string,
	err error,
) {
	refreshSet := NewAlbumRefreshSet(albumsWhoNeedMetadataRefreshSoFar)

	counter, unchangedFiles, allTagsWritten, errorFiles, err = WalkAndProcess(root, configFile.AutotaggerrProcessConcurrency, func(path string) (bool, int, error) {
		return ProcessTrackFile(path, lidarrClient, plexClient, refreshSet, root, configFile)
	}, nil)

	return counter, unchangedFiles, allTagsWritten, errorFiles, refreshSet.Snapshot(), err
}

// CountSupportedFiles walks root and counts the audio files a scan would process.
// It is the same enumeration WalkAndProcess uses for its own progress logging,
// exposed so a caller can size a progress bar across several roots before any of
// them starts. Walk errors are ignored, exactly as the scan tolerates them.
func CountSupportedFiles(root string) int {
	total := 0
	filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() && supportedExtensions[strings.ToLower(filepath.Ext(path))] {
			total++
		}
		return nil
	})
	return total
}

// WalkAndProcess walks root and, for every supported audio file, runs process in
// a bounded worker pool — aggregating counts, collecting per-file errors, logging
// progress, and periodically flushing batched caches. It is the single scan
// orchestrator shared by the legacy folder scan and the component pipeline; the
// process callback decides how each file is correlated and tagged.
//
// onFile, if non-nil, is called once per file after it is processed, with its path.
// It runs on the worker goroutine, so it must be cheap and safe for concurrent
// calls — the scan uses it to advance a live progress counter without the per-file
// hot path ever taking a lock.
func WalkAndProcess(root string, workers int, process func(path string) (unchanged bool, tagsWritten int, err error), onFile func(path string)) (
	counter int,
	unchangedFiles int,
	allTagsWritten int,
	errorFiles []string,
	err error,
) {
	errorFiles = []string{}

	// <1 falls back to the default; 1 reproduces the old serial behavior exactly
	if workers < 1 {
		workers = defaultProcessConcurrency
	}

	// first pass, count total supported files
	totalFiles := CountSupportedFiles(root)

	if totalFiles == 0 {
		logger.Log.Info("no supported files found in: " + root)
		return 0, 0, 0, errorFiles, nil
	}

	logger.Log.Infof("found %d supported files. starting processing with %d worker(s)...", totalFiles, workers)

	var (
		counterAtomic   atomic.Int64 // successfully processed files
		unchangedAtomic atomic.Int64
		tagsAtomic      atomic.Int64
		resultMu        sync.Mutex // guards errorFiles + nextProgress
		nextProgress    = 10       // progress thresholds (10%, 20%, ... 100%)
	)

	// No cache flushing here any more: every cache writes through as it is
	// populated (see cache.go), so a scan interrupted at minute 400 has already
	// persisted everything it fetched by minute 399.

	// second pass, actual processing — bounded worker pool
	sem := make(chan struct{}, workers)
	var wg sync.WaitGroup

	err = filepath.WalkDir(root, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() || !supportedExtensions[strings.ToLower(filepath.Ext(path))] {
			return nil
		}

		wg.Add(1)
		sem <- struct{}{} // blocks when the pool is full (backpressure)
		go func(path string) {
			defer wg.Done()
			defer func() { <-sem }()
			// Progress advances for every file visited, error or not, so the bar can
			// reach 100% on a scan that hits some failures. Deferred so the error
			// early-return below still counts the file as done.
			if onFile != nil {
				defer onFile(path)
			}

			unchanged, tagsWritten, procErr := process(path)
			if procErr != nil {
				logger.Log.Error("failed to process file '" + path + "'. error: " + procErr.Error())
				resultMu.Lock()
				errorFiles = append(errorFiles, path)
				resultMu.Unlock()
				return
			}

			done := counterAtomic.Add(1)
			if unchanged {
				unchangedAtomic.Add(1)
			}
			tagsAtomic.Add(int64(tagsWritten))

			// print intervals (guarded so concurrent workers don't double-log a threshold)
			progress := int(done) * 100 / totalFiles
			resultMu.Lock()
			if progress >= nextProgress {
				logger.Log.Info(fmt.Sprintf("progress: %d%% (%d/%d files)", progress, done, totalFiles))
				for progress >= nextProgress {
					nextProgress += 10
				}
			}
			resultMu.Unlock()
		}(path)

		return nil
	})

	wg.Wait()

	counter = int(counterAtomic.Load())
	unchangedFiles = int(unchangedAtomic.Load())
	allTagsWritten = int(tagsAtomic.Load())
	return counter, unchangedFiles, allTagsWritten, errorFiles, err
}

func ReleaseToAlbumType(release models.MusicBrainzReleaseResponse) string {
	if strings.Contains(strings.ToLower(release.Title), "remix") || strings.Contains(strings.ToLower(release.Disambiguation), "remix") {
		return "album; remix"
	}

	if len(release.ReleaseGroup.SecondaryTypes) == 0 {
		return strings.ToLower(release.ReleaseGroup.PrimaryType)
	}

	primarySecondary := release.ReleaseGroup.SecondaryTypes[0]
	switch strings.ToLower(primarySecondary) {
	case "soundtrack":
		return "album; soundtrack"
	case "compilation":
		return "album; compilation"
	case "remix":
		return "album; remix"
	case "live":
		return "album; live"
	case "demo":
		return "album; demo"
	}

	switch strings.ToLower(release.ReleaseGroup.PrimaryType) {
	case "album":
		return "album"
	case "ep":
		return "ep"
	case "single":
		return "single"
	}

	return ""
}
