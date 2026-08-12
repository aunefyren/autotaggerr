package collection

import "sync"

// Artwork is fetched for an artist or album at the moment the collection gains it,
// rather than at the next scheduled pass.
//
// # Why the hook is here and not on the verbs
//
// *Add artist*, a Lidarr sync and a discography sync are the same event wearing three
// hats: **the collection just gained a row it did not have.** Hooking each verb would
// be three integration points that can drift, and a fourth case — whatever introduces
// rows next — that silently would not warm. There are exactly two places a row is
// created (upsertArtist and upsertReleaseGroup, plus AddArtist for the manual path),
// so that is where this sits.
//
// # Three properties keep it safe
//
//   - **It enqueues, never fetches.** AddArtist must return immediately, not forty
//     seconds later having downloaded eighty covers. The warmer's contract is that it
//     returns at once and does the work on its own goroutine.
//   - **It is silent on failure.** A cover that will not download must never make an
//     *Add artist* or a sync report an error. Artwork is decorative and its failures
//     belong in the artwork event; nothing here returns an error at all, which is what
//     makes that structural rather than a rule to remember.
//   - **It never forces.** These rows were created seconds ago and cannot have a stale
//     cached copy, so there is nothing to distrust.
//
// # Only on create
//
// Both upserts run on every rebuild for every row, overwhelmingly as *updates*. Firing
// on those would hand the warmer the whole collection several times a day, which is
// the scheduled pass's job and pointless to duplicate — so the notification sits in
// the create branch only.

// ArtworkWarmer is what the collection tells about new entities. Implemented by
// artwork.Runner; an interface here because artwork imports this package to enumerate
// the collection, so the dependency can only run one way.
type ArtworkWarmer interface {
	// Warm queues artwork for these MBIDs and returns immediately.
	Warm(artists, groups []string)
}

var (
	artworkMu     sync.RWMutex
	artworkWarmer ArtworkWarmer
)

// SetArtworkWarmer wires the artwork runner in. Called once from main; a nil warmer
// (tests, the one-shot file path) turns every notification into a no-op rather than
// something callers have to guard.
func SetArtworkWarmer(w ArtworkWarmer) {
	artworkMu.Lock()
	defer artworkMu.Unlock()
	artworkWarmer = w
}

// warmArtistArtwork notifies that an artist row was created.
func warmArtistArtwork(mbID string) {
	if mbID == "" {
		return
	}
	notifyArtwork([]string{mbID}, nil)
}

// warmGroupArtwork notifies that a release-group row was created.
func warmGroupArtwork(mbID string) {
	if mbID == "" {
		return
	}
	notifyArtwork(nil, []string{mbID})
}

func notifyArtwork(artists, groups []string) {
	artworkMu.RLock()
	warmer := artworkWarmer
	artworkMu.RUnlock()
	if warmer == nil {
		return
	}
	warmer.Warm(artists, groups)
}
