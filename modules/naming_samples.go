package modules

import (
	"strconv"

	"github.com/aunefyren/autotaggerr/models"
)

// NamingSample is a release the profile editor previews templates against, and that
// validation renders them against. Built in rather than fetched: a preview should not
// cost a rate-limited request per keystroke, and validation must not depend on what
// the cache happens to hold. The IDs are placeholders, not MusicBrainz's.
type NamingSample struct {
	Label   string                            `json:"label"`
	Release models.MusicBrainzReleaseResponse `json:"-"`
	TrackID string                            `json:"-"`
	Ext     string                            `json:"-"`
}

func sampleArtist(id, name, sortName string) []models.ArtistCredit {
	return []models.ArtistCredit{{Name: name, Artist: models.Artist{ID: id, Name: name, SortName: sortName}}}
}

func sampleTrack(id string, position int, title string, credit []models.ArtistCredit) models.Track {
	t := models.Track{ID: id, Position: position, Number: strconv.Itoa(position), Title: title, ArtistCredit: credit}
	t.Recording.Title = title
	return t
}

func sampleRelease(id, groupID, title, date string, credit []models.ArtistCredit, media ...models.MusicBrainzMedia) models.MusicBrainzReleaseResponse {
	return models.MusicBrainzReleaseResponse{
		ID:           id,
		Title:        title,
		Date:         date,
		ArtistCredit: credit,
		ReleaseGroup: models.ReleaseGroup{
			ID:               groupID,
			Title:            title,
			FirstReleaseDate: date,
			PrimaryType:      "Album",
			ArtistCredit:     credit,
		},
		Media: media,
	}
}

func sampleID(n string) string { return "00000000-0000-4000-8000-0000000000" + n }

var (
	sampleRumours = func() models.MusicBrainzReleaseResponse {
		fm := sampleArtist(sampleID("01"), "Fleetwood Mac", "Fleetwood Mac")
		return sampleRelease(sampleID("11"), sampleID("21"), "Rumours", "1977-02-04", fm,
			models.MusicBrainzMedia{Position: 1, Format: "CD", Tracks: []models.Track{
				sampleTrack(sampleID("31"), 1, "Second Hand News", fm),
				sampleTrack(sampleID("32"), 2, "Dreams", fm),
			}})
	}()

	sampleWhiteAlbum = func() models.MusicBrainzReleaseResponse {
		beatles := sampleArtist(sampleID("02"), "The Beatles", "Beatles, The")
		return sampleRelease(sampleID("12"), sampleID("22"), "The Beatles", "1968-11-22", beatles,
			models.MusicBrainzMedia{Position: 1, Format: "CD", Tracks: []models.Track{
				sampleTrack(sampleID("41"), 1, "Back in the U.S.S.R.", beatles),
			}},
			models.MusicBrainzMedia{Position: 2, Format: "CD", Tracks: []models.Track{
				sampleTrack(sampleID("51"), 1, "Birthday", beatles),
				sampleTrack(sampleID("52"), 2, "Yer Blues", beatles),
				sampleTrack(sampleID("53"), 3, "Mother Nature's Son", beatles),
				sampleTrack(sampleID("54"), 4, "Everybody's Got Something to Hide Except Me and My Monkey", beatles),
				sampleTrack(sampleID("55"), 5, "Sexy Sadie", beatles),
			}})
	}()

	sampleBackInBlack = func() models.MusicBrainzReleaseResponse {
		acdc := sampleArtist(sampleID("03"), "AC/DC", "AC/DC")
		return sampleRelease(sampleID("13"), sampleID("23"), "Back in Black", "1980-07-25", acdc,
			models.MusicBrainzMedia{Position: 1, Format: "CD", Tracks: []models.Track{
				sampleTrack(sampleID("61"), 1, "Hells Bells", acdc),
			}})
	}()

	sampleJustice = func() models.MusicBrainzReleaseResponse {
		metallica := sampleArtist(sampleID("04"), "Metallica", "Metallica")
		return sampleRelease(sampleID("14"), sampleID("24"), "...And Justice for All", "1988-08-25", metallica,
			models.MusicBrainzMedia{Position: 1, Format: "CD", Tracks: []models.Track{
				sampleTrack(sampleID("71"), 1, "Blackened", metallica),
				sampleTrack(sampleID("72"), 2, "...And Justice for All", metallica),
			}})
	}()
)

// NamingSamples is what the profile editor shows a template rendering: a plain album,
// a disc of a multi-disc one, and the two kinds of name that cleaning exists for.
func NamingSamples() []NamingSample {
	return []NamingSample{
		{Label: "a single-disc album", Release: sampleRumours, TrackID: sampleID("32"), Ext: ".flac"},
		{Label: "a disc of a multi-disc album", Release: sampleWhiteAlbum, TrackID: sampleID("51"), Ext: ".flac"},
		{Label: "a name with a slash", Release: sampleBackInBlack, TrackID: sampleID("61"), Ext: ".mp3"},
		{Label: "a name with leading dots", Release: sampleJustice, TrackID: sampleID("72"), Ext: ".flac"},
	}
}

// namingDiscProbe is disc 2, track 5: the numbers validation reads back, chosen so
// that neither can be right by accident.
func namingDiscProbe() NamingSample {
	return NamingSample{Label: "disc 2, track 5", Release: sampleWhiteAlbum, TrackID: sampleID("55"), Ext: ".flac"}
}
