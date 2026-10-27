package modules

import (
	"math"
	"time"
)

// The request rates the app holds itself to for the providers whose pace is not a
// setting: artwork per image host (artworkRateLimit) and AcoustID (acoustidRateLimit).
// Exported as requests per second so the Data sources page states what the code does
// rather than a number of its own that could drift from it. MusicBrainz is absent: its
// rate is the source row's, because a local mirror lifts the public 1/s.

// ArtworkRequestsPerSecond is the pace of artwork requests to each image host.
func ArtworkRequestsPerSecond() float64 { return perSecond(artworkRateLimit) }

// AcoustIDRequestsPerSecond is the pace of AcoustID lookups.
func AcoustIDRequestsPerSecond() float64 { return perSecond(acoustidRateLimit) }

// perSecond rounds to one decimal: 334ms is AcoustID's 3/s, not 2.994/s.
func perSecond(gap time.Duration) float64 {
	return math.Round(float64(time.Second)/float64(gap)*10) / 10
}
