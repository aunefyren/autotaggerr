package modules

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/aunefyren/autotaggerr/logger"
	"github.com/aunefyren/autotaggerr/models"
	"github.com/aunefyren/autotaggerr/utilities"
	"github.com/bogem/id3v2"
)

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

func extractFromID3v2(filePath string, metadataType string) (string, error) {
	var keyName string
	switch metadataType {
	case "release":
		keyName = "MusicBrainz Album Id"
	case "release_group":
		keyName = "MusicBrainz Release Group Id"
	case "track":
		keyName = "MusicBrainz Release Track Id"
	case "recording":
		keyName = "MusicBrainz Recording Id"
	case "title":
		keyName = "title"
	// add others if needed
	default:
		return "", errors.New("unsupported tag name for media type")
	}

	tagFile, err := id3v2.Open(filePath, id3v2.Options{Parse: true})
	if err != nil {
		return "", err
	}
	defer tagFile.Close()

	// simple tags
	switch keyName {
	case "title":
		for _, frame := range tagFile.GetFrames("TIT2") {
			if tf, ok := frame.(id3v2.TextFrame); ok {
				return strings.TrimSpace(tf.Text), nil
			}
		}
		return "", nil
	}

	// check for TXXX tags
	for _, frame := range tagFile.GetFrames("TXXX") {
		if uf, ok := frame.(id3v2.UserDefinedTextFrame); ok {
			if strings.EqualFold(strings.TrimSpace(uf.Description), keyName) {
				return strings.TrimSpace(uf.Value), nil
			}
		}
	}
	return "", nil
}

// Write MusicBrainz Album ID to an MP3 tag
func writeMusicBrainzAlbumIDToID3v2(mp3Path, mbid string) error {
	tagFile, err := id3v2.Open(mp3Path, id3v2.Options{Parse: true})
	if err != nil {
		return err
	}
	defer tagFile.Close()

	// Create UserDefinedTextFrame
	udtf := id3v2.UserDefinedTextFrame{
		Description: "MusicBrainz Album Id",
		Value:       mbid,
	}

	// Add or overwrite the frame
	tagFile.AddFrame(tagFile.CommonID("UserDefinedText"), udtf)

	// Save changes
	if err := tagFile.Save(); err != nil {
		return err
	}

	return nil
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

func ProcessTrackFile(filePath string, lidarrClient *LidarrClient, plexClient *PlexClient, albumsWhoNeedMetadataRefreshSoFar map[string]string, rootDir string, configFile models.ConfigStruct) (unchanged bool, tagsWritten int, albumsWhoNeedMetadataRefresh map[string]string, err error) {
	unchanged = false
	tagsWritten = 0
	albumsWhoNeedMetadataRefresh = albumsWhoNeedMetadataRefreshSoFar
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
			return unchanged, tagsWritten, albumsWhoNeedMetadataRefresh, fmt.Errorf("failed to retrieve track details from Lidarr for '%s'", filePath)
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
			return unchanged, tagsWritten, albumsWhoNeedMetadataRefresh, errors.New("failed to extract MB release ID")
		}

		// get MB data from track
		mbTrackID, err = ExtractMusicBrainzTrackID(filePath)
		if err != nil {
			logger.Log.Error("failed to extract track MB ID. error: " + err.Error())
			return unchanged, tagsWritten, albumsWhoNeedMetadataRefresh, errors.New("failed to extract track MB ID")
		}

		// get MB data from track
		mbRecordingID, err = ExtractMusicBrainzRecordingID(filePath)
		if err != nil {
			logger.Log.Error("failed to extract recording MB ID. error: " + err.Error())
			return unchanged, tagsWritten, albumsWhoNeedMetadataRefresh, errors.New("failed to extract recording MB ID")
		}

		// get track title from track
		trackTitle, err = ExtractTrackTitle(filePath)
		if err != nil {
			logger.Log.Error("failed to extract track title. error: " + err.Error())
			return unchanged, tagsWritten, albumsWhoNeedMetadataRefresh, errors.New("failed to extract track title")
		}
	}

	logger.Log.Debug("MB release ID: " + mbReleaseID)
	logger.Log.Debug("MB track ID: " + mbTrackID)
	logger.Log.Debug("track title: " + trackTitle)
	logger.Log.Debug("MB recording ID: " + mbRecordingID)

	if mbTrackID == "" || mbReleaseID == "" {
		return unchanged, tagsWritten, albumsWhoNeedMetadataRefresh, errors.New("MB track or release ID field empty")
	}

	// Get MB data from API
	response, err := GetMusicBrainzRelease(mbReleaseID)
	if err != nil {
		logger.Log.Error("failed to get MB release data. error: " + err.Error())
		return unchanged, tagsWritten, albumsWhoNeedMetadataRefresh, errors.New("failed to get MB release data")
	}
	logger.Log.Debug("MB title response: " + response.Title)

	// Go through API response for information
	for _, media := range response.Media {
		for _, track := range media.Tracks {
			if track.ID == mbTrackID {
				logger.Log.Debug("release track ID found in MB response")
				unchanged, tagsWritten, albumsWhoNeedMetadataRefresh, err = ProcessTrackFileAfterMatch(
					filePath,
					lidarrClient,
					plexClient,
					albumsWhoNeedMetadataRefresh,
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
	return unchanged, tagsWritten, albumsWhoNeedMetadataRefresh, fmt.Errorf("failed to tag file, track not found in release data for '%s'", response.ID)
}

func ProcessTrackFileAfterMatch(
	filePath string,
	lidarrClient *LidarrClient,
	plexClient *PlexClient,
	albumsWhoNeedMetadataRefreshSoFar map[string]string,
	rootDir string, configFile models.ConfigStruct,
	track models.Track,
	media models.MusicBrainzMedia,
	response models.MusicBrainzReleaseResponse,
) (
	unchanged bool,
	tagsWritten int,
	albumsWhoNeedMetadataRefresh map[string]string,
	err error,
) {
	albumsWhoNeedMetadataRefresh = albumsWhoNeedMetadataRefreshSoFar

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
		return unchanged, tagsWritten, albumsWhoNeedMetadataRefresh, errors.New("failed to determine album artist")
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
	}

	for _, genre := range response.ReleaseGroup.Genres {
		metadata.Genres = append(metadata.Genres, genre.Name)
	}

	// re-tag file with new information
	unchanged, tagsWritten, err = SetFileTags(filePath, metadata, configFile)
	if err != nil {
		logger.Log.Error("failed to set file tags. error: " + err.Error())
		return unchanged, tagsWritten, albumsWhoNeedMetadataRefresh, errors.New("failed to set FLAC artist tags")
	} else {
		logger.Log.Debug("file tagger finished")
	}

	changeString := "unchanged"
	if !unchanged {
		changeString = "changed. tags written: " + strconv.Itoa(tagsWritten)
	}

	if plexClient != nil && !unchanged {
		albumsWhoNeedMetadataRefresh, err = PlexRefreshForFile(unchanged, tagsWritten, albumsWhoNeedMetadataRefresh, *plexClient, response.Title, releaseArtist, track.Title)
		if err != nil {
			logger.Log.Warn("failed to prepare Plex refresh for album. error: " + err.Error())
		}
	}

	logger.Log.Debug("file processed. " + changeString + ". path: '" + filePath + "'")
	return unchanged, tagsWritten, albumsWhoNeedMetadataRefresh, nil

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

	// load cache into memory
	err = MusicbrainzLoadCache()
	if err != nil {
		logger.Log.Error("failed to load release cache. error: " + err.Error())
		return counter, unchangedFiles, allTagsWritten, errorFiles, albumsWhoNeedMetadataRefreshSoFar, errors.New("failed to load release cache")
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
		return counter, unchangedFiles, allTagsWritten, errorFiles, albumsWhoNeedMetadataRefreshSoFar, nil
	}

	logger.Log.Info(fmt.Sprintf("found %d supported files. starting processing...", totalFiles))

	// track progress thresholds (10%, 20%, ... 100%)
	nextProgress := 10

	albumsWhoNeedMetadataRefresh = albumsWhoNeedMetadataRefreshSoFar

	// second pass, actual processing
	err = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}

		unchanged := false
		tagsWritten := 0

		if supportedExtensions[strings.ToLower(filepath.Ext(path))] {
			unchanged, tagsWritten, albumsWhoNeedMetadataRefresh, err = ProcessTrackFile(path, lidarrClient, plexClient, albumsWhoNeedMetadataRefresh, originalRoot, configFile)
			if err != nil {
				logger.Log.Error("failed to process file '" + path + "'. error: " + err.Error())
				errorFiles = append(errorFiles, path)
			} else {
				counter++
				if unchanged {
					unchangedFiles++
				}
				allTagsWritten += tagsWritten

				// print intervals
				progress := (counter * 100) / totalFiles
				if progress >= nextProgress {
					logger.Log.Info(fmt.Sprintf("progress: %d%% (%d/%d files)", progress, counter, totalFiles))
					nextProgress += 10
				}
			}
		}
		return nil
	})

	return counter, unchangedFiles, allTagsWritten, errorFiles, albumsWhoNeedMetadataRefresh, err
}

// try to retrieve the MB release from Lidarr
func ResolveMetadataDetailsFromLidarr(cli *LidarrClient, trackPath string, rootDir string) (*models.LidarrTrackMetadataDetails, error) {
	// derive the artist from the path folder
	artistName, err := utilities.ExtractArtistNameFromTrackFilePath(rootDir, trackPath)
	if err != nil {
		return nil, err
	}
	logger.Log.Debugf("artist name found: %s", artistName)

	artists, err := cli.FindArtistByName(artistName)
	if err != nil {
		return nil, err
	} else if len(artists) > 1 {
		logger.Log.Warnf("%d artists found by that name, checking all and returning first match", len(artists))
	}

	for _, artist := range artists {
		logger.Log.Debugf("checking for artist: %s (%d)", artist.Name, artist.ID)
		lidarrTrackMetadataDetails := models.LidarrTrackMetadataDetails{}

		tf, err := cli.FindTrackFileByPath(artist.ID, trackPath, rootDir)
		if err != nil {
			return nil, err
		} else if tf == nil {
			logger.Log.Warn("tracks not found in Lidarr by file path")
			continue
		}

		logger.Log.Tracef("Lidarr track file found: %d", tf.ID)

		tracks, err := cli.GetTracksByAlbumAndArtistID(artist.ID, tf.AlbumID)
		if err != nil {
			return &lidarrTrackMetadataDetails, err
		} else if tracks == nil {
			logger.Log.Warn("tracks not found in Lidarr by album and artist")
			continue
		} else if len(tracks) < 1 {
			logger.Log.Warn("tracks list found in Lidarr by album and artist is empty")
		}

		found := false
		for _, track := range tracks {
			logger.Log.Tracef("comparing track ID %d, file ID %d", track.ID, *track.TrackFileID)
			if track.TrackFileID != nil && *track.TrackFileID == tf.ID {
				logger.Log.Tracef("Lidarr track found: %d", track.ID)
				lidarrTrackMetadataDetails.MBTrackID = track.ForeignTrackID
				lidarrTrackMetadataDetails.MBRecordingID = track.ForeignRecordingID
				lidarrTrackMetadataDetails.TrackTitle = track.Title
				found = true
				break
			}
		}

		if !found {
			logger.Log.Warn("track not found in tracks returned by Lidarr")
			continue
		}

		mbReleaseID, err := cli.GetMonitoredAlbumMBID(artist.ID, tf.AlbumID)
		if err != nil {
			return &lidarrTrackMetadataDetails, err
		} else if mbReleaseID == nil {
			logger.Log.Warn("MusicBrainz Release ID not found in Lidarr by album ID")
			continue
		}

		lidarrTrackMetadataDetails.MBReleaseID = *mbReleaseID

		logger.Log.Debug("found Lidarr details")
		return &lidarrTrackMetadataDetails, nil
	}

	logger.Log.Warn("no artists in Lidarr had the matching tracks for file")
	return nil, nil
}

func PlexRefreshForFile(unchanged bool, tagsWritten int, albumsWhoNeedMetadataRefreshInput map[string]string, plexClient PlexClient, albumTitle string, releaseArtist string, trackTitle string) (albumsWhoNeedMetadataRefresh map[string]string, err error) {
	albumsWhoNeedMetadataRefresh = albumsWhoNeedMetadataRefreshInput

	err = PlexLoadAlbumKeyCache()
	if err != nil {
		return albumsWhoNeedMetadataRefresh, err
	}

	albumKey := ""
	if cached, ok := plexAlbumKeyCache[albumTitle]; ok {
		logger.Log.Trace("cached entry for Plex Album key found")
		if time.Since(cached.Timestamp) < plexAlbumKeyCacheDuration {
			logger.Log.Debug("returning cached album key for album: " + albumTitle)
			albumKey = cached.AlbumKey
		}
	} else {
		sectionID, err := plexClient.FindMusicSectionID()
		if err != nil {
			logger.Log.Error("failed to find Plex music section ID. error: " + err.Error())
			return albumsWhoNeedMetadataRefresh, errors.New("failed to find Plex music section ID")
		}

		artistKey, err := plexClient.FindArtistKey(sectionID, releaseArtist)
		if err != nil {
			logger.Log.Error("failed to find Plex artist key for '" + releaseArtist + "'. error: " + err.Error())
			return albumsWhoNeedMetadataRefresh, errors.New("failed to find Plex artist key for '" + releaseArtist + "'")
		}

		logger.Log.Trace(artistKey + " - " + albumTitle)

		albumKey, err := plexClient.ResolveAlbumKeyInSection(sectionID, releaseArtist, albumTitle, trackTitle)
		if err != nil {
			logger.Log.Error("failed to find Plex album key. error: " + err.Error())
			return albumsWhoNeedMetadataRefresh, errors.New("failed to find Plex album key")
		} else {
			logger.Log.Trace(albumKey)
		}

		// add album key to cache
		plexAlbumKeyCache[albumTitle] = models.PlexAlbumKeyCache{
			AlbumKey:  albumKey,
			Timestamp: time.Now(),
		}

		// save new cache
		err = PlexSaveAlbumKeyCache()
		if err != nil {
			return albumsWhoNeedMetadataRefresh, err
		}
	}

	if !unchanged && tagsWritten > 0 {
		albumsWhoNeedMetadataRefresh[albumTitle] = albumKey
	}

	return
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
