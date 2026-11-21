package models

import "time"

// Insight credit roles: who an artist is on a recording, for the collaboration graph.
// "credit" is the track's artist credit; the others are relationship groups
// (modules.CreditGroup).
const (
	InsightRoleCredit     = "credit"
	InsightRolePerformer  = "performer"
	InsightRoleProduction = "production"
	InsightRoleWriting    = "writing"
)

// InsightCredit is one artist credited on one recording of one release, derived from
// the cached release by insights.SyncCredits. Keyed by release as well as recording
// because release-level relationships (a producer for the whole album) differ between
// releases of the same recording, and so that the graph can join it to exactly the
// files in scope (library_items carries both IDs).
//
// Derived data: rebuilt from the release cache, never edited. Deleting the table
// loses nothing but the time to rebuild it.
type InsightCredit struct {
	ReleaseMBID   string `gorm:"primaryKey" json:"release_mb_id"`
	RecordingMBID string `gorm:"primaryKey;index" json:"recording_mb_id"`
	ArtistMBID    string `gorm:"primaryKey;index" json:"artist_mb_id"`
	Role          string `gorm:"primaryKey" json:"role"`
	ArtistName    string `json:"artist_name"`
}

// InsightCreditRelease records that a release's credits are in insight_credits, read
// from the cache entry fetched at CachedAt. A cache entry fetched later means the
// release changed upstream (or was re-read), and its credits are read again.
type InsightCreditRelease struct {
	ReleaseMBID string    `gorm:"primaryKey" json:"release_mb_id"`
	CachedAt    time.Time `json:"cached_at"`
	// Genre is the release group's strongest MusicBrainz genre, for colouring the
	// graph by genre. Empty when MusicBrainz lists none.
	Genre string `gorm:"index" json:"genre"`
	// Version is the insights.SyncCredits derivation the row was read with. A row
	// from an older one is read again, which is how a change to what is derived (the
	// genre, when it was added) reaches releases already synced.
	Version int `json:"version"`
}
