package routers

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/aunefyren/autotaggerr/collection"
	"github.com/aunefyren/autotaggerr/models"
	"github.com/aunefyren/autotaggerr/modules"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// MCP tools for the collection: artists, their albums, editions and tracklists, and
// what is wanted but missing. Every judgement (owned, complete, discrepancy, wanted)
// comes from the same functions the artist pages use; these tools only reshape.

type mcpArtistRow struct {
	MBID      string `json:"mbid"`
	Name      string `json:"name"`
	Monitored bool   `json:"monitored" jsonschema:"followed: new albums matching the follow settings are wanted automatically"`
	ManagedBy string `json:"managed_by" jsonschema:"who owns this artist's identity: lidarr, autotaggerr or mixed"`
	Owned     int    `json:"owned" jsonschema:"albums with files on disk"`
	Complete  int    `json:"complete" jsonschema:"owned albums with every track"`
	Partial   int    `json:"partial" jsonschema:"owned albums missing tracks"`
	Missing   int    `json:"missing" jsonschema:"albums known but not owned (wanted or not)"`
	Mismatch  int    `json:"mismatch" jsonschema:"albums where disk and the manager's catalog disagree"`
	Picked    int    `json:"picked" jsonschema:"albums explicitly asked for"`
}

type mcpListArtistsInput struct {
	NameContains string `json:"name_contains,omitempty" jsonschema:"case-insensitive substring of the artist name"`
	Filter       string `json:"filter,omitempty" jsonschema:"only artists with: partial (owned albums missing tracks), mismatch, picked, monitored or unmonitored"`
	mcpPaging
}

type mcpArtistList struct {
	mcpPage
	Artists []mcpArtistRow `json:"artists"`
}

type mcpAlbumRow struct {
	MBID             string `json:"mbid" jsonschema:"release-group MBID; pass to get_album"`
	Title            string `json:"title"`
	PrimaryType      string `json:"primary_type,omitempty"`
	SecondaryTypes   string `json:"secondary_types,omitempty"`
	FirstReleaseDate string `json:"first_release_date,omitempty"`
	Owned            bool   `json:"owned"`
	OwnedTracks      int    `json:"owned_tracks,omitempty"`
	TotalTracks      int    `json:"total_tracks,omitempty" jsonschema:"track count of the best-owned edition"`
	Complete         bool   `json:"complete"`
	OwnedEditions    int    `json:"owned_editions,omitempty"`
	Discrepancy      string `json:"discrepancy,omitempty" jsonschema:"how disk and the manager's catalog disagree; empty when they agree"`
	Wanted           bool   `json:"wanted"`
	WantedSource     string `json:"wanted_source,omitempty" jsonschema:"explicit, auto or manager"`
	WantedEditions   int    `json:"wanted_editions,omitempty" jsonschema:"specific editions asked for; 0 with wanted means any edition"`
	WantedTracks     int    `json:"wanted_tracks,omitempty" jsonschema:"specific songs asked for; 0 means the whole album"`
}

func newMCPAlbumRow(v releaseGroupView) mcpAlbumRow {
	discrepancy := v.Discrepancy
	if discrepancy == models.DiscrepancyNone {
		discrepancy = ""
	}
	return mcpAlbumRow{
		MBID: v.MBID, Title: v.Title, PrimaryType: v.PrimaryType, SecondaryTypes: v.SecondaryTypes,
		FirstReleaseDate: v.FirstReleaseDate, Owned: v.Owned, OwnedTracks: v.OwnedTracks, TotalTracks: v.TotalTracks,
		Complete: v.Complete, OwnedEditions: v.OwnedEditions, Discrepancy: discrepancy,
		Wanted: v.Wanted, WantedSource: v.WantedSource,
		WantedEditions: len(v.DesiredReleases), WantedTracks: len(v.DesiredRecordings),
	}
}

type mcpArtistRef struct {
	MBID string `json:"mbid"`
	Name string `json:"name"`
}

type mcpGetArtistInput struct {
	MBID string `json:"mbid" jsonschema:"artist MBID from list_artists"`
}

type mcpArtistDetail struct {
	MBID            string        `json:"mbid"`
	Name            string        `json:"name"`
	Monitored       bool          `json:"monitored"`
	ManagedBy       string        `json:"managed_by"`
	FollowGoverns   bool          `json:"follow_governs" jsonschema:"whether monitored decides anything; false when a manager decides what is wanted"`
	FollowTypes     string        `json:"follow_types,omitempty" jsonschema:"album types following wants"`
	FollowSecondary bool          `json:"follow_secondary,omitempty"`
	FollowFromYear  int           `json:"follow_from_year,omitempty"`
	Albums          []mcpAlbumRow `json:"albums"`
	// UnresolvableAlbums names albums whose stored ID resolves nowhere upstream — the
	// artist page's repair prompt.
	UnresolvableAlbums []string `json:"unresolvable_albums,omitempty" jsonschema:"MBIDs of albums that no longer resolve at MusicBrainz; a manager refresh repairs them"`
}

type mcpListMissingInput struct {
	ArtistMBID        string `json:"artist_mbid,omitempty" jsonschema:"only this artist"`
	IncludeIncomplete bool   `json:"include_incomplete,omitempty" jsonschema:"also list wanted albums that are owned but missing tracks"`
	mcpPaging
}

type mcpMissingRow struct {
	Artist mcpArtistRef `json:"artist"`
	mcpAlbumRow
}

type mcpMissingList struct {
	mcpPage
	Albums []mcpMissingRow `json:"albums"`
}

type mcpGetAlbumInput struct {
	ArtistMBID string `json:"artist_mbid" jsonschema:"artist MBID the album is listed under"`
	AlbumMBID  string `json:"album_mbid" jsonschema:"release-group MBID from get_artist or list_missing"`
}

type mcpEditionRow struct {
	MBID           string `json:"mbid" jsonschema:"release MBID; pass to get_release_tracks"`
	Title          string `json:"title"`
	Date           string `json:"date,omitempty"`
	Country        string `json:"country,omitempty"`
	Disambiguation string `json:"disambiguation,omitempty"`
	Formats        string `json:"formats,omitempty" jsonschema:"media, e.g. CD + CD"`
	Tracks         int    `json:"tracks"`
	Owned          bool   `json:"owned"`
	OwnedTracks    int    `json:"owned_tracks,omitempty"`
	Complete       bool   `json:"complete,omitempty"`
	Wanted         bool   `json:"wanted,omitempty" jsonschema:"this specific edition was asked for"`
}

type mcpAlbumDetail struct {
	Artist   mcpArtistRef    `json:"artist"`
	Album    mcpAlbumRow     `json:"album"`
	Editions []mcpEditionRow `json:"editions" jsonschema:"every edition MusicBrainz lists, plus any owned edition it no longer does"`
}

type mcpGetReleaseTracksInput struct {
	ReleaseMBID string `json:"release_mbid" jsonschema:"edition (release) MBID from get_album"`
}

type mcpTrackRow struct {
	Medium        int    `json:"medium"`
	MediumTitle   string `json:"medium_title,omitempty"`
	Position      int    `json:"position"`
	Number        string `json:"number,omitempty"`
	Title         string `json:"title"`
	LengthMS      int    `json:"length_ms,omitempty"`
	RecordingMBID string `json:"recording_mbid"`
	TrackMBID     string `json:"track_mbid"`
	Video         bool   `json:"video,omitempty"`
	FileID        string `json:"file_id,omitempty" jsonschema:"the indexed file identified as this track; pass to get_file. Absent when not owned"`
}

type mcpReleaseTracks struct {
	MBID           string        `json:"mbid"`
	Title          string        `json:"title"`
	Date           string        `json:"date,omitempty"`
	Country        string        `json:"country,omitempty"`
	Disambiguation string        `json:"disambiguation,omitempty"`
	Tracks         []mcpTrackRow `json:"tracks"`
}

func (a *API) addMCPCollectionTools(server *mcp.Server) {
	mcp.AddTool(server, &mcp.Tool{
		Name: "list_artists",
		Description: "The artists in the collection, by name, with album counts: owned, complete, partial, missing, " +
			"mismatch and picked. Filter by name or by what needs attention.",
		Annotations: readOnlyTool("List artists", false),
	}, a.mcpListArtists)

	mcp.AddTool(server, &mcp.Tool{
		Name: "get_artist",
		Description: "One artist and all their albums (release-groups): owned, complete, wanted and why, and any " +
			"disagreement with the manager's catalog. Answered from the database.",
		Annotations: readOnlyTool("Get artist", false),
	}, a.mcpGetArtist)

	mcp.AddTool(server, &mcp.Tool{
		Name: "list_missing",
		Description: "Wanted albums that are not owned, across the whole collection or one artist — \"what am I " +
			"missing?\". include_incomplete adds wanted albums owned with tracks missing.",
		Annotations: readOnlyTool("List missing albums", false),
	}, a.mcpListMissing)

	mcp.AddTool(server, &mcp.Tool{
		Name: "get_album",
		Description: "One album (release-group): its state, and every edition (release) with which are owned and " +
			"how complete. May fetch the edition list from MusicBrainz, which can take a second or more.",
		Annotations: readOnlyTool("Get album", true),
	}, a.mcpGetAlbum)

	mcp.AddTool(server, &mcp.Tool{
		Name: "get_release_tracks",
		Description: "The tracklist of one edition (release), with the file identified as each track where there " +
			"is one. May fetch from MusicBrainz, which can take a second or more.",
		Annotations: readOnlyTool("Get release tracks", true),
	}, a.mcpGetReleaseTracks)
}

func (a *API) mcpListArtists(_ context.Context, _ *mcp.CallToolRequest, in mcpListArtistsInput) (*mcp.CallToolResult, mcpArtistList, error) {
	all, err := a.artistList()
	if err != nil {
		return nil, mcpArtistList{}, mcpStoreError(err)
	}
	keep, err := mcpArtistFilter(in.Filter)
	if err != nil {
		return nil, mcpArtistList{}, err
	}
	needle := strings.ToLower(strings.TrimSpace(in.NameContains))

	matched := make([]artistSummary, 0, len(all))
	for _, ar := range all {
		if needle != "" && !strings.Contains(strings.ToLower(ar.Name), needle) {
			continue
		}
		if keep(ar) {
			matched = append(matched, ar)
		}
	}

	lo, hi := in.window(len(matched), mcpMaxArtists)
	out := mcpArtistList{
		mcpPage: newMCPPage(int64(len(matched)), lo, hi-lo),
		Artists: make([]mcpArtistRow, 0, hi-lo),
	}
	for _, ar := range matched[lo:hi] {
		out.Artists = append(out.Artists, mcpArtistRow{
			MBID: ar.MBID, Name: ar.Name, Monitored: ar.Monitored, ManagedBy: ar.ManagedBy,
			Owned: ar.OwnedCount, Complete: ar.CompleteCount, Partial: ar.PartialCount,
			Missing: ar.MissingCount, Mismatch: ar.MismatchCount, Picked: ar.PickedCount,
		})
	}
	return nil, out, nil
}

func mcpArtistFilter(filter string) (func(artistSummary) bool, error) {
	switch strings.TrimSpace(filter) {
	case "":
		return func(artistSummary) bool { return true }, nil
	case "partial":
		return func(ar artistSummary) bool { return ar.PartialCount > 0 }, nil
	case "mismatch":
		return func(ar artistSummary) bool { return ar.MismatchCount > 0 }, nil
	case "picked":
		return func(ar artistSummary) bool { return ar.PickedCount > 0 }, nil
	case "monitored":
		return func(ar artistSummary) bool { return ar.Monitored }, nil
	case "unmonitored":
		return func(ar artistSummary) bool { return !ar.Monitored }, nil
	}
	return nil, fmt.Errorf("unknown filter %q — use partial, mismatch, picked, monitored or unmonitored; for wanted albums not owned, use list_missing", filter)
}

func (a *API) mcpGetArtist(_ context.Context, _ *mcp.CallToolRequest, in mcpGetArtistInput) (*mcp.CallToolResult, mcpArtistDetail, error) {
	detail, err := a.artistDetail(strings.TrimSpace(in.MBID))
	if err != nil {
		return nil, mcpArtistDetail{}, mcpArtistNotFound(in.MBID)
	}
	ar := detail.Artist
	out := mcpArtistDetail{
		MBID: ar.MBID, Name: ar.Name, Monitored: ar.Monitored, ManagedBy: ar.ManagedBy,
		FollowGoverns: ar.FollowGoverns, FollowTypes: ar.FollowTypes,
		FollowSecondary: ar.FollowSecondary, FollowFromYear: ar.FollowFromYear,
		Albums: make([]mcpAlbumRow, 0, len(detail.ReleaseGroups)),
	}
	for _, view := range detail.ReleaseGroups {
		out.Albums = append(out.Albums, newMCPAlbumRow(view))
	}
	if detail.Repair != nil {
		out.UnresolvableAlbums = detail.Repair.Albums
	}
	return nil, out, nil
}

// mcpListMissing is the one query phase 1 adds: the per-artist wanted rules applied
// across the collection at once, so "what am I missing" is one call rather than one
// get_artist per artist.
//
// An album is judged under each credited artist in the collection, exactly as it is
// on each of their pages — a collaboration can be wanted by following one artist and
// not the other — and listed once, under the first artist that wants it.
func (a *API) mcpListMissing(_ context.Context, _ *mcp.CallToolRequest, in mcpListMissingInput) (*mcp.CallToolResult, mcpMissingList, error) {
	rows, err := a.missingAlbums(strings.TrimSpace(in.ArtistMBID), in.IncludeIncomplete)
	if err != nil {
		return nil, mcpMissingList{}, mcpStoreError(err)
	}
	lo, hi := in.window(len(rows), mcpMaxArtists)
	return nil, mcpMissingList{mcpPage: newMCPPage(int64(len(rows)), lo, hi-lo), Albums: rows[lo:hi]}, nil
}

func (a *API) missingAlbums(onlyArtist string, includeIncomplete bool) ([]mcpMissingRow, error) {
	var artists []models.CollectionArtist
	if err := a.DB.Order("name").Find(&artists).Error; err != nil {
		return nil, errors.New("failed to list artists")
	}
	var groups []models.CollectionReleaseGroup
	if err := a.DB.Find(&groups).Error; err != nil {
		return nil, errors.New("failed to list release groups")
	}
	var desires []models.CollectionDesire
	if err := a.DB.Find(&desires).Error; err != nil {
		return nil, errors.New("failed to list desires")
	}
	rgMBIDs := make([]string, 0, len(groups))
	for _, rg := range groups {
		rgMBIDs = append(rgMBIDs, rg.MBID)
	}
	// Without the credit links an album is judged under its primary artist only,
	// which is what the artist list degrades to as well.
	credits, _ := collection.ArtistsByReleaseGroup(a.DB, rgMBIDs)

	byMBID := make(map[string]models.CollectionArtist, len(artists))
	for _, ar := range artists {
		byMBID[ar.MBID] = ar
	}
	catalogued := catalogChecked(artists)
	byGroup := desiresByGroup(desires)

	out := []mcpMissingRow{}
	for _, rg := range groups {
		for _, artistMBID := range creditedArtists(rg, credits) {
			artist, ok := byMBID[artistMBID]
			if !ok || (onlyArtist != "" && artistMBID != onlyArtist) {
				continue
			}
			view := newReleaseGroupView(rg, catalogued[artistMBID], artist, byGroup[rg.MBID])
			if !view.Wanted || (view.Owned && (view.Complete || !includeIncomplete)) {
				continue
			}
			out = append(out, mcpMissingRow{
				Artist:      mcpArtistRef{MBID: artist.MBID, Name: artist.Name},
				mcpAlbumRow: newMCPAlbumRow(view),
			})
			break
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Artist.Name != out[j].Artist.Name {
			return out[i].Artist.Name < out[j].Artist.Name
		}
		return out[i].FirstReleaseDate < out[j].FirstReleaseDate
	})
	return out, nil
}

func (a *API) mcpGetAlbum(_ context.Context, _ *mcp.CallToolRequest, in mcpGetAlbumInput) (*mcp.CallToolResult, mcpAlbumDetail, error) {
	detail, err := a.releaseGroupDetailData(strings.TrimSpace(in.ArtistMBID), strings.TrimSpace(in.AlbumMBID))
	if err != nil {
		return nil, mcpAlbumDetail{}, mcpArtistNotFound(in.ArtistMBID)
	}
	if detail.ReleaseGroup.Title == "" && len(detail.Editions) == 0 {
		return nil, mcpAlbumDetail{}, fmt.Errorf("album not found: %s under artist %s — get_artist lists the artist's album MBIDs", in.AlbumMBID, in.ArtistMBID)
	}

	wanted := map[string]bool{}
	for _, mbid := range detail.ReleaseGroup.DesiredReleases {
		wanted[mbid] = true
	}
	out := mcpAlbumDetail{
		Artist:   mcpArtistRef{MBID: detail.Artist.MBID, Name: detail.Artist.Name},
		Album:    newMCPAlbumRow(detail.ReleaseGroup),
		Editions: make([]mcpEditionRow, 0, len(detail.Editions)),
	}
	for _, ed := range detail.Editions {
		formats := make([]string, 0, len(ed.Media))
		tracks := 0
		for _, medium := range ed.Media {
			if medium.Format != "" {
				formats = append(formats, medium.Format)
			}
			tracks += medium.TrackCount
		}
		out.Editions = append(out.Editions, mcpEditionRow{
			MBID: ed.ID, Title: ed.Title, Date: ed.Date, Country: ed.Country, Disambiguation: ed.Disambiguation,
			Formats: strings.Join(formats, " + "), Tracks: tracks,
			Owned: ed.Owned, OwnedTracks: ed.OwnedTracks, Complete: ed.Complete, Wanted: wanted[ed.ID],
		})
	}
	return nil, out, nil
}

func (a *API) mcpGetReleaseTracks(_ context.Context, _ *mcp.CallToolRequest, in mcpGetReleaseTracksInput) (*mcp.CallToolResult, mcpReleaseTracks, error) {
	mbid := strings.TrimSpace(in.ReleaseMBID)
	if mbid == "" {
		return nil, mcpReleaseTracks{}, errors.New("release_mbid is required — get_album lists an album's editions")
	}
	release, err := a.meta().GetRelease(mbid)
	if err != nil {
		return nil, mcpReleaseTracks{}, fmt.Errorf("could not load release %s from MusicBrainz: %s", mbid, err.Error())
	}

	// Which of these tracks a file is identified as. One query for the release, keyed
	// by track ID — the identity a file is tagged with.
	var items []models.LibraryItem
	a.DB.Select("id", "mb_release_track_id").Where("mb_release_id = ?", release.ID).Find(&items)
	fileByTrack := make(map[string]string, len(items))
	for _, item := range items {
		if item.MBReleaseTrackID != "" {
			fileByTrack[item.MBReleaseTrackID] = item.ID.String()
		}
	}

	tracks := modules.ReleaseTracks(release)
	out := mcpReleaseTracks{
		MBID: release.ID, Title: release.Title, Date: release.Date, Country: release.Country,
		Disambiguation: release.Disambiguation, Tracks: make([]mcpTrackRow, 0, len(tracks)),
	}
	for _, t := range tracks {
		out.Tracks = append(out.Tracks, mcpTrackRow{
			Medium: t.Medium, MediumTitle: t.MediumTitle, Position: t.Position, Number: t.Number,
			Title: t.Title, LengthMS: t.Length, RecordingMBID: t.RecordingID, TrackMBID: t.TrackID,
			Video: t.Video, FileID: fileByTrack[t.TrackID],
		})
	}
	return nil, out, nil
}

func mcpArtistNotFound(mbid string) error {
	return fmt.Errorf("artist not found: %s — list_artists returns the artist MBIDs in this collection", mbid)
}
