package modules

import (
	"context"
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

// lidarrUnmatchedReason is what an unmatched file in a Lidarr library is told. By the
// time a file is unmatched its path below the root, its artist folder and its own tags
// have all been tried (see lidarr_match.go), so where it sits is no longer the reason:
// Lidarr has no track file there. Both fixes are Lidarr's — it owns identity here.
const lidarrUnmatchedReason = "Lidarr has no track file at this path. If Lidarr never imported it, import it there " +
	"(Wanted → Manual Import); if it was moved since Lidarr last looked, refresh the artist in Lidarr."

// UnmatchedReason is the explanation an ErrUnmatched carries, without the sentinel and
// the file name — what the index keeps and the Items page shows. "" for any other error.
func UnmatchedReason(err error) string {
	if !errors.Is(err, ErrUnmatched) {
		return ""
	}
	if _, reason, ok := strings.Cut(err.Error(), " — "); ok {
		return reason
	}
	return ""
}

// AlbumRefresh is one album a run changed, as one media server knows it.
type AlbumRefresh struct {
	// ServerID is the media server row; Key is that server's album key, which means
	// nothing on another server.
	ServerID string
	Name     string
	Key      string
}

// albumRefreshKey identifies an album by server and key rather than by name: two
// albums can share a title, and one album is one refresh per server that holds it.
type albumRefreshKey struct{ serverID, key string }

// AlbumRefreshSet is a concurrency-safe collection of the albums that changed during
// a run and therefore need a metadata refresh, per media server. It replaces the
// previous pass-a-map-and-return-it threading so that multiple files can be
// processed in parallel.
type AlbumRefreshSet struct {
	mu sync.Mutex
	m  map[albumRefreshKey]string
}

// NewAlbumRefreshSet returns an empty set.
func NewAlbumRefreshSet() *AlbumRefreshSet {
	return &AlbumRefreshSet{m: map[albumRefreshKey]string{}}
}

func (s *AlbumRefreshSet) Add(serverID, albumName, albumKey string) {
	// A nil set means "nobody is collecting refreshes"; drop silently rather than
	// panicking. The Plex refresh is a non-fatal follow-up to a write that already
	// succeeded, so a missing collector must never take down the tagging path.
	if s == nil {
		return
	}
	s.mu.Lock()
	s.m[albumRefreshKey{serverID, albumKey}] = albumName
	s.mu.Unlock()
}

// Snapshot returns a copy of the current entries, safe to iterate after the scan, in
// a stable order — by album name, then server — so two runs over the same set read
// the same way.
func (s *AlbumRefreshSet) Snapshot() []AlbumRefresh {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	out := make([]AlbumRefresh, 0, len(s.m))
	for k, name := range s.m {
		out = append(out, AlbumRefresh{ServerID: k.serverID, Name: name, Key: k.key})
	}
	s.mu.Unlock()
	sort.Slice(out, func(i, j int) bool {
		if out[i].Name != out[j].Name {
			return out[i].Name < out[j].Name
		}
		if out[i].ServerID != out[j].ServerID {
			return out[i].ServerID < out[j].ServerID
		}
		return out[i].Key < out[j].Key
	})
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

// IsSupportedAudioFile reports whether a path has an extension the pipeline
// processes — the same test the walk applies, for callers that walk on their own.
func IsSupportedAudioFile(path string) bool {
	return supportedExtensions[strings.ToLower(filepath.Ext(path))]
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

func SetFileTags(filePath string, metadata models.FileTags, tagger models.TaggerSettings) (unchanged bool, tagsWritten int, changed []models.TagChange, err error) {
	ext := strings.ToLower(filepath.Ext(filePath))

	switch ext {
	case ".mp3":
		return SetMP3Tags(filePath, metadata, tagger)
	case ".flac":
		return SetFlacTags(filePath, metadata, tagger)
	default:
		return false, 0, nil, errors.New("unsupported file type")
	}
}

// ProcessTrackFile resolves a file's MusicBrainz correlation and writes tags. It
// is kept as the low-level single-file engine (used by the CLI path and tests);
// the component pipeline reuses ResolveCorrelation + TagResolvedFile directly.
func ProcessTrackFile(filePath string, lidarrClient *LidarrClient, plexClient PlexTargets, refreshSet *AlbumRefreshSet, rootDir string, tagger models.TaggerSettings) (unchanged bool, tagsWritten int, err error) {
	correlation, err := ResolveCorrelation(filePath, lidarrClient, rootDir, true)
	if err != nil {
		return false, 0, err
	}
	unchanged, tagsWritten, _, err = TagResolvedFile(filePath, correlation, plexClient, refreshSet, rootDir, tagger)
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
	return resolveCorrelation(filePath, lidarrClient, rootDir, allowTagFallback, false)
}

// ResolveCorrelationFresh is ResolveCorrelation for a file whose bytes changed since
// the index last saw it: Lidarr is asked through
// ResolveMetadataDetailsFromLidarrFresh, which trusts no cached answer older than a
// couple of minutes, matching or not.
func ResolveCorrelationFresh(filePath string, lidarrClient *LidarrClient, rootDir string, allowTagFallback bool) (models.Correlation, error) {
	return resolveCorrelation(filePath, lidarrClient, rootDir, allowTagFallback, true)
}

func resolveCorrelation(filePath string, lidarrClient *LidarrClient, rootDir string, allowTagFallback, fresh bool) (models.Correlation, error) {
	correlation := models.Correlation{}

	logger.Log.Debugf("processing track file: %s", filePath)

	if lidarrClient != nil {
		logger.Log.Debug("trying to get metadata details from Lidarr...")
		resolve := ResolveMetadataDetailsFromLidarr
		if fresh {
			resolve = ResolveMetadataDetailsFromLidarrFresh
		}
		lidarrTrackObject, err := resolve(lidarrClient, filePath, rootDir)
		if err != nil {
			logger.Log.Errorf("failed to retrieve track details from Lidarr for '%s'. error: %s", filePath, err.Error())
			// Wrap, do not replace. This error is what recordItem stores on the item and
			// what the Items page and Activity feed show, so dropping the cause here left
			// every Lidarr failure — auth, a proxy login page, a folder mismatch, a
			// malformed path — rendering as the same sentence with nothing to act on.
			return correlation, fmt.Errorf("failed to retrieve track details from Lidarr for '%s': %w", filePath, err)
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
			correlation.ReleaseGroup = lidarrTrackObject.ReleaseGroup
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
			return correlation, fmt.Errorf("%w: %s — %s", ErrUnmatched, filepath.Base(filePath), lidarrUnmatchedReason)
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
func TagResolvedFile(filePath string, correlation models.Correlation, plexClient PlexTargets, refreshSet *AlbumRefreshSet, rootDir string, tagger models.TaggerSettings) (unchanged bool, tagsWritten int, changed []models.TagChange, err error) {
	return TagCorrelatedFile(filePath, &correlation, plexClient, refreshSet, rootDir, tagger)
}

// TagCorrelatedFile is TagResolvedFile for a caller that records the correlation
// afterwards: when the track had to be re-matched (rematchRebuiltTrack), the
// correlation is updated to the track the file was actually tagged as, so the index
// stores the identity now in the file rather than the one that no longer exists.
func TagCorrelatedFile(filePath string, correlation *models.Correlation, plexClient PlexTargets, refreshSet *AlbumRefreshSet, rootDir string, tagger models.TaggerSettings) (unchanged bool, tagsWritten int, changed []models.TagChange, err error) {
	// Get MB data from API
	response, err := GetMusicBrainzRelease(correlation.MBReleaseID)
	if err != nil {
		// wrap the cause; the scan/single-file caller logs this together with the
		// file path, so no separate log line is needed here
		return false, 0, nil, fmt.Errorf("failed to get MB release data: %w", err)
	}
	logger.Log.Debug("tagging from release: " + response.Title)

	track, media, ok := matchCorrelatedTrack(response, *correlation)
	if !ok && response.ID == correlation.MBReleaseID && rematchable(correlation.Source) {
		if track, media, ok = rematchRebuiltTrack(filePath, response, *correlation); ok {
			logger.Log.Warnf("track %s is no longer in release %s; re-matched '%s' to track %s",
				correlation.MBReleaseTrackID, response.ID, filePath, track.ID)
			correlation.MBReleaseTrackID = track.ID
			correlation.MBRecordingID = track.Recording.ID
			correlation.TrackTitle = track.Title
			correlation.Source = models.CorrelationSourceRematched
		}
	}
	if !ok {
		logger.Log.Errorf("failed to tag file, track (track ID %s, release ID %s, title %s) not found in release data for '%s'", correlation.MBReleaseTrackID, correlation.MBReleaseID, correlation.TrackTitle, response.ID)
		if response.ID != correlation.MBReleaseID {
			// Re-correlating cannot help here: the manager is what still holds the
			// merged-away IDs, so it hands the same stale pair back on every pass.
			return false, 0, nil, fmt.Errorf("%w: track %s belongs to release %s, which MusicBrainz merged into %s, and no track there has the same recording or title — this clears once the manager's metadata picks up the merge", ErrTrackNotInRelease, correlation.MBReleaseTrackID, correlation.MBReleaseID, response.ID)
		}
		if rematchable(correlation.Source) {
			// Nothing upstream supplies a fresher pair for a file identified by its own
			// tags, so say why the re-match refused rather than point at a manager.
			return false, 0, nil, fmt.Errorf("%w: track %s is no longer in release %s, and neither its recording nor its disc and position (with the same title) identify one track there — attach the file by hand", ErrTrackNotInRelease, correlation.MBReleaseTrackID, response.ID)
		}
		logger.Log.Warn("the manager's release selection and track mapping disagree; a force re-correlate may reconcile them")
		return false, 0, nil, fmt.Errorf("%w: track %s is not in release %s — the manager's release and track mapping disagree; force re-correlate this artist to reconcile", ErrTrackNotInRelease, correlation.MBReleaseTrackID, response.ID)
	}

	if err := verifyDiscFolder(filePath, *correlation, track, media, response); err != nil {
		logger.Log.Warnf("refusing to tag '%s': %s", filePath, err.Error())
		return false, 0, nil, err
	}
	return ProcessTrackFileAfterMatch(
		filePath,
		nil,
		plexClient,
		refreshSet,
		rootDir,
		tagger,
		track,
		media,
		response)
}

// matchCorrelatedTrack locates the correlation's track in the fetched release.
//
// The exact track ID is the only match for a release fetched under its own ID. The
// fallback exists for the one case where a miss is expected rather than a sign of
// drift: the correlation names a release MusicBrainz has merged away. MusicBrainz
// follows the redirect and answers with the survivor, whose tracks carry the
// survivor's IDs — the merged-away track IDs are redirects now and appear nowhere
// in the payload. A manager whose metadata lags MusicBrainz (Lidarr's does) keeps
// handing out the old pair, so an exact match fails on every pass and re-correlating
// only fetches the same pair again.
//
// A merge renames, it does not substitute, so the same song is in the survivor: by
// recording first, since merges keep recordings, and by title when the recording
// was merged as well. Either has to name exactly one track. A recording or title
// that repeats is the multi-disc ambiguity verifyDiscFolder guards against, and
// choosing between the candidates here would be a guess — so a repeat refuses
// rather than picks.
func matchCorrelatedTrack(response models.MusicBrainzReleaseResponse, correlation models.Correlation) (models.Track, models.MusicBrainzMedia, bool) {
	for _, media := range response.Media {
		for _, track := range media.Tracks {
			if track.ID == correlation.MBReleaseTrackID {
				logger.Log.Debug("release track ID found in the release")
				return track, media, true
			}
		}
	}
	if response.ID == correlation.MBReleaseID {
		return models.Track{}, models.MusicBrainzMedia{}, false
	}

	match := func(same func(models.Track) bool) (models.Track, models.MusicBrainzMedia, bool) {
		var found models.Track
		var foundMedia models.MusicBrainzMedia
		hits := 0
		for _, media := range response.Media {
			for _, track := range media.Tracks {
				if same(track) {
					found, foundMedia = track, media
					hits++
				}
			}
		}
		return found, foundMedia, hits == 1
	}

	if correlation.MBRecordingID != "" {
		if track, media, ok := match(func(t models.Track) bool { return t.Recording.ID == correlation.MBRecordingID }); ok {
			logger.Log.Infof("release %s was merged into %s; matched track %s by recording %s", correlation.MBReleaseID, response.ID, track.ID, correlation.MBRecordingID)
			return track, media, true
		}
	}
	if correlation.TrackTitle != "" {
		if track, media, ok := match(func(t models.Track) bool { return strings.EqualFold(t.Title, correlation.TrackTitle) }); ok {
			logger.Log.Infof("release %s was merged into %s; matched track %s by title %q", correlation.MBReleaseID, response.ID, track.ID, correlation.TrackTitle)
			return track, media, true
		}
	}
	return models.Track{}, models.MusicBrainzMedia{}, false
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
	if disc == 0 {
		return verifyDiscByDuration(filePath, track, media, response)
	}
	if disc == media.Position {
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

// verifyDiscByDuration is the disc guard for a file whose folder names no disc — a
// flat album folder, or one named something discFolderPattern does not read. The
// look-alike case is the same: another disc holds a track at the same position that
// is the same recording or has the same title, so nothing about the resolution could
// tell the two apart. What can is the audio: the file plays for as long as one of
// them, and MusicBrainz states both lengths.
//
// The comparison is relative, never a tolerance. Encodes drift from the stated length
// by seconds (a production file played 4:19 against a stated 4:12), so "within N
// seconds" tight enough to matter would refuse right answers. What decides is which
// candidate is closer: the resolved track is refused only when a look-alike is
// clearly closer — less than half the gap, and at least lengthMarginSeconds nearer.
// Jerry Goldsmith's *Alien*, whose two discs open with a 4:12 and a 3:34 "Main
// Title", is that case exactly.
//
// Every unknown is a pass: no look-alike, no stated length, or a file whose duration
// cannot be read. The guard only ever adds a refusal it has evidence for.
func verifyDiscByDuration(filePath string, track models.Track, media models.MusicBrainzMedia, response models.MusicBrainzReleaseResponse) error {
	if track.Length <= 0 {
		return nil
	}
	var lookAlikes []models.Track
	var lookAlikeDiscs []int
	for _, other := range response.Media {
		if other.Position == media.Position {
			continue
		}
		for _, candidate := range other.Tracks {
			if candidate.Position != track.Position || candidate.Length <= 0 {
				continue
			}
			sameRecording := candidate.Recording.ID != "" && candidate.Recording.ID == track.Recording.ID
			if !sameRecording && !strings.EqualFold(candidate.Title, track.Title) {
				continue
			}
			lookAlikes = append(lookAlikes, candidate)
			lookAlikeDiscs = append(lookAlikeDiscs, other.Position)
		}
	}
	if len(lookAlikes) == 0 {
		return nil
	}

	seconds, ok := audioDuration(filePath)
	if !ok {
		return nil
	}
	gap := func(lengthMS int) float64 {
		d := seconds - float64(lengthMS)/1000
		if d < 0 {
			return -d
		}
		return d
	}
	resolvedGap := gap(track.Length)
	for i, candidate := range lookAlikes {
		candidateGap := gap(candidate.Length)
		if candidateGap*2 < resolvedGap && resolvedGap-candidateGap >= lengthMarginSeconds {
			return fmt.Errorf(
				"%w: the file plays %s, but was resolved to track %s on disc %d of release %s (%s), "+
					"while disc %d carries the same track at %s — correct the file's track in the manager, "+
					"or put the file in a folder named for its disc",
				ErrDiscMismatch, clock(seconds), track.ID, media.Position, response.ID, clock(float64(track.Length)/1000),
				lookAlikeDiscs[i], clock(float64(candidate.Length)/1000))
		}
	}
	return nil
}

// lengthMarginSeconds is how much nearer a look-alike's stated length has to be before
// the duration check believes it over the resolution. Below it, two candidates are
// both within ordinary encoder drift and neither is evidence.
const lengthMarginSeconds = 5

func clock(seconds float64) string {
	total := int(seconds + 0.5)
	return fmt.Sprintf("%d:%02d", total/60, total%60)
}

func ProcessTrackFileAfterMatch(
	filePath string,
	lidarrClient *LidarrClient,
	plexClient PlexTargets,
	refreshSet *AlbumRefreshSet,
	rootDir string, tagger models.TaggerSettings,
	track models.Track,
	media models.MusicBrainzMedia,
	response models.MusicBrainzReleaseResponse,
) (
	unchanged bool,
	tagsWritten int,
	changed []models.TagChange,
	err error,
) {
	metadata, err := BuildFileTags(track, media, response, tagger)
	if err != nil {
		return false, 0, nil, err
	}
	// The one field that is not MusicBrainz's: the stored tempo analysis, if the
	// profile writes it (docs/tempo.md).
	metadata.BPM = TempoTagValue(filePath, tagger)

	// re-tag file with new information
	unchanged, tagsWritten, changed, err = SetFileTags(filePath, metadata, tagger)
	if err != nil {
		// A missing metaflac was logged once already; per file it is the same line
		// thousands of times.
		if !errors.Is(err, ErrMetaflacMissing) {
			logger.Log.Error("failed to set file tags. error: " + err.Error())
		}
		return unchanged, tagsWritten, changed, fmt.Errorf("failed to set file tags: %w", err)
	} else {
		logger.Log.Debug("file tagger finished")
	}
	// A BPM to write means the stored tempo was just confirmed to describe this audio;
	// the write changed tags only, so keep the row matching the file.
	if !unchanged && metadata.BPM != "" {
		RestampTempoAfterWrite(filePath)
	}

	changeString := "unchanged"
	if !unchanged {
		changeString = "changed. tags written: " + strconv.Itoa(tagsWritten)
	}

	if !unchanged && tagsWritten > 0 {
		// Every server the library is linked to holds its own copy of the album, so
		// each is looked up and refreshed on its own; one that is down costs its own
		// refresh, not the others'.
		for _, server := range plexClient {
			if err := PlexRefreshForFile(refreshSet, server, filePath, rootDir, response.Title, metadata.AlbumArtist, track.Title); err != nil {
				logger.Log.Warnf("failed to prepare a refresh on %s for album. error: %s", server.DisplayName(), err.Error())
			}
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
	tagger models.TaggerSettings,
) (models.FileTags, error) {
	// determine release artist
	releaseArtist := ""
	if len(response.ArtistCredit) > 0 {
		if tagger.UseCurrentArtistName {
			// use current artist name if configured
			releaseArtist = response.ArtistCredit[0].Artist.Name
		} else {
			// use original release artist name if configured
			releaseArtist = response.ArtistCredit[0].Name
		}
	} else {
		return models.FileTags{}, errors.New("failed to determine album artist")
	}

	trackArtist := MusicBrainzArtistsArrayToString(track.ArtistCredit, tagger) // change the array into string to be tagged
	// "Redundant" means the track credit says nothing the album artist does not
	// already say — so it is decided by comparing the two strings, not by counting
	// credits. Counting was the old rule, and it read "one credited artist" as
	// "same as the album artist": on a compilation, or on a release credited to one
	// artist with a track credited solely to another, it emptied ARTIST on a track
	// whose artist the album artist does not name at all. Loose comparison so
	// punctuation or accent differences between the two credits do not keep a
	// genuinely redundant string.
	trackArtistSort := MusicBrainzArtistsSortString(track.ArtistCredit, tagger)
	if tagger.IgnoreRedundantContributingArtists && utilities.EqLoose(trackArtist, releaseArtist) {
		// The sort name goes with the name it sorts: no ARTIST, no ARTISTSORT.
		trackArtist, trackArtistSort = "", ""
	}
	// The first album artist's sort name, matching ALBUMARTIST being the first only.
	releaseArtistSort := strings.TrimSpace(response.ArtistCredit[0].Artist.SortName)
	if releaseArtistSort == "" {
		releaseArtistSort = releaseArtist
	}
	logger.Log.Trace("track artists: " + trackArtist)

	// creditedNames lists the names behind an artist credit, honouring the profile's
	// choice between the artist's current name and the name as credited on this
	// particular release. Credited order is preserved: it is the order the release
	// itself states, and the diff compares it.
	creditedNames := func(credits []models.ArtistCredit) []string {
		names := make([]string, 0, len(credits))
		for _, artistCredit := range credits {
			if tagger.UseCurrentArtistName {
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

	// A blank or malformed date leaves both empty, which drops the tags rather than
	// writing a wrong one.
	releaseYear, releaseDate, _ := ParseMusicBrainzDate(response.Date)
	releaseGroupYear, releaseGroupDate, _ := ParseMusicBrainzDate(response.ReleaseGroup.FirstReleaseDate)
	// DATE and YEAR are the edition's unless the profile asks for the album's own
	// (TaggerSettings.OriginalDateAsDate) — and only when there is one to give, so a
	// release group with no known first date keeps the edition's rather than none.
	date, year := releaseDate, releaseYear
	if tagger.OriginalDateAsDate && releaseGroupDate != "" {
		date, year = releaseGroupDate, releaseGroupYear
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
	labelNames = utilities.NormalizeTagValues(labelNames)
	// Plex shows one record label (see TaggerSettings.SingleRecordLabel). The first
	// is MusicBrainz's first, after blanks are dropped, so an entry with only a
	// catalogue number never leaves the tag empty.
	if tagger.SingleRecordLabel && len(labelNames) > 1 {
		labelNames = labelNames[:1]
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
		Date:                  date,
		Year:                  year,
		ArtistSort:            trackArtistSort,
		AlbumArtistSort:       releaseArtistSort,
		DiscSubtitle:          strings.TrimSpace(media.Title),
		Compilation:           compilationFlag(response),
		Language:              response.TextRepresentation.Language,
		ASIN:                  strings.TrimSpace(response.Asin),
		Album:                 response.Title,
		Title:                 track.Title,
		ISRCs:                 utilities.NormalizeTagValues(track.Recording.ISRCs),
		Track:                 strconv.Itoa(track.Position),
		TrackTotal:            strconv.Itoa(len(media.Tracks)),
		DiscNumber:            strconv.Itoa(media.Position),
		DiscTotal:             strconv.Itoa(len(response.Media)),
		MBAlbumStatus:         strings.ToLower(response.Status),
		MBAlbumTypes:          ReleaseTypes(response, tagger),
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
	metadata.Genres = selectGenres(genres, tagger.MaxGenres)

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
func DiffFileTags(filePath string, metadata models.FileTags, tagger models.TaggerSettings) ([]models.TagDiffEntry, error) {
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
		applyTempoPolicy(desired, existing, tagger)
		changed, _ = utilities.DiffFlacTags(existing, desired, tagger)
	case ".mp3":
		desired = renderMP3Tags(buildMP3DesiredTags(metadata), tagger)
		m, err := GetMP3Tags(filePath)
		if err != nil {
			return nil, err
		}
		existing = m
		applyTempoPolicy(desired, existing, tagger)
		changed, _ = utilities.DiffID3Tags(existing, desired, tagger)
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
		_, isChanged := changed[up]

		// The preview says what a write would do, so a changed row renders its two sides
		// the way the write reports them: a value count is part of the change on a
		// multi-value field, and joining hides it (see utilities.DescribeTagValues).
		//
		// An unchanged row is joined, because there the count cannot differ — the two
		// sides hold the same values in the same shape, and the list form would only put
		// brackets around every multi-value tag the file already agrees about. This view
		// renders every tag, not just the changed ones, so that is most of the page.
		render := utilities.JoinTagValues
		if isChanged {
			render = utilities.DescribeTagValues
		}
		current := render(existing[up])
		want := render(desired[k])
		if current == "" && want == "" {
			continue // nothing to show for an empty-on-both tag
		}
		entries = append(entries, models.TagDiffEntry{Key: k, Current: current, Desired: want, Changed: isChanged})
	}
	return entries, nil
}

// ScanFolderRecursive walks root and processes every audio file under it with
// concurrency workers. Concurrency is a parameter rather than read from config
// because tagging takes only the profile's settings now — how hard the walk is
// pushed is the caller's business, not the tagger's.
func ScanFolderRecursive(root string, lidarrClient *LidarrClient, plexClient PlexTargets, concurrency int, tagger models.TaggerSettings) (
	counter int,
	unchangedFiles int,
	allTagsWritten int,
	errorFiles []string,
	albumsWhoNeedMetadataRefresh []AlbumRefresh,
	err error,
) {
	refreshSet := NewAlbumRefreshSet()

	counter, unchangedFiles, allTagsWritten, errorFiles, err = WalkAndProcess(root, concurrency, func(path string) (bool, int, error) {
		return ProcessTrackFile(path, lidarrClient, plexClient, refreshSet, root, tagger)
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
	return WalkAndProcessContext(context.Background(), root, workers, process, onFile)
}

// WalkAndProcessContext is WalkAndProcess that stops when ctx is cancelled: no file is
// started after that, the ones already in flight finish, and the error is ctx's —
// so the caller can tell a stopped walk from a finished one and prune nothing on it.
func WalkAndProcessContext(ctx context.Context, root string, workers int, process func(path string) (unchanged bool, tagsWritten int, err error), onFile func(path string)) (
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
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
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
				if errors.Is(procErr, ErrMetaflacMissing) {
					logger.Log.Debug("not tagged, metaflac is missing: " + path)
				} else {
					logger.Log.Error("failed to process file '" + path + "'. error: " + procErr.Error())
				}
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

// variousArtistsMBID is MusicBrainz's special purpose artist for compilations of
// several artists.
const variousArtistsMBID = "89ad4ac3-39f7-470e-963a-56509c546377"

// compilationFlag is Picard's COMPILATION: "1" when the release is credited to Various
// Artists, "" otherwise. It is not MusicBrainz's "compilation" secondary type — a
// greatest-hits album by one artist is that, and is not a various-artists compilation.
func compilationFlag(release models.MusicBrainzReleaseResponse) string {
	if len(release.ArtistCredit) > 0 && release.ArtistCredit[0].Artist.ID == variousArtistsMBID {
		return "1"
	}
	return ""
}

// isRemixEdition reports an edition that says it is a remix in its own title or
// disambiguation — the remix editions MusicBrainz files in a release group that is not
// itself typed remix.
func isRemixEdition(release models.MusicBrainzReleaseResponse) bool {
	return strings.Contains(strings.ToLower(release.Title), "remix") || strings.Contains(strings.ToLower(release.Disambiguation), "remix")
}

// ReleaseTypes is the release type tag's values (RELEASETYPE, TXXX:MusicBrainz Album
// Type). By default one Plex-shaped value (PlexReleaseType); with the profile's
// MusicBrainzReleaseType, MusicBrainz's own types as separate values, primary first,
// as Picard writes them. The remix-edition rule applies to both unless the profile
// turns it off: as "album; remix", or as a "remix" value beside the others.
func ReleaseTypes(release models.MusicBrainzReleaseResponse, tagger models.TaggerSettings) []string {
	remix := !tagger.IgnoreRemixEditions && isRemixEdition(release)
	if !tagger.MusicBrainzReleaseType {
		return single(PlexReleaseType(release, remix))
	}
	types := []string{strings.ToLower(release.ReleaseGroup.PrimaryType)}
	hasRemix := false
	for _, secondary := range release.ReleaseGroup.SecondaryTypes {
		secondary = strings.ToLower(secondary)
		hasRemix = hasRemix || secondary == "remix"
		types = append(types, secondary)
	}
	if remix && !hasRemix {
		types = append(types, "remix")
	}
	return utilities.NormalizeTagValues(types)
}

// PlexReleaseType is the release type as one value in the shape Plex sorts albums by:
// the primary type alone when there is no secondary type ("album", "ep", "broadcast"),
// "album; <type>" when the first secondary type is soundtrack, compilation, remix, live
// or demo — whatever the primary type, since Plex shelves those by the second half and
// does not read "ep; live" — and otherwise the primary type if it is an album, EP or
// single, or "" for any other. Only the first secondary type counts. remixEditions
// applies the remix-edition rule (isRemixEdition) ahead of MusicBrainz's types.
func PlexReleaseType(release models.MusicBrainzReleaseResponse, remixEditions bool) string {
	if remixEditions && isRemixEdition(release) {
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

// rematchable reports whether a correlation's source may be re-matched when its track
// has gone from its release. Only identities read from the file itself: a manager
// hands out a fresh pair on every pass (so its miss clears without guessing), and a
// manual correlation is a person's decision, which a re-match must never overwrite.
func rematchable(source string) bool {
	switch source {
	case models.CorrelationSourceTags, models.CorrelationSourceFingerprint, models.CorrelationSourceRematched:
		return true
	}
	return false
}

// rematchRebuiltTrack finds the track a file is, in a release whose tracklist
// MusicBrainz rebuilt under the same release ID — the case that otherwise has no way
// out for a file identified by its own tags, since every pass re-reads the vanished
// track ID from the very file it is trying to repair.
//
// Two ways, both refusing anything short of one answer, because a wrong match here
// mistags a file and the file then carries the mistake as its identity:
//
//  1. **By recording**, when exactly one track has the file's recording. A rebuild
//     re-issues track IDs and keeps recordings.
//  2. **By position, confirmed by title**: the disc and track number in the file's own
//     tags name a track, and that track has the file's title. Position alone is not
//     enough — a rebuild can reorder or insert tracks — and title alone repeats on
//     plenty of albums ("Interlude"). Together they are one track or none.
func rematchRebuiltTrack(filePath string, response models.MusicBrainzReleaseResponse, correlation models.Correlation) (models.Track, models.MusicBrainzMedia, bool) {
	if correlation.MBRecordingID != "" {
		var found models.Track
		var foundMedia models.MusicBrainzMedia
		hits := 0
		for _, media := range response.Media {
			for _, track := range media.Tracks {
				if track.Recording.ID == correlation.MBRecordingID {
					found, foundMedia = track, media
					hits++
				}
			}
		}
		if hits == 1 {
			return found, foundMedia, true
		}
	}

	tags, err := readTagMap(filePath)
	if err != nil {
		logger.Log.Debugf("could not read the tags of '%s' to re-match it: %s", filePath, err.Error())
		return models.Track{}, models.MusicBrainzMedia{}, false
	}
	position := leadingInt(firstTagValue(tags, "TRACKNUMBER"))
	if position < 1 {
		return models.Track{}, models.MusicBrainzMedia{}, false
	}
	disc := leadingInt(firstTagValue(tags, "DISCNUMBER"))
	if disc < 1 {
		disc = 1
	}
	title := correlation.TrackTitle
	if title == "" {
		title = firstTagValue(tags, "TITLE")
	}
	if strings.TrimSpace(title) == "" {
		return models.Track{}, models.MusicBrainzMedia{}, false
	}
	for _, media := range response.Media {
		if media.Position != disc {
			continue
		}
		for _, track := range media.Tracks {
			if track.Position == position && strings.EqualFold(strings.TrimSpace(track.Title), strings.TrimSpace(title)) {
				return track, media, true
			}
		}
	}
	return models.Track{}, models.MusicBrainzMedia{}, false
}

// readTagMap reads a file's tags under the canonical keys both writers use.
func readTagMap(filePath string) (map[string][]string, error) {
	switch strings.ToLower(filepath.Ext(filePath)) {
	case ".flac":
		return getFlacTagsMap(filePath)
	case ".mp3":
		return GetMP3Tags(filePath)
	default:
		return nil, errors.New("unsupported file type")
	}
}

func firstTagValue(tags map[string][]string, key string) string {
	if values := tags[key]; len(values) > 0 {
		return values[0]
	}
	return ""
}

// leadingInt reads the number a "5", "05" or "5/12" tag value starts with; 0 when it
// starts with none.
func leadingInt(value string) int {
	value = strings.TrimSpace(value)
	end := 0
	for end < len(value) && value[end] >= '0' && value[end] <= '9' {
		end++
	}
	n, err := strconv.Atoi(value[:end])
	if err != nil {
		return 0
	}
	return n
}
