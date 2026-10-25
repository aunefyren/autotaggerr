package collection

import (
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/aunefyren/autotaggerr/logger"
	"github.com/aunefyren/autotaggerr/models"
	"gorm.io/gorm"
)

// Every artist's catalogue, however the artist arrived.
//
// An artist used to be measured against whatever release-groups happened to be stored
// for it: an artist found in your files had rows only for what you own, so three albums
// of eleven read as "3 of 3"; one synced from Lidarr had what Lidarr's metadata profile
// listed; one added by hand had nothing. The collection was treating "nobody looked"
// as "nothing missing". So every artist's discography is now read — by a queued job,
// at the MusicBrainz rate — and its size recorded on the artist (CatalogueCount), which
// is what coverage is measured against.
//
// Reading the catalogue is not wanting it. Only following (or a pick) stores rows for
// albums you do not own, so an unfollowed artist's missing list stays empty however
// large its discography is. See docs/collection.md#every-artist-has-a-catalogue.

// RecordCatalogue stores how much of a discography this artist's follow shape takes in.
// The shape is the artist's own, followed or not: it is what "their albums" means for
// this artist, and following later wants exactly that.
func RecordCatalogue(db *gorm.DB, artist models.CollectionArtist, groups []models.MusicBrainzArtistReleaseGroup) error {
	count := 0
	for _, rg := range groups {
		if FollowWants(artist, rg.PrimaryType, rg.SecondaryTypes, rg.FirstReleaseDate) {
			count++
		}
	}
	now := time.Now()
	return db.Model(&models.CollectionArtist{}).Where("mb_id = ?", artist.MBID).
		Updates(map[string]any{"catalogue_count": count, "catalogue_read_at": now}).Error
}

// CatalogueReader is told when an artist is new to the collection, so its discography
// is read without waiting for the next scheduled pass. Implemented by the process
// runner, which queues the read; like the artwork warmer it must return at once.
type CatalogueReader interface {
	ReadCatalogues()
}

var (
	catalogueMu     sync.RWMutex
	catalogueReader CatalogueReader
)

// SetCatalogueReader wires the reader in. Called once from main; nil (tests, the
// one-shot file path) makes every notification a no-op.
func SetCatalogueReader(r CatalogueReader) {
	catalogueMu.Lock()
	defer catalogueMu.Unlock()
	catalogueReader = r
}

// readNewArtistCatalogue notifies that an artist row was created. Same rules as the
// artwork hook: create branch only, enqueue only, silent.
func readNewArtistCatalogue() {
	catalogueMu.RLock()
	r := catalogueReader
	catalogueMu.RUnlock()
	if r != nil {
		r.ReadCatalogues()
	}
}

// LoadPolicy returns the collection policy, creating it with the defaults on first
// read.
func LoadPolicy(db *gorm.DB) (models.CollectionPolicy, error) {
	var policy models.CollectionPolicy
	err := db.Order("created_at").First(&policy).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		policy = models.CollectionPolicy{NewArtistFollow: models.NewArtistFollowOff}
		err = db.Create(&policy).Error
	}
	if policy.NewArtistFollow == "" {
		policy.NewArtistFollow = models.NewArtistFollowOff
	}
	return policy, err
}

// SavePolicy writes the policy's editable fields.
func SavePolicy(db *gorm.DB, in models.CollectionPolicy) (models.CollectionPolicy, error) {
	switch in.NewArtistFollow {
	case models.NewArtistFollowOff, models.NewArtistFollowNew, models.NewArtistFollowAll:
	default:
		return in, errors.New(`new_artist_follow is "off", "new" or "all"`)
	}
	policy, err := LoadPolicy(db)
	if err != nil {
		return policy, err
	}
	policy.NewArtistFollow = in.NewArtistFollow
	policy.FollowTypes = strings.TrimSpace(in.FollowTypes)
	policy.FollowSecondary = in.FollowSecondary
	return policy, db.Save(&policy).Error
}

// applyNewArtistPolicy fills in the follow settings of an artist about to be created
// from files, by the collection policy. Only an artist Autotaggerr governs: a manager
// decides what is wanted for its own, and following would only be stored, not obeyed.
func applyNewArtistPolicy(db *gorm.DB, artist *models.CollectionArtist) {
	if !FollowGoverns(*artist) {
		return
	}
	policy, err := LoadPolicy(db)
	if err != nil {
		logger.Log.Warnf("could not read the collection policy for new artist %s: %s", artist.MBID, err.Error())
		return
	}
	switch policy.NewArtistFollow {
	case models.NewArtistFollowNew:
		artist.FollowFromYear = time.Now().Year()
	case models.NewArtistFollowAll:
	default:
		return
	}
	artist.Monitored = true
	artist.FollowTypes = policy.FollowTypes
	artist.FollowSecondary = policy.FollowSecondary
}
