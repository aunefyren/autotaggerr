// Artwork lookup: album covers from the Cover Art Archive, artist images from
// fanart.tv. Both are keyed by MusicBrainz ID, which is the only reason they can
// be bolted onto a collection that already speaks in MBIDs.
//
// Everything here is optional and fails soft. A missing cover is not an error —
// it is the normal case for obscure releases — so a failed lookup returns
// ErrNoArtwork and the UI falls back to a monogram tile. Nothing in the tagging
// pipeline depends on this file.
//
// Three properties make it safe to call from a table with a hundred rows:
//
//  1. a disk cache under config/artwork/, so a cover is transferred about once a
//     month rather than once a paint;
//  2. a negative cache, so "no art for this MBID" is remembered rather than
//     re-asked on every paint — this is the difference between one wasted request
//     and one per row per reload;
//  3. single-flight per key, so N rows racing for the same uncached image make one
//     upstream request, not N.
//
// Both caches are indexed by models.ArtworkCacheEntry rows, which is what lets
// them survive a restart and what gives images an expiry at all. The bytes stay on
// disk; only the metadata the filesystem cannot express — the fetch time, and the
// fact that a provider said there is nothing — lives in the database.
package modules

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/aunefyren/autotaggerr/files"
	"github.com/aunefyren/autotaggerr/logger"
	"github.com/aunefyren/autotaggerr/models"
	"github.com/google/uuid"
)

const (
	coverArtArchiveBaseURL = "https://coverartarchive.org"
	fanartBaseURL          = "https://webservice.fanart.tv/v3"

	// artworkRateLimit is this client's own gate, deliberately separate from the
	// MusicBrainz limiter: the Cover Art Archive and fanart.tv are different hosts
	// with their own budgets, and routing images through the 1 req/s MusicBrainz
	// limiter would make a page of covers take a minute to fill for no reason.
	artworkRateLimit = 500 * time.Millisecond // ~2 req/s per host

	// artworkFetchTimeout bounds one upstream image fetch. The Cover Art Archive
	// redirects to archive.org, which can be slow; a browser waiting on a thumbnail
	// must not wait indefinitely.
	artworkFetchTimeout = 20 * time.Second

	// artworkNegativeTTL is how long "there is no art for this MBID" is trusted.
	// Long enough that browsing costs nothing, short enough that art added
	// upstream shows up the same week.
	artworkNegativeTTL = 7 * 24 * time.Hour

	// artworkPositiveTTL is how long a cached image is served before it is fetched
	// again. Images are the longest-lived thing this app caches, and deliberately
	// so: a cover does not change, it is expensive to transfer, and the disk copy
	// is the whole reason a page of a hundred albums paints instantly.
	//
	// It is not infinite — which is what it effectively was before this table —
	// because the Cover Art Archive does get *better*: a release with no art, or
	// with a poor scan, gets a proper one uploaded eventually, and without an
	// expiry that improvement would never reach an install that once cached the
	// placeholder.
	artworkPositiveTTL = 30 * 24 * time.Hour

	// artworkNegativeMax bounds the negative cache. The artwork endpoint answers
	// for any MBID, not only ones in the collection, and it is reachable without a
	// session (an <img> tag cannot send an Authorization header) — so an unbounded
	// map here would grow with every distinct id anyone asked for. Well past the
	// size of a real library, and cheap to rebuild.
	artworkNegativeMax = 20000

	// artworkMaxBytes caps one image. Cover Art Archive originals can be many
	// megabytes; nothing in the UI renders larger than 1200px.
	artworkMaxBytes = 12 << 20
)

// Artwork entities and kinds. The entity says which MBID namespace the id belongs
// to; the kind says which image of that entity is wanted.
const (
	ArtworkEntityReleaseGroup = "release-group"
	ArtworkEntityRelease      = "release"
	ArtworkEntityArtist       = "artist"

	ArtworkKindFront      = "front"      // album front cover (release / release-group)
	ArtworkKindThumb      = "thumb"      // artist portrait, roughly square
	ArtworkKindBackground = "background" // artist backdrop, 16:9
)

// ErrNoArtwork means the providers have no image for this entity — the ordinary
// outcome, not a failure. Callers render a fallback rather than reporting an error.
var ErrNoArtwork = errors.New("no artwork available")

// ErrBadArtworkRequest means the request itself is malformed: not a MusicBrainz ID,
// or a kind of image the entity cannot have. Kept distinct from a provider failure
// so the handler can answer 400 rather than blaming an upstream that was never
// called.
var ErrBadArtworkRequest = errors.New("invalid artwork request")

// ArtworkProviders is the resolved provider configuration for one lookup. The
// router builds it from the DataSource rows so this package stays free of the
// database, exactly as the AcoustID client does.
type ArtworkProviders struct {
	// CoverArt serves album covers and needs no credential.
	CoverArtEnabled bool
	CoverArtBaseURL string

	// Fanart serves artist images and is useless without a personal API key, so an
	// empty key reads as "not configured" rather than as an error.
	FanartEnabled bool
	FanartBaseURL string
	FanartAPIKey  string
}

// Artwork is one image, ready to serve.
type Artwork struct {
	Data        []byte
	ContentType string
	// FromCache reports a disk-cache hit, so the handler can say so in a header
	// while debugging a slow first paint.
	FromCache bool
}

var (
	artworkThrottleMu sync.Mutex
	artworkLastCall   = map[string]time.Time{}

	artworkIndexMu sync.Mutex
	artworkIndex   = map[string]artworkMeta{}

	artworkFlightMu sync.Mutex
	artworkFlight   = map[string]*artworkCall{}

	artworkClient = &http.Client{Timeout: artworkFetchTimeout}
)

// artworkCall is one in-flight lookup that later callers for the same key wait on
// instead of starting their own.
type artworkCall struct {
	done chan struct{}
	art  Artwork
	err  error
}

// artworkMeta is what the index knows about one cache key: whether the providers
// had an image, and until when that answer is trusted. The bytes themselves live
// on disk — this is only the part the filesystem cannot record.
type artworkMeta struct {
	missing     bool
	contentType string
	expiresAt   time.Time
}

func (m artworkMeta) fresh() bool { return time.Now().Before(m.expiresAt) }

// GetArtwork returns one image for an entity, from the disk cache when possible.
//
// size is the requested edge length in pixels and is advisory: the Cover Art
// Archive offers 250/500/1200 and the nearest is used, while fanart.tv serves
// whatever it has. It reaches the cache key through artworkKeySize, so a thumbnail
// and a hero cover do not evict each other while two requests for the same bytes
// still share one entry.
func GetArtwork(providers ArtworkProviders, entity, mbid, kind string, size int) (Artwork, error) {
	if _, err := uuid.Parse(mbid); err != nil {
		// The MBID becomes a file name in the cache, so a non-UUID is rejected
		// before it can be one — not merely because it cannot match anything.
		return Artwork{}, fmt.Errorf("%w: %q is not a MusicBrainz ID", ErrBadArtworkRequest, mbid)
	}
	if !validArtworkRequest(entity, kind) {
		return Artwork{}, fmt.Errorf("%w: %s has no %q artwork", ErrBadArtworkRequest, entity, kind)
	}

	key := artworkCacheKey(entity, mbid, kind, artworkKeySize(entity, size))

	if art, ok := readArtworkCache(key); ok {
		return art, nil
	}
	if negativeCached(key) {
		return Artwork{}, ErrNoArtwork
	}

	// Single-flight: a table of covers renders every row at once, and without this
	// a cold cache turns one missing image into one upstream request per row.
	artworkFlightMu.Lock()
	if call, ok := artworkFlight[key]; ok {
		artworkFlightMu.Unlock()
		<-call.done
		return call.art, call.err
	}
	call := &artworkCall{done: make(chan struct{})}
	artworkFlight[key] = call
	artworkFlightMu.Unlock()

	call.art, call.err = fetchArtwork(providers, entity, mbid, kind, size)
	if call.err == nil {
		writeArtworkCache(key, call.art)
	} else if errors.Is(call.err, ErrNoArtwork) {
		rememberNoArtwork(key)
	} else if art, ok := readExpiredArtwork(key); ok {
		// A provider outage must not make covers vanish from a page that had them
		// yesterday. Giving images an expiry is about picking up better scans over
		// time, not about discarding a perfectly good one because a CDN is down —
		// so a failed *refresh* falls back to the copy already on disk, exactly as
		// the MusicBrainz lookups fall back to their stale entries.
		call.art, call.err = art, nil
	}

	close(call.done)
	artworkFlightMu.Lock()
	delete(artworkFlight, key)
	artworkFlightMu.Unlock()

	return call.art, call.err
}

// ArtworkFresh reports whether the cache already holds a current answer for this
// image — the bytes, or a remembered absence — so a warming pass can skip it
// without spending an upstream request. It is the artwork counterpart of
// MusicbrainzEntityFresh.
//
// It has to exist as its own predicate because GetArtwork cannot be asked the
// question: a fresh negative entry and a provider that has just answered "no image"
// both return ErrNoArtwork, so a pass that used the return value to decide whether
// it had done any work would re-ask for every coverless album on every run.
//
// A positive entry whose file has since been deleted is not an answer, so the index
// alone is not enough — the negative half has nothing on disk by definition and is
// taken at its word.
func ArtworkFresh(entity, mbid, kind string, size int) bool {
	key := artworkCacheKey(entity, mbid, kind, artworkKeySize(entity, size))
	meta, known := artworkMetaFor(key)
	if !known || !meta.fresh() {
		return false
	}
	if meta.missing {
		return true
	}
	info, err := os.Stat(artworkCachePath(key))
	return err == nil && info.Size() > 0
}

// ArtworkExpire marks one cache entry stale so the next GetArtwork goes upstream,
// which is how a forced refresh reaches past the cache check GetArtwork does for
// itself. Both halves are covered: an image is re-downloaded, and a remembered "no
// image" is re-asked.
//
// Expiring rather than deleting, for the same reason MusicbrainzExpireEntity does
// it — the stale copy stays on disk as the fallback if the re-fetch then fails, so a
// forced pass during a provider outage does not empty a page that had covers on it.
func ArtworkExpire(entity, mbid, kind string, size int) {
	key := artworkCacheKey(entity, mbid, kind, artworkKeySize(entity, size))
	meta, known := artworkMetaFor(key)
	if !known {
		return
	}
	meta.expiresAt = time.Now().Add(-time.Second)
	storeArtworkMeta(key, meta)
}

// ArtworkCacheCounts reports what the artwork cache holds right now: how many
// images, and how many remembered "this entity has no image" answers.
//
// The two are worth separating rather than summing. Only `images` costs a transfer
// to rebuild, so it is the number a "re-download everything" estimate has to rest
// on; `missing` is the cheap half, and on its own it answers the question a page of
// monogram tiles raises — whether the provider was asked and said no.
func ArtworkCacheCounts() (images, missing int) {
	artworkIndexMu.Lock()
	defer artworkIndexMu.Unlock()
	for _, meta := range artworkIndex {
		if meta.missing {
			missing++
			continue
		}
		images++
	}
	return images, missing
}

// validArtworkRequest rejects combinations that cannot exist. MusicBrainz has no
// artist photos and the Cover Art Archive has no artist entity, so an artist
// front cover is a coding mistake rather than a missing image.
func validArtworkRequest(entity, kind string) bool {
	switch entity {
	case ArtworkEntityReleaseGroup, ArtworkEntityRelease:
		return kind == ArtworkKindFront
	case ArtworkEntityArtist:
		return kind == ArtworkKindThumb || kind == ArtworkKindBackground
	}
	return false
}

// fetchArtwork routes to the provider that holds this kind of image.
func fetchArtwork(providers ArtworkProviders, entity, mbid, kind string, size int) (Artwork, error) {
	switch entity {
	case ArtworkEntityReleaseGroup, ArtworkEntityRelease:
		if !providers.CoverArtEnabled {
			return Artwork{}, ErrNoArtwork
		}
		return fetchCoverArt(providers, entity, mbid, size)
	case ArtworkEntityArtist:
		if !providers.FanartEnabled || strings.TrimSpace(providers.FanartAPIKey) == "" {
			// No key is a deployment fact, not a failure: artist images are opt-in.
			return Artwork{}, ErrNoArtwork
		}
		return fetchFanartImage(providers, mbid, kind)
	}
	return Artwork{}, ErrNoArtwork
}

// coverArtSizes are the thumbnail edges the Cover Art Archive actually serves.
var coverArtSizes = []int{250, 500, 1200}

// fetchCoverArt asks the Cover Art Archive for a front cover. A 404 is the
// documented answer for "this release has no art" and is the common case.
func fetchCoverArt(providers ArtworkProviders, entity, mbid string, size int) (Artwork, error) {
	base := strings.TrimRight(orDefault(providers.CoverArtBaseURL, coverArtArchiveBaseURL), "/")
	url := fmt.Sprintf("%s/%s/%s/front-%d", base, entity, mbid, nearestCoverArtSize(size))

	art, err := downloadImage("coverartarchive", url, "")
	if err != nil {
		return Artwork{}, err
	}
	return art, nil
}

func nearestCoverArtSize(size int) int {
	for _, candidate := range coverArtSizes {
		if size <= candidate {
			return candidate
		}
	}
	return coverArtSizes[len(coverArtSizes)-1]
}

// fanartResponse is the slice of fanart.tv's music response this app uses. Every
// image list is ordered by likes, so the first entry is the community's pick.
type fanartResponse struct {
	Name             string         `json:"name"`
	ArtistThumb      []fanartImage  `json:"artistthumb"`
	ArtistBackground []fanartImage  `json:"artistbackground"`
	MusicLogo        []fanartImage  `json:"musiclogo"`
	HDMusicLogo      []fanartImage  `json:"hdmusiclogo"`
	Albums           map[string]any `json:"albums"`
}

type fanartImage struct {
	ID    string `json:"id"`
	URL   string `json:"url"`
	Likes string `json:"likes"`
}

// fetchFanartImage resolves an artist image in two hops: fanart.tv's JSON index
// for the MBID, then the image itself off their CDN.
func fetchFanartImage(providers ArtworkProviders, mbid, kind string) (Artwork, error) {
	base := strings.TrimRight(orDefault(providers.FanartBaseURL, fanartBaseURL), "/")
	url := fmt.Sprintf("%s/music/%s?api_key=%s", base, mbid, strings.TrimSpace(providers.FanartAPIKey))

	artworkThrottle("fanart")
	resp, err := artworkClient.Get(url)
	if err != nil {
		return Artwork{}, fmt.Errorf("fanart.tv request failed: %w", err)
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusNotFound:
		// fanart.tv simply has nothing for this artist. Extremely common.
		return Artwork{}, ErrNoArtwork
	case http.StatusUnauthorized, http.StatusForbidden:
		return Artwork{}, errors.New("fanart.tv rejected the API key — check the data source")
	default:
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return Artwork{}, fmt.Errorf("fanart.tv returned HTTP %d: %s", resp.StatusCode, snippet(body))
	}

	var parsed fanartResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&parsed); err != nil {
		return Artwork{}, fmt.Errorf("could not parse the fanart.tv response: %w", err)
	}

	var candidates []fanartImage
	switch kind {
	case ArtworkKindThumb:
		candidates = parsed.ArtistThumb
	case ArtworkKindBackground:
		candidates = parsed.ArtistBackground
	}
	if len(candidates) == 0 || candidates[0].URL == "" {
		return Artwork{}, ErrNoArtwork
	}

	return downloadImage("fanart", candidates[0].URL, "")
}

// downloadImage fetches one image URL, enforcing the per-host rate limit, the
// size cap, and that what came back is actually an image.
func downloadImage(host, url, referer string) (Artwork, error) {
	artworkThrottle(host)

	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return Artwork{}, err
	}
	req.Header.Set("User-Agent", artworkUserAgent())
	if referer != "" {
		req.Header.Set("Referer", referer)
	}

	resp, err := artworkClient.Do(req)
	if err != nil {
		return Artwork{}, fmt.Errorf("artwork request failed: %w", err)
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusNotFound, http.StatusGone:
		return Artwork{}, ErrNoArtwork
	default:
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return Artwork{}, fmt.Errorf("artwork provider returned HTTP %d: %s", resp.StatusCode, snippet(body))
	}

	data, err := io.ReadAll(io.LimitReader(resp.Body, artworkMaxBytes))
	if err != nil {
		return Artwork{}, fmt.Errorf("could not read the artwork response: %w", err)
	}
	if len(data) == 0 {
		return Artwork{}, ErrNoArtwork
	}

	contentType := resp.Header.Get("Content-Type")
	if !strings.HasPrefix(contentType, "image/") {
		contentType = sniffImageType(data)
		if contentType == "" {
			return Artwork{}, ErrNoArtwork
		}
	}
	return Artwork{Data: data, ContentType: contentType}, nil
}

// sniffImageType identifies the formats these providers actually serve. Used when
// a CDN omits or mislabels Content-Type.
func sniffImageType(data []byte) string {
	switch {
	case len(data) > 3 && data[0] == 0xFF && data[1] == 0xD8 && data[2] == 0xFF:
		return "image/jpeg"
	case len(data) > 8 && string(data[1:4]) == "PNG":
		return "image/png"
	case len(data) > 12 && string(data[0:4]) == "RIFF" && string(data[8:12]) == "WEBP":
		return "image/webp"
	case len(data) > 6 && string(data[0:3]) == "GIF":
		return "image/gif"
	}
	return ""
}

func artworkUserAgent() string {
	return "Autotaggerr/" + files.ConfigFile.AutotaggerrVersion + " (https://github.com/aunefyren/autotaggerr)"
}

// artworkThrottle gates one host. Per host rather than global: a page needing an
// artist portrait and twelve covers should not serialize the two providers behind
// one another.
func artworkThrottle(host string) {
	artworkThrottleMu.Lock()
	last := artworkLastCall[host]
	wait := artworkRateLimit - time.Since(last)
	if wait <= 0 {
		artworkLastCall[host] = time.Now()
		artworkThrottleMu.Unlock()
		return
	}
	// Reserve the slot before sleeping, so concurrent callers queue behind this
	// one instead of all waking into the same window.
	artworkLastCall[host] = last.Add(artworkRateLimit)
	artworkThrottleMu.Unlock()
	time.Sleep(wait)
}

// --- caching ----------------------------------------------------------------

// artworkCacheKey is also the cache's file name, so it must stay filesystem-safe.
// Every component is either a fixed constant or a validated UUID.
func artworkCacheKey(entity, mbid, kind string, size int) string {
	return fmt.Sprintf("%s_%s_%s_%d", entity, mbid, kind, size)
}

// artworkKeySize reduces a requested size to the one that actually distinguishes a
// cached image, so two requests that resolve to identical bytes share one entry.
//
// **Artist images have no size dimension at all.** fanart.tv serves whatever it has
// and ignores what was asked for, so keying by the requested size stored the same
// portrait once per size a page happened to want — 250 for a collection row, 500 for
// the artist page, 1200 for the backdrop — and, far worse, discovered "this artist
// has no image" separately at each of them, at one upstream request apiece. Most
// artists have no fanart entry, so that was three requests per artist to learn the
// same nothing.
//
// **Covers do have variants**, but only the three the Cover Art Archive serves, and
// `fetchCoverArt` already rounds to them. The key carries the size that will be
// fetched rather than the one that was asked for, so 240 and 250 cannot end up as two
// copies of one image.
func artworkKeySize(entity string, size int) int {
	if entity == ArtworkEntityArtist {
		return 0
	}
	return nearestCoverArtSize(size)
}

func artworkCacheDir() string {
	return filepath.Join("config", "artwork")
}

func artworkCachePath(key string) string {
	return filepath.Join(artworkCacheDir(), key)
}

// readArtworkCache returns a previously fetched image, if one is on disk and the
// index still trusts it. The content type is sniffed from the bytes rather than
// guessed from an extension, because fanart.tv serves a mix of formats behind
// extensionless URLs.
//
// A file with no index entry is *adopted* rather than ignored: installs that
// cached artwork before the index existed have a full config/artwork/ directory,
// and treating those as misses would re-download every cover an install already
// has. Adoption gives them a normal expiry, so they refresh on the ordinary
// schedule from here on.
func readArtworkCache(key string) (Artwork, bool) {
	meta, known := artworkMetaFor(key)
	if known {
		if meta.missing || !meta.fresh() {
			return Artwork{}, false
		}
	}

	data, err := os.ReadFile(artworkCachePath(key))
	if err != nil || len(data) == 0 {
		return Artwork{}, false
	}
	contentType := sniffImageType(data)
	if contentType == "" {
		return Artwork{}, false
	}

	if !known {
		storeArtworkMeta(key, artworkMeta{
			contentType: contentType,
			expiresAt:   time.Now().Add(artworkPositiveTTL),
		})
	}
	return Artwork{Data: data, ContentType: contentType, FromCache: true}, true
}

// readExpiredArtwork returns a cached image whatever its age, for the case where
// the refresh that would have replaced it failed. It refuses entries recorded as
// missing — there is no file behind those to serve.
func readExpiredArtwork(key string) (Artwork, bool) {
	if meta, known := artworkMetaFor(key); known && meta.missing {
		return Artwork{}, false
	}

	data, err := os.ReadFile(artworkCachePath(key))
	if err != nil || len(data) == 0 {
		return Artwork{}, false
	}
	contentType := sniffImageType(data)
	if contentType == "" {
		return Artwork{}, false
	}
	return Artwork{Data: data, ContentType: contentType, FromCache: true}, true
}

func writeArtworkCache(key string, art Artwork) {
	if err := os.MkdirAll(artworkCacheDir(), 0o755); err != nil {
		logger.Log.Warnf("could not create the artwork cache directory: %s", err.Error())
		return
	}
	// Written to a temporary name and renamed, so a killed process cannot leave a
	// truncated image behind that the cache would then serve forever.
	path := artworkCachePath(key)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, art.Data, 0o644); err != nil {
		logger.Log.Warnf("could not cache artwork %s: %s", key, err.Error())
		return
	}
	if err := os.Rename(tmp, path); err != nil {
		logger.Log.Warnf("could not cache artwork %s: %s", key, err.Error())
		_ = os.Remove(tmp)
		return
	}

	storeArtworkMeta(key, artworkMeta{
		contentType: art.ContentType,
		expiresAt:   time.Now().Add(artworkPositiveTTL),
	})
}

func negativeCached(key string) bool {
	meta, ok := artworkMetaFor(key)
	return ok && meta.missing && meta.fresh()
}

func rememberNoArtwork(key string) {
	pruneArtworkNegatives()
	storeArtworkMeta(key, artworkMeta{
		missing:   true,
		expiresAt: time.Now().Add(artworkNegativeTTL),
	})
}

func artworkMetaFor(key string) (artworkMeta, bool) {
	artworkIndexMu.Lock()
	defer artworkIndexMu.Unlock()
	meta, ok := artworkIndex[key]
	return meta, ok
}

// storeArtworkMeta records one index entry in memory and, when a database is
// configured, writes it through. Persisting is what makes the negative cache
// worth having: "no cover for this release" is the common answer for obscure
// releases, and a process-local memory of it meant every restart re-asked the
// Cover Art Archive for thousands of covers it had already declined.
func storeArtworkMeta(key string, meta artworkMeta) {
	artworkIndexMu.Lock()
	artworkIndex[key] = meta
	artworkIndexMu.Unlock()

	if cacheDB == nil {
		return
	}
	row := models.ArtworkCacheEntry{
		Key:         key,
		Missing:     meta.missing,
		ContentType: meta.contentType,
		FetchedAt:   time.Now(),
		ExpiresAt:   meta.expiresAt,
	}
	if err := cacheDB.Save(&row).Error; err != nil {
		logger.Log.Warnf("failed to persist artwork cache row %s: %s", key, err.Error())
	}
}

// pruneArtworkNegatives bounds the negative half of the index. Only negatives are
// capped: a positive entry required a real image to come back, so those are
// bounded by the collection, while the artwork endpoint answers for any MBID
// anyone asks about and is reachable without a session (an <img> tag cannot send
// an Authorization header).
//
// Dropped wholesale rather than evicted one by one: the entries are all equally
// cheap to re-derive, and an LRU here would be machinery in service of a cache
// whose only job is to stop a repeated 404.
func pruneArtworkNegatives() {
	artworkIndexMu.Lock()
	negatives := 0
	for _, meta := range artworkIndex {
		if meta.missing {
			negatives++
		}
	}
	if negatives < artworkNegativeMax {
		artworkIndexMu.Unlock()
		return
	}
	for key, meta := range artworkIndex {
		if meta.missing {
			delete(artworkIndex, key)
		}
	}
	artworkIndexMu.Unlock()

	if cacheDB != nil {
		if err := cacheDB.Where("missing = ?", true).Delete(&models.ArtworkCacheEntry{}).Error; err != nil {
			logger.Log.Warnf("failed to prune artwork negative cache: %s", err.Error())
		}
	}
}

// ResetArtworkNegativeCache forgets every "no artwork" answer. Exposed for tests
// and for the case where a provider was just configured — the empty answers
// recorded before the API key existed are not worth waiting a week on.
func ResetArtworkNegativeCache() {
	artworkIndexMu.Lock()
	for key, meta := range artworkIndex {
		if meta.missing {
			delete(artworkIndex, key)
		}
	}
	artworkIndexMu.Unlock()

	if cacheDB != nil {
		if err := cacheDB.Where("missing = ?", true).Delete(&models.ArtworkCacheEntry{}).Error; err != nil {
			logger.Log.Warnf("failed to clear artwork negative cache: %s", err.Error())
		}
	}
}

// canonicalArtistKey rewrites a legacy size-carrying artist key onto the size-less
// one, reporting false for a key that is already canonical or is not an artist's.
//
// Keys are `entity_mbid_kind_size`, and none of the four components can contain an
// underscore — entity names are fixed constants ("release-group" hyphenates for this
// reason), the MBID is a validated UUID, and kind is a constant.
func canonicalArtistKey(key string) (string, bool) {
	parts := strings.Split(key, "_")
	if len(parts) != 4 || parts[0] != ArtworkEntityArtist || parts[3] == "0" {
		return "", false
	}
	return artworkCacheKey(parts[0], parts[1], parts[2], 0), true
}

// artworkMigrateArtistKeys folds artist entries onto the size-less key (see
// artworkKeySize) so the change of key does not read as an empty cache.
//
// Without it, the first run after the upgrade re-downloads every artist portrait the
// install already holds and re-asks fanart.tv about every artist it has already
// declined — at ~2 req/s, and for a collection where most artists have no image, that
// is the bulk of the cache paying to learn what it already knew.
//
// Idempotent, and safe to interrupt: a row already on the canonical key is left
// alone, duplicates collapse onto the first one seen (they are the same image by
// definition), and a row whose file cannot be moved is left where it is to miss and
// refetch normally. Negative rows have no file at all, which is the expected case
// rather than an error.
func artworkMigrateArtistKeys() error {
	var rows []models.ArtworkCacheEntry
	if err := cacheDB.Find(&rows).Error; err != nil {
		return err
	}

	canonical := map[string]bool{}
	for _, r := range rows {
		canonical[r.Key] = true
	}

	folded, dropped := 0, 0
	for _, r := range rows {
		target, legacy := canonicalArtistKey(r.Key)
		if !legacy {
			continue
		}

		if canonical[target] {
			_ = os.Remove(artworkCachePath(r.Key))
			if err := cacheDB.Delete(&models.ArtworkCacheEntry{}, "key = ?", r.Key).Error; err != nil {
				return err
			}
			dropped++
			continue
		}

		if err := os.Rename(artworkCachePath(r.Key), artworkCachePath(target)); err != nil && !errors.Is(err, os.ErrNotExist) {
			logger.Log.Debugf("could not move cached artwork %s: %s", r.Key, err.Error())
			continue
		}

		row := r
		row.Key = target
		if err := cacheDB.Create(&row).Error; err != nil {
			return err
		}
		if err := cacheDB.Delete(&models.ArtworkCacheEntry{}, "key = ?", r.Key).Error; err != nil {
			return err
		}
		canonical[target] = true
		folded++
	}

	if folded > 0 || dropped > 0 {
		logger.Log.Infof("artwork cache: folded %d artist entr(ies) onto their size-less key, dropped %d duplicate(s)", folded, dropped)
	}
	return nil
}

// artworkLoadCache warms the index from the database at startup, so a restart
// keeps both what has been fetched and what the providers said does not exist.
func artworkLoadCache() error {
	if cacheDB == nil {
		return nil
	}

	// Before the rows are read, so the in-memory index is warmed with the keys the
	// running code will ask for.
	if err := artworkMigrateArtistKeys(); err != nil {
		logger.Log.Warnf("could not fold legacy artist artwork keys: %s", err.Error())
	}

	var rows []models.ArtworkCacheEntry
	if err := cacheDB.Find(&rows).Error; err != nil {
		return err
	}

	artworkIndexMu.Lock()
	defer artworkIndexMu.Unlock()
	for _, r := range rows {
		artworkIndex[r.Key] = artworkMeta{
			missing:     r.Missing,
			contentType: r.ContentType,
			expiresAt:   r.ExpiresAt,
		}
	}
	return nil
}

func orDefault(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}
