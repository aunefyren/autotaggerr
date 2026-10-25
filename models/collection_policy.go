package models

// CollectionPolicy holds the collection-wide decisions: what the collection does by
// itself, as opposed to how the process runs (config.json). One row; it is created on
// first read with the defaults below. See docs/collection.md#new-artists.
type CollectionPolicy struct {
	Base
	// NewArtistFollow is whether an artist first found in your files — by a scan, an
	// import or a Lidarr sync — is followed: NewArtistFollow*. Off by default: a
	// library of five hundred artists would otherwise start with thousands of missing
	// albums. Only artists Autotaggerr governs are followed; a manager decides for its
	// own.
	NewArtistFollow string `json:"new_artist_follow"`
	// The follow shape a newly followed artist gets: the same fields an artist carries.
	// Empty FollowTypes is the default, studio albums and EPs.
	FollowTypes     string `json:"follow_types"`
	FollowSecondary bool   `json:"follow_secondary"`
}

const (
	// NewArtistFollowOff leaves a new artist unfollowed: only what you own is listed.
	NewArtistFollowOff = "off"
	// NewArtistFollowNew follows from the year the artist was found: what you are told
	// about is what they release next, not the back catalogue you chose not to own.
	NewArtistFollowNew = "new"
	// NewArtistFollowAll follows the whole discography.
	NewArtistFollowAll = "all"
)
