package modules

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/aunefyren/autotaggerr/logger"
	"github.com/aunefyren/autotaggerr/models"
	"github.com/aunefyren/autotaggerr/utilities"
	"github.com/bogem/id3v2"
	"github.com/mewkiz/flac"
	"github.com/mewkiz/flac/meta"
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

func extractFromID3v2(filePath string, metadataType string) (string, error) {
	var keyName string
	switch metadataType {
	case "release":
		keyName = "MusicBrainz Release Id"
	case "track":
		keyName = "MusicBrainz Track Id"
	// add others if needed
	default:
		return "", errors.New("unsupported media type")
	}

	tagFile, err := id3v2.Open(filePath, id3v2.Options{Parse: true})
	if err != nil {
		return "", err
	}
	defer tagFile.Close()

	for _, frame := range tagFile.GetFrames("TXXX") {
		if uf, ok := frame.(id3v2.UserDefinedTextFrame); ok {
			if strings.EqualFold(strings.TrimSpace(uf.Description), keyName) {
				return strings.TrimSpace(uf.Value), nil
			}
		}
	}
	return "", nil
}

// ExtractFLACTag returns the value of a Vorbis comment key (case-insensitive).
// If key is empty, it will resolve from metadataType (e.g., "release" => MUSICBRAINZ_ALBUMID).
func ExtractFLACTag(filePath, key, metadataType string) (string, error) {
	if key == "" {
		var ok bool
		key, ok = utilities.MBVorbisKeyFor(metadataType)
		if !ok {
			return "", errors.New("unsupported or empty key/metadataType")
		}
	}
	key = strings.ToUpper(key)

	tags, err := getFlacTagsMap(filePath) // read all once
	if err != nil {
		return "", err
	}

	// return first non-empty match (Vorbis comments may have duplicates)
	if vals, ok := tags[key]; ok {
		for _, v := range vals {
			v = utilities.NormalizeTagValue(v)
			if v != "" {
				return v, nil
			}
		}
	}
	return "", nil
}

// getFlacTagsMap returns all Vorbis comments as KEY -> []values (uppercased keys).
func getFlacTagsMap(filePath string) (map[string][]string, error) {
	stream, err := flac.ParseFile(filePath)
	if err != nil {
		return nil, err
	}

	out := make(map[string][]string)
	for _, block := range stream.Blocks {
		if vc, ok := block.Body.(*meta.VorbisComment); ok {
			for _, kv := range vc.Tags {
				if len(kv) < 2 {
					continue
				}
				key := strings.ToUpper(strings.TrimSpace(kv[0]))
				val := utilities.NormalizeTagValue(kv[1])
				out[key] = append(out[key], val)
			}
		}
	}
	return out, nil
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

func SetFileTags(filePath string, metadata models.FileTags) (unchanged bool, tagsWritten int, err error) {
	ext := strings.ToLower(filepath.Ext(filePath))

	switch ext {
	case ".mp3":
		return SetMP3Tags(filePath, metadata)
	case ".flac":
		return SetFlacTags(filePath, metadata)
	default:
		return false, 0, errors.New("unsupported file type")
	}
}

// SetFlacTags updates multiple Vorbis comment tags on a FLAC file.
func SetFlacTags(filePath string, metadata models.FileTags) (unchanged bool, tagsWritten int, err error) {
	unchanged = false
	tagsWritten = 0

	desired := map[string]string{
		"ARTIST":       metadata.Artist,
		"ALBUMARTIST":  metadata.AlbumArtist,
		"GENRE":        metadata.Genre,
		"DATE":         metadata.ReleaseDate,
		"YEAR":         metadata.ReleaseYear,
		"ORIGINALDATE": metadata.OriginalDate,
		"RELEASEDATE":  metadata.ReleaseDate,
		"ALBUM":        metadata.Album,
		"TITLE":        metadata.Title,
		"TRACKNUMBER":  metadata.Track,
		"TRACKTOTAL":   metadata.TrackTotal,
		"DISCNUMBER":   metadata.DiscNumber,
		"DISCTOTAL":    metadata.DiscTotal,
		"ISRC":         metadata.ISRC,
	}

	existing, err := getFlacTagsMap(filePath)
	if err != nil {
		// Optional: keep going even if read fails, or return error
		return unchanged, tagsWritten, err
	}

	changes, hasChanges := utilities.DiffFlacTags(existing, desired)
	if !hasChanges {
		logger.Log.Info("no tag changes needed: " + filePath)
		return true, tagsWritten, nil
	}

	utf8Env := append(os.Environ(), "LANG=en_US.UTF-8", "LC_ALL=en_US.UTF-8")

	for key, value := range changes {
		// remove then set only the keys that changed
		removeCmd := exec.Command("metaflac", "--remove-tag="+key, filePath)
		removeCmd.Env = utf8Env
		if err := removeCmd.Run(); err != nil {
			logger.Log.Error(fmt.Sprintf("failed to remove tag %s: %s", key, err.Error()))
			return unchanged, tagsWritten, errors.New("failed to remove tag")
		}

		setCmd := exec.Command("metaflac", "--set-tag", fmt.Sprintf("%s=%s", key, value), filePath)
		setCmd.Env = utf8Env
		if err := setCmd.Run(); err != nil {
			logger.Log.Error(fmt.Sprintf("failed to set tag %s: %s", key, err.Error()))
			return unchanged, tagsWritten, errors.New("failed to set tag")
		} else {
			tagsWritten++
		}
	}

	return unchanged, tagsWritten, nil
}

func SetMP3Tags(filePath string, metadata models.FileTags) (unchanged bool, tagsWritten int, err error) {
	unchanged = false
	tagsWritten = 0

	desired := map[string]string{
		"ARTIST":      metadata.Artist,
		"ALBUMARTIST": metadata.AlbumArtist,
		"GENRE":       metadata.Genre,
		"ALBUM":       metadata.Album,
		"TITLE":       metadata.Title,
		"TRACKNUMBER": metadata.Track,
		"TRACKTOTAL":  metadata.TrackTotal,
		"DISCNUMBER":  metadata.DiscNumber,
		"DISCTOTAL":   metadata.DiscTotal,
		"ISRC":        metadata.ISRC,

		// Release
		"DATE": metadata.ReleaseDate, // maps to TDRC
		"YEAR": metadata.ReleaseYear, // maps to TYER

		// Original release
		"ORIGINALDATE": metadata.OriginalDate, // maps to TDOR
		"ORIGINALYEAR": metadata.OriginalYear, // maps toTORY (and TXXX backup)
	}

	existing, err := GetMP3Tags(filePath)
	if err != nil {
		return false, 0, fmt.Errorf("read mp3 tags failed: %w", err)
	}

	changes, hasChanges := utilities.DiffID3Tags(existing, desired)
	if !hasChanges {
		return true, 0, nil // unchanged
	}

	// Build ffmpeg args; only set changed fields (plus paired composite fields)
	args := []string{
		"-i", filePath,
		"-y",
		"-map_metadata", "0",
		"-codec", "copy",
		"-write_id3v1", "1", // legacy fallback
		"-id3v2_version", "4", // prefer v2.4 (gives TDOR/TDRC)
	}

	addMeta := func(k, v string) {
		args = append(args, "-metadata", fmt.Sprintf("%s=%s", k, v))
	}

	// Simple 1:1 fields
	if _, ok := changes["ARTIST"]; ok {
		addMeta("artist", desired["ARTIST"])
		tagsWritten++
	}
	if _, ok := changes["ALBUMARTIST"]; ok {
		addMeta("album_artist", desired["ALBUMARTIST"])
		tagsWritten++
	}
	if _, ok := changes["GENRE"]; ok {
		addMeta("genre", desired["GENRE"])
		tagsWritten++
	}

	// Release date/year
	if _, ok := changes["DATE"]; ok {
		addMeta("date", desired["DATE"])
		tagsWritten++
	}
	if _, ok := changes["YEAR"]; ok {
		addMeta("year", desired["YEAR"])
		tagsWritten++
	}

	// Original release date/year
	if _, ok := changes["ORIGINALDATE"]; ok {
		// v2.4 TDOR
		addMeta("originaldate", desired["ORIGINALDATE"])
		tagsWritten++
	}
	if _, ok := changes["ORIGINALYEAR"]; ok {
		// Explicit TORY for compatibility (v2.3 style)
		args = append(args, "-metadata", fmt.Sprintf("TORY=%s", desired["ORIGINALYEAR"]))
		// TXXX backup
		args = append(args, "-metadata", fmt.Sprintf("TXXX=ORIGINALYEAR:%s", desired["ORIGINALYEAR"]))
		tagsWritten++
	}

	if _, ok := changes["ALBUM"]; ok {
		addMeta("album", desired["ALBUM"])
		tagsWritten++
	}
	if _, ok := changes["TITLE"]; ok {
		addMeta("title", desired["TITLE"])
		tagsWritten++
	}

	// Composite: track (TRACKNUMBER/TRACKTOTAL)
	if _, nChanged := changes["TRACKNUMBER"]; nChanged || changes["TRACKTOTAL"] != "" {
		tn := desired["TRACKNUMBER"]
		tt := desired["TRACKTOTAL"]
		if tn != "" && tt != "" {
			addMeta("track", fmt.Sprintf("%s/%s", tn, tt))
		} else if tn != "" {
			addMeta("track", tn)
		}
		if nChanged {
			tagsWritten++
		}
		if _, tChanged := changes["TRACKTOTAL"]; tChanged {
			tagsWritten++
		}
	}

	// Composite: disc (DISCNUMBER/DISCTOTAL)
	if _, nChanged := changes["DISCNUMBER"]; nChanged || changes["DISCTOTAL"] != "" {
		dn := desired["DISCNUMBER"]
		dt := desired["DISCTOTAL"]
		if dn != "" && dt != "" {
			addMeta("disc", fmt.Sprintf("%s/%s", dn, dt))
		} else if dn != "" {
			addMeta("disc", dn)
		}
		if nChanged {
			tagsWritten++
		}
		if _, tChanged := changes["DISCTOTAL"]; tChanged {
			tagsWritten++
		}
	}

	// Custom TXXX frames
	if _, ok := changes["ISRC"]; ok && desired["ISRC"] != "" {
		addMeta("TXXX=ISRC:"+desired["ISRC"], "")
		// ffmpeg expects "TXXX=KEY:VALUE" as one value; we pass via previous call format:
		args[len(args)-1] = fmt.Sprintf("TXXX=ISRC:%s", desired["ISRC"])
		tagsWritten++
	}
	if _, ok := changes["TRACKTOTAL"]; ok && desired["TRACKTOTAL"] != "" {
		args = append(args, "-metadata", fmt.Sprintf("TXXX=TRACKTOTAL:%s", desired["TRACKTOTAL"]))
	}
	if _, ok := changes["DISCTOTAL"]; ok && desired["DISCTOTAL"] != "" {
		args = append(args, "-metadata", fmt.Sprintf("TXXX=DISCTOTAL:%s", desired["DISCTOTAL"]))
	}

	tempOutput := filePath + ".temp.mp3"
	args = append(args, tempOutput)

	cmd := exec.Command("ffmpeg", args...)
	// Ensure UTF-8 env if you’ve used that elsewhere:
	cmd.Env = append(os.Environ(), "LANG=en_US.UTF-8", "LC_ALL=en_US.UTF-8")
	cmd.Stderr = os.Stderr
	cmd.Stdout = os.Stdout

	if err := cmd.Run(); err != nil {
		return false, 0, fmt.Errorf("ffmpeg tagging failed: %w", err)
	}
	if err := os.Rename(tempOutput, filePath); err != nil {
		return false, 0, fmt.Errorf("failed to replace original file: %w", err)
	}

	return false, tagsWritten, nil
}

func ProcessTrackFile(filePath string, lidarrClient *LidarrClient, plexClient *PlexClient, albumsWhoNeedMetadataRefreshSoFar map[string]string, rootDir string) (unchanged bool, tagsWritten int, albumsWhoNeedMetadataRefresh map[string]string, err error) {
	unchanged = false
	tagsWritten = 0
	albumsWhoNeedMetadataRefresh = albumsWhoNeedMetadataRefreshSoFar

	// get MB release data from track
	mbReleaseID, err := ExtractMusicBrainzReleaseID(filePath)
	if err != nil {
		logger.Log.Error("failed to extract MB release ID. error: " + err.Error())
		return unchanged, tagsWritten, albumsWhoNeedMetadataRefresh, errors.New("failed to extract MB release ID")
	}
	logger.Log.Debug("MB release ID: " + mbReleaseID)

	// get MB data from track
	mbTrackID, err := ExtractMusicBrainzTrackID(filePath)
	if err != nil {
		logger.Log.Error("failed to extract track MB ID. error: " + err.Error())
		return unchanged, tagsWritten, albumsWhoNeedMetadataRefresh, errors.New("failed to extract track MB ID")
	}
	logger.Log.Debug("MB track ID: " + mbTrackID)

	if (mbTrackID == "" || mbReleaseID == "") && lidarrClient != nil {
		logger.Log.Info("MB track or release ID field empty. Trying Lidarr...")
		mbReleaseID, mbTrackID, err = ResolveMBReleaseAndTrackIDFromLidarr(lidarrClient, filePath, rootDir)
		if err != nil {
			logger.Log.Error("failed to retrieve track MB ID from Lidarr. error: " + err.Error())
			return unchanged, tagsWritten, albumsWhoNeedMetadataRefresh, errors.New("failed to retrieve track MB ID from Lidarr")
		}

		logger.Log.Trace("MB release ID: " + mbReleaseID)
		logger.Log.Trace("MB track ID: " + mbTrackID)
	}

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
	for mediaCount, media := range response.Media {
		for _, track := range media.Tracks {
			if track.ID == mbTrackID {
				logger.Log.Debug("release track ID found in MB response")
				trackArtist := MusicBrainzArtistsArrayToString(track.ArtistCredit)
				logger.Log.Debug("track artists: " + trackArtist)

				releaseArtist := ""
				if releaseArtist == "" && len(response.ArtistCredit) > 0 {
					releaseArtist = response.ArtistCredit[0].Name
				} else if releaseArtist == "" {
					return unchanged, tagsWritten, albumsWhoNeedMetadataRefresh, errors.New("failed to determine album artist")
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

				isrc := ""
				if len(track.Recording.ISRCs) > 0 {
					isrc = track.Recording.ISRCs[0]
				}

				metadata := models.FileTags{
					Artist:       trackArtist,
					AlbumArtist:  releaseArtist,
					Genre:        "",
					OriginalDate: releaseGroupDate,
					OriginalYear: releaseGroupYear,
					ReleaseDate:  releaseDate,
					ReleaseYear:  releaseYear,
					Album:        response.Title,
					Title:        track.Title,
					ISRC:         isrc,
					Track:        track.Number,
					TrackTotal:   strconv.Itoa(len(media.Tracks)),
					DiscNumber:   strconv.Itoa(mediaCount + 1),
					DiscTotal:    strconv.Itoa(len(response.Media)),
				}

				// re-tag file with new information
				unchanged, tagsWritten, err := SetFileTags(filePath, metadata)
				if err != nil {
					logger.Log.Error("failed to set file tags. error: " + err.Error())
					return unchanged, tagsWritten, albumsWhoNeedMetadataRefresh, errors.New("failed to set FLAC artist tags")
				} else {
					logger.Log.Debug("file tagged")
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
		}
	}

	return unchanged, tagsWritten, albumsWhoNeedMetadataRefresh, errors.New("failed to tag file, track not found in release data")
}

func ScanFolderRecursive(root string, lidarrClient *LidarrClient, plexClient *PlexClient, albumsWhoNeedMetadataRefreshSoFar map[string]string) (counter int, unchangedFiles int, allTagsWritten int, errorFiles int, albumsWhoNeedMetadataRefresh map[string]string, err error) {
	originalRoot := root
	counter = 0
	unchangedFiles = 0
	allTagsWritten = 0
	errorFiles = 0

	return counter, unchangedFiles, allTagsWritten, errorFiles, albumsWhoNeedMetadataRefresh, filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err // report permission errors, etc.
		}
		if d.IsDir() {
			return nil // keep walking
		}
		unchanged := false
		tagsWritten := 0

		if supportedExtensions[strings.ToLower(filepath.Ext(path))] {
			unchanged, tagsWritten, albumsWhoNeedMetadataRefresh, err = ProcessTrackFile(path, lidarrClient, plexClient, albumsWhoNeedMetadataRefreshSoFar, originalRoot)
			if err != nil {
				logger.Log.Error("failed to process file '" + path + "'. error: " + err.Error())
				errorFiles++
			} else {
				counter++
				if unchanged {
					unchangedFiles++
				} else {
					logger.Log.Trace("file changed: " + path)
				}
				allTagsWritten += tagsWritten
			}
		}
		return nil
	})
}

func GetMP3Tags(filePath string) (map[string][]string, error) {
	cmd := exec.Command("ffprobe",
		"-v", "quiet",
		"-print_format", "json",
		"-show_format",
		filePath,
	)
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("ffprobe failed: %w", err)
	}
	var fp models.FfprobeFormat
	if err := json.Unmarshal(out, &fp); err != nil {
		return nil, fmt.Errorf("ffprobe parse failed: %w", err)
	}
	res := make(map[string][]string)
	for k, v := range fp.Format.Tags {
		key := strings.ToLower(strings.TrimSpace(k))
		val := utilities.NormalizeTagValue(v)

		switch key {
		case "artist":
			res["ARTIST"] = append(res["ARTIST"], val)
		case "album_artist":
			res["ALBUMARTIST"] = append(res["ALBUMARTIST"], val)
		case "genre":
			res["GENRE"] = append(res["GENRE"], val)
		case "date", "tdrc":
			res["DATE"] = append(res["DATE"], val)
		case "year", "tyer":
			res["YEAR"] = append(res["YEAR"], val)
		case "originaldate", "tdor":
			res["ORIGINALDATE"] = append(res["ORIGINALDATE"], val)
		case "tory", "originalyear", "original_year":
			res["ORIGINALYEAR"] = append(res["ORIGINALYEAR"], val)
		case "album":
			res["ALBUM"] = append(res["ALBUM"], val)
		case "title":
			res["TITLE"] = append(res["TITLE"], val)
		case "track":
			// e.g. "3/12" or "3"
			parts := strings.SplitN(val, "/", 2)
			if len(parts) >= 1 {
				res["TRACKNUMBER"] = append(res["TRACKNUMBER"], utilities.NormalizeTagValue(parts[0]))
			}
			if len(parts) == 2 {
				res["TRACKTOTAL"] = append(res["TRACKTOTAL"], utilities.NormalizeTagValue(parts[1]))
			}
		case "disc":
			parts := strings.SplitN(val, "/", 2)
			if len(parts) >= 1 {
				res["DISCNUMBER"] = append(res["DISCNUMBER"], utilities.NormalizeTagValue(parts[0]))
			}
			if len(parts) == 2 {
				res["DISCTOTAL"] = append(res["DISCTOTAL"], utilities.NormalizeTagValue(parts[1]))
			}
		default:
			// Handle TXXX:* custom frames (e.g., TXXX:ISRC)
			if strings.HasPrefix(strings.ToUpper(key), "TXXX:") {
				custom := strings.ToUpper(strings.TrimPrefix(key, "TXXX:"))
				switch custom {
				case "ISRC", "TRACKTOTAL", "DISCTOTAL":
					res[custom] = append(res[custom], val)
				}
			}
		}
	}
	return res, nil
}

// try to retrieve the MB release from Lidarr
func ResolveMBReleaseAndTrackIDFromLidarr(cli *LidarrClient, trackPath string, rootDir string) (string, string, error) {
	mbTrackID := ""
	mbReleaseID := ""

	// derive the artist from the path folder
	artistName, err := utilities.ExtractArtistNameFromTrackFilePath(rootDir, trackPath)
	if err != nil {
		return "", "", err
	}

	artist, err := cli.FindArtistByName(artistName)
	if err != nil {
		return "", "", err
	}

	tf, err := cli.FindTrackFileByPath(artist.ID, trackPath, rootDir)
	if err != nil {
		return "", "", err
	}

	logger.Log.Trace("Lidarr track file: ")
	logger.Log.Trace(tf)

	tracks, err := cli.GetTracksByAlbumAndArtistID(artist.ID, tf.AlbumID)
	if err != nil {
		return "", "", err
	}

	for _, track := range tracks {
		if track.TrackFileID == tf.ID {
			mbTrackID = track.ForeignTrackID
		}
	}

	mbReleaseID, err = cli.GetMonitoredAlbumMBID(artist.ID, tf.AlbumID)
	if err != nil {
		return "", "", err
	}

	return mbReleaseID, mbTrackID, nil
}

func PlexRefreshForFile(unchanged bool, tagsWritten int, albumsWhoNeedMetadataRefreshInput map[string]string, plexClient PlexClient, albumTitle string, releaseArtist string, trackTitle string) (albumsWhoNeedMetadataRefresh map[string]string, err error) {
	albumsWhoNeedMetadataRefresh = albumsWhoNeedMetadataRefreshInput

	err = PlexLoadAlbumKeyCache()
	if err != nil {
		return albumsWhoNeedMetadataRefresh, err
	}

	albumKey := ""
	if cached, ok := plexAlbumKeyCache[albumTitle]; ok {
		logger.Log.Trace("cached entry found")
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
