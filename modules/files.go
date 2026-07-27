package modules

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/aunefyren/autotaggerr/logger"
	"github.com/aunefyren/autotaggerr/models"
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

func SetFileTags(filePath string, metadata models.FileTags, configFile models.ConfigStruct) (unchanged bool, tagsWritten int, err error) {
	ext := strings.ToLower(filepath.Ext(filePath))

	switch ext {
	case ".mp3":
		return SetMP3Tags(filePath, metadata)
	case ".flac":
		return SetFlacTags(filePath, metadata, configFile)
	default:
		return false, 0, errors.New("unsupported file type")
	}
}

func ProcessTrackFile(filePath string, lidarrClient *LidarrClient, plexClient *PlexClient, refreshSet *AlbumRefreshSet, rootDir string, configFile models.ConfigStruct) (unchanged bool, tagsWritten int, err error) {
	unchanged = false
	tagsWritten = 0
	mbReleaseID := ""
	mbTrackID := ""
	mbRecordingID := ""
	trackTitle := ""

	logger.Log.Debugf("processing track file: %s", filePath)

	if lidarrClient != nil {
		logger.Log.Debug("trying to get metadata details from Lidarr...")
		lidarrTrackObject, err := ResolveMetadataDetailsFromLidarr(lidarrClient, filePath, rootDir)
		if err != nil {
			logger.Log.Errorf("failed to retrieve track details from Lidarr for '%s'. error: %s", filePath, err.Error())
			return unchanged, tagsWritten, fmt.Errorf("failed to retrieve track details from Lidarr for '%s'", filePath)
		} else if lidarrTrackObject == nil {
			logger.Log.Warnf("Lidarr successfully executed, but found nothing for %s", filePath)
		} else {
			logger.Log.Debugf("Lidarr successfully executed for %s", filePath)
		}

		if lidarrTrackObject != nil {
			mbReleaseID = lidarrTrackObject.MBReleaseID
			mbTrackID = lidarrTrackObject.MBTrackID
			mbRecordingID = lidarrTrackObject.MBRecordingID
			trackTitle = lidarrTrackObject.TrackTitle
		}
	}

	if mbTrackID == "" || mbReleaseID == "" {
		// get MB release data from track
		mbReleaseID, err = ExtractMusicBrainzReleaseID(filePath)
		if err != nil {
			logger.Log.Error("failed to extract MB release ID. error: " + err.Error())
			return unchanged, tagsWritten, errors.New("failed to extract MB release ID")
		}

		// get MB data from track
		mbTrackID, err = ExtractMusicBrainzTrackID(filePath)
		if err != nil {
			logger.Log.Error("failed to extract track MB ID. error: " + err.Error())
			return unchanged, tagsWritten, errors.New("failed to extract track MB ID")
		}

		// get MB data from track
		mbRecordingID, err = ExtractMusicBrainzRecordingID(filePath)
		if err != nil {
			logger.Log.Error("failed to extract recording MB ID. error: " + err.Error())
			return unchanged, tagsWritten, errors.New("failed to extract recording MB ID")
		}

		// get track title from track
		trackTitle, err = ExtractTrackTitle(filePath)
		if err != nil {
			logger.Log.Error("failed to extract track title. error: " + err.Error())
			return unchanged, tagsWritten, errors.New("failed to extract track title")
		}
	}

	logger.Log.Debug("MB release ID: " + mbReleaseID)
	logger.Log.Debug("MB track ID: " + mbTrackID)
	logger.Log.Debug("track title: " + trackTitle)
	logger.Log.Debug("MB recording ID: " + mbRecordingID)

	if mbTrackID == "" || mbReleaseID == "" {
		return unchanged, tagsWritten, errors.New("MB track or release ID field empty")
	}

	// Get MB data from API
	response, err := GetMusicBrainzRelease(mbReleaseID)
	if err != nil {
		// wrap the cause; the scan/single-file caller logs this together with the
		// file path, so no separate log line is needed here
		return unchanged, tagsWritten, fmt.Errorf("failed to get MB release data: %w", err)
	}
	logger.Log.Debug("MB title response: " + response.Title)

	// Go through API response for information
	for _, media := range response.Media {
		for _, track := range media.Tracks {
			if track.ID == mbTrackID {
				logger.Log.Debug("release track ID found in MB response")
				unchanged, tagsWritten, err = ProcessTrackFileAfterMatch(
					filePath,
					lidarrClient,
					plexClient,
					refreshSet,
					rootDir,
					configFile,
					track,
					media,
					response)
				return
			}
		}
	}

	logger.Log.Errorf("failed to tag file, track (track ID %s, release ID %s, title %s) not found in release data for '%s'", mbTrackID, mbReleaseID, trackTitle, response.ID)
	logger.Log.Warn("Lidarr metadata data could be outdated")
	return unchanged, tagsWritten, fmt.Errorf("failed to tag file, track not found in release data for '%s'", response.ID)
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
	err error,
) {
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
	if releaseArtist == "" && len(response.ArtistCredit) > 0 {
		if configFile.AutotaggerrUseCurrentArtistName {
			// use current artist name if configured
			releaseArtist = response.ArtistCredit[0].Artist.Name
		} else {
			// use original release artist name if configured
			releaseArtist = response.ArtistCredit[0].Name
		}
	} else if releaseArtist == "" {
		return unchanged, tagsWritten, errors.New("failed to determine album artist")
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

	// re-tag file with new information
	unchanged, tagsWritten, err = SetFileTags(filePath, metadata, configFile)
	if err != nil {
		logger.Log.Error("failed to set file tags. error: " + err.Error())
		return unchanged, tagsWritten, errors.New("failed to set FLAC artist tags")
	} else {
		logger.Log.Debug("file tagger finished")
	}

	changeString := "unchanged"
	if !unchanged {
		changeString = "changed. tags written: " + strconv.Itoa(tagsWritten)
	}

	if plexClient != nil && !unchanged {
		err = PlexRefreshForFile(unchanged, tagsWritten, refreshSet, *plexClient, response.Title, releaseArtist, track.Title)
		if err != nil {
			logger.Log.Warn("failed to prepare Plex refresh for album. error: " + err.Error())
		}
	}

	logger.Log.Debug("file processed. " + changeString + ". path: '" + filePath + "'")
	return unchanged, tagsWritten, nil

}

func ScanFolderRecursive(root string, lidarrClient *LidarrClient, plexClient *PlexClient, albumsWhoNeedMetadataRefreshSoFar map[string]string, configFile models.ConfigStruct) (
	counter int,
	unchangedFiles int,
	allTagsWritten int,
	errorFiles []string,
	albumsWhoNeedMetadataRefresh map[string]string,
	err error,
) {
	originalRoot := root
	counter = 0
	unchangedFiles = 0
	allTagsWritten = 0
	errorFiles = []string{}

	// caches are loaded once at startup (see modules.LoadAllCaches) and kept warm
	// in memory across scans, so there is no per-scan disk read here.

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
		return counter, unchangedFiles, allTagsWritten, errorFiles, albumsWhoNeedMetadataRefreshSoFar, nil
	}

	// number of files to process in parallel; <1 falls back to the default and
	// 1 reproduces the old serial behavior exactly
	workers := configFile.AutotaggerrProcessConcurrency
	if workers < 1 {
		workers = defaultProcessConcurrency
	}
	logger.Log.Infof("found %d supported files. starting processing with %d worker(s)...", totalFiles, workers)

	refreshSet := NewAlbumRefreshSet(albumsWhoNeedMetadataRefreshSoFar)

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

			unchanged, tagsWritten, procErr := ProcessTrackFile(path, lidarrClient, plexClient, refreshSet, originalRoot, configFile)
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
	albumsWhoNeedMetadataRefresh = refreshSet.Snapshot()

	return counter, unchangedFiles, allTagsWritten, errorFiles, albumsWhoNeedMetadataRefresh, err
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
