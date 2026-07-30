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
	"time"

	"github.com/aunefyren/autotaggerr/logger"
	"github.com/aunefyren/autotaggerr/models"
	"github.com/aunefyren/autotaggerr/utilities"
)

// defaultProcessConcurrency is the fallback worker count when the config value is
// unset or invalid. Kept modest because FLAC rewrites are disk-bound.
const defaultProcessConcurrency = 4

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
	s.mu.Lock()
	s.m[albumName] = albumKey
	s.mu.Unlock()
}

// Snapshot returns a copy of the current entries, safe to iterate after the scan.
func (s *AlbumRefreshSet) Snapshot() map[string]string {
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
		return SetMP3Tags(filePath, metadata)
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
	correlation, err := ResolveCorrelation(filePath, lidarrClient, rootDir)
	if err != nil {
		return false, 0, err
	}
	unchanged, tagsWritten, _, err = TagResolvedFile(filePath, correlation, plexClient, refreshSet, rootDir, configFile)
	return unchanged, tagsWritten, err
}

// ResolveCorrelation determines the MusicBrainz release/track/recording IDs for a
// file. It prefers Lidarr (when a client is supplied) and falls back to the file's
// own embedded tags, mirroring the original ProcessTrackFile behavior. A nil
// lidarrClient reproduces the native (tags-only) path.
func ResolveCorrelation(filePath string, lidarrClient *LidarrClient, rootDir string) (models.Correlation, error) {
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
	logger.Log.Warn("Lidarr metadata data could be outdated")
	return false, 0, nil, fmt.Errorf("failed to tag file, track not found in release data for '%s'", response.ID)
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
	trackArtist := ""
	if !configFile.AutotaggerrIgnoreRedundantContributingArtists || len(track.ArtistCredit) > 1 {
		trackArtist = MusicBrainzArtistsArrayToString(track.ArtistCredit, configFile) // change the array into string to be tagged
	}
	logger.Log.Trace("track artists: " + trackArtist)

	trackArtistSemiColon := ""
	for index, artistCredit := range track.ArtistCredit {
		if index != 0 {
			trackArtistSemiColon += "; "
		}
		if configFile.AutotaggerrUseCurrentArtistName {
			trackArtistSemiColon += artistCredit.Artist.Name
		} else {
			trackArtistSemiColon += artistCredit.Name
		}
	}

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

	releaseArtistID := ""
	for index, artist := range track.ArtistCredit {
		if index != 0 {
			releaseArtistID += "; "
		}
		releaseArtistID += artist.Artist.ID
	}

	releaseGroupArtistID := ""
	for index, artist := range response.ArtistCredit {
		if index != 0 {
			releaseGroupArtistID += "; "
		}
		releaseGroupArtistID += artist.Artist.ID
	}

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

	isrcString := ""
	for index, isrc := range track.Recording.ISRCs {
		if index != 0 {
			isrcString += "; "
		}
		isrcString += isrc
	}

	recordLabelString := ""
	catalogString := ""
	if len(response.LabelInfo) > 0 {
		for index, recordLabel := range response.LabelInfo {
			if index != 0 && recordLabel.Label.Name != "" {
				recordLabelString += "; "
			}
			if index != 0 && recordLabel.CatalogNumber != "" {
				catalogString += "; "
			}
			recordLabelString += recordLabel.Label.Name
			catalogString += recordLabel.CatalogNumber
		}
	}

	metadata := models.FileTags{
		Artist:                trackArtist,
		ArtistSemicolon:       trackArtistSemiColon,
		AlbumArtist:           releaseArtist,
		OriginalDate:          releaseGroupDate,
		OriginalYear:          releaseGroupYear,
		ReleaseDate:           releaseDate,
		ReleaseYear:           releaseYear,
		Album:                 response.Title,
		Title:                 track.Title,
		ISRC:                  isrcString,
		Track:                 strconv.Itoa(track.Position),
		TrackTotal:            strconv.Itoa(len(media.Tracks)),
		DiscNumber:            strconv.Itoa(media.Position),
		DiscTotal:             strconv.Itoa(len(response.Media)),
		MBAlbumStatus:         strings.ToLower(response.Status),
		MBAlbumType:           ReleaseToAlbumType(response),
		MBAlbumReleaseCountry: response.Country,
		MBAlbumID:             response.ID,
		MBArtistID:            releaseArtistID,
		MBAlbumArtistID:       releaseGroupArtistID,
		MBReleaseGroupID:      response.ReleaseGroup.ID,
		MBReleaseTrackID:      track.ID,
		MBRecordingID:         track.Recording.ID,
		Script:                response.TextRepresentation.Script,
		RecordLabel:           recordLabelString,
		Media:                 media.Format,
		Barcode:               response.Barcode,
		ASIN:                  "",
		CatalogNumber:         catalogString,
		Author:                "",
		Composer:              "",
	}

	for _, genre := range response.ReleaseGroup.Genres {
		metadata.Genres = append(metadata.Genres, genre.Name)
	}

	return metadata, nil
}

// DiffFileTags reports, per tag, the file's current value versus what Autotaggerr
// would write — without touching the file. It reuses the same desired-tag maps and
// diff logic as the writer, so "changed" matches exactly what a scan would do.
func DiffFileTags(filePath string, metadata models.FileTags, configFile models.ConfigStruct) ([]models.TagDiffEntry, error) {
	ext := strings.ToLower(filepath.Ext(filePath))

	var desired map[string]string
	var existing map[string][]string
	var changed map[string]string

	switch ext {
	case ".flac":
		desired = buildFLACDesiredTags(metadata)
		m, err := getFlacTagsMap(filePath)
		if err != nil {
			return nil, err
		}
		existing = m
		changed, _ = utilities.DiffFlacTags(existing, desired, configFile)
	case ".mp3":
		desired = buildMP3DesiredTags(metadata)
		m, err := GetMP3Tags(filePath)
		if err != nil {
			return nil, err
		}
		existing = m
		changed, _ = utilities.DiffID3Tags(existing, desired)
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
		current := strings.Join(existing[up], "; ")
		want := desired[k]
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
	})

	return counter, unchangedFiles, allTagsWritten, errorFiles, refreshSet.Snapshot(), err
}

// WalkAndProcess walks root and, for every supported audio file, runs process in
// a bounded worker pool — aggregating counts, collecting per-file errors, logging
// progress, and periodically flushing batched caches. It is the single scan
// orchestrator shared by the legacy folder scan and the component pipeline; the
// process callback decides how each file is correlated and tagged.
func WalkAndProcess(root string, workers int, process func(path string) (unchanged bool, tagsWritten int, err error)) (
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
	totalFiles := 0
	filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() && supportedExtensions[strings.ToLower(filepath.Ext(path))] {
			totalFiles++
		}
		return nil
	})

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

	// Cache writes are batched (see cache.go): a background ticker flushes pending
	// changes during long scans so a crash only loses a bounded amount of freshly
	// fetched data, and a final flush runs once processing completes.
	defer FlushCaches()
	flushDone := make(chan struct{})
	var flushWG sync.WaitGroup
	flushWG.Add(1)
	go func() {
		defer flushWG.Done()
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-flushDone:
				return
			case <-ticker.C:
				FlushCaches()
			}
		}
	}()

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
	close(flushDone)
	flushWG.Wait()

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
