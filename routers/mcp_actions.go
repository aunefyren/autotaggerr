package routers

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/aunefyren/autotaggerr/collection"
	"github.com/aunefyren/autotaggerr/metadata"
	"github.com/aunefyren/autotaggerr/models"
	"github.com/google/uuid"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// MCP tools that act, shown only to a credential that may write (see mcpHandler).
// Every one calls the command its REST handler calls (commands.go), so the guards —
// the Lidarr identity gates, the nothing-to-tag refusals, the queue's dedup — are the
// same whichever door the request came through.

// actionTool is the annotation of a tool that changes something. destructive marks
// one that overwrites or discards (files' tags, a pin, a want); idempotent one that
// repeating with the same arguments changes nothing further; openWorld one that may
// fetch from MusicBrainz.
func actionTool(title string, destructive, idempotent, openWorld bool) *mcp.ToolAnnotations {
	return &mcp.ToolAnnotations{
		Title: title, ReadOnlyHint: false,
		DestructiveHint: &destructive, IdempotentHint: idempotent, OpenWorldHint: &openWorld,
	}
}

// mcpToolError turns a command's error into the message an assistant reads; a
// command's errors are already written for whoever asked.
func mcpToolError(err error) error {
	var ae *apiError
	if errors.As(err, &ae) {
		return ae
	}
	return mcpStoreError(err)
}

// --- search ---

type mcpSearchArtistsInput struct {
	Query string `json:"query" jsonschema:"artist name to search MusicBrainz for"`
}

type mcpArtistHit struct {
	MBID           string `json:"mbid"`
	Name           string `json:"name"`
	Disambiguation string `json:"disambiguation,omitempty"`
	Type           string `json:"type,omitempty"`
	Country        string `json:"country,omitempty"`
	Score          int    `json:"score"`
	InCollection   bool   `json:"in_collection"`
}

type mcpArtistHits struct {
	Artists []mcpArtistHit `json:"artists"`
}

type mcpSearchReleasesInput struct {
	Query      string `json:"query,omitempty" jsonschema:"free text"`
	Artist     string `json:"artist,omitempty" jsonschema:"artist name"`
	ArtistMBID string `json:"artist_mbid,omitempty"`
	Title      string `json:"title,omitempty" jsonschema:"release title"`
	Date       string `json:"date,omitempty" jsonschema:"YYYY, YYYY-MM or YYYY-MM-DD"`
	Country    string `json:"country,omitempty" jsonschema:"two-letter release country, e.g. GB"`
	Format     string `json:"format,omitempty" jsonschema:"medium format, e.g. CD, Vinyl, Digital Media"`
	Tracks     int    `json:"tracks,omitempty" jsonschema:"total track count"`
	mcpPaging
}

type mcpReleaseHit struct {
	MBID           string `json:"mbid" jsonschema:"edition (release) MBID; pass to get_release_tracks or attach_file"`
	Title          string `json:"title"`
	Artist         string `json:"artist,omitempty"`
	Date           string `json:"date,omitempty"`
	Country        string `json:"country,omitempty"`
	Status         string `json:"status,omitempty"`
	Disambiguation string `json:"disambiguation,omitempty"`
	Formats        string `json:"formats,omitempty"`
	Tracks         int    `json:"tracks"`
	AlbumMBID      string `json:"album_mbid,omitempty" jsonschema:"its release-group; pass to want_album"`
	AlbumType      string `json:"album_type,omitempty"`
	Score          int    `json:"score"`
}

type mcpReleaseHits struct {
	mcpPage
	Releases []mcpReleaseHit `json:"releases"`
}

type mcpBrowseArtistInput struct {
	ArtistMBID string `json:"artist_mbid" jsonschema:"any MusicBrainz artist, in the collection or not"`
}

type mcpUpstreamAlbum struct {
	MBID             string `json:"mbid" jsonschema:"release-group MBID; pass to want_album"`
	Title            string `json:"title"`
	PrimaryType      string `json:"primary_type,omitempty"`
	SecondaryTypes   string `json:"secondary_types,omitempty"`
	FirstReleaseDate string `json:"first_release_date,omitempty"`
	InCollection     bool   `json:"in_collection"`
}

type mcpUpstreamAlbums struct {
	Albums []mcpUpstreamAlbum `json:"albums"`
}

func (a *API) addMCPSearchTools(server *mcp.Server) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        "search_artists",
		Description: "Search MusicBrainz for artists by name — the way to an artist MBID for add_artist. One rate-limited request.",
		Annotations: readOnlyTool("Search artists", true),
	}, a.mcpSearchArtists)

	mcp.AddTool(server, &mcp.Tool{
		Name: "search_releases",
		Description: "Search MusicBrainz for editions (releases) by any mix of text, artist, title, date, country, " +
			"format and track count — the way to the release and album MBIDs for attach_file and want_album. " +
			"One rate-limited request per page.",
		Annotations: readOnlyTool("Search releases", true),
	}, a.mcpSearchReleases)

	mcp.AddTool(server, &mcp.Tool{
		Name: "browse_artist_albums",
		Description: "Every album (release-group) MusicBrainz lists for an artist, whether or not they are in the " +
			"collection. For an artist in it, get_artist answers from the database instead.",
		Annotations: readOnlyTool("Browse artist albums", true),
	}, a.mcpBrowseArtistAlbums)
}

func (a *API) mcpSearchArtists(_ context.Context, _ *mcp.CallToolRequest, in mcpSearchArtistsInput) (*mcp.CallToolResult, mcpArtistHits, error) {
	query := strings.TrimSpace(in.Query)
	if query == "" {
		return nil, mcpArtistHits{}, errors.New("query is required")
	}
	results, err := a.meta().SearchArtists(query)
	if err != nil {
		return nil, mcpArtistHits{}, fmt.Errorf("MusicBrainz search failed: %s", err.Error())
	}
	known := a.collectionArtistSet()
	out := mcpArtistHits{Artists: make([]mcpArtistHit, 0, len(results))}
	for _, r := range results {
		out.Artists = append(out.Artists, mcpArtistHit{
			MBID: r.ID, Name: r.Name, Disambiguation: r.Disambiguation, Type: r.Type,
			Country: r.Country, Score: r.Score, InCollection: known[r.ID],
		})
	}
	return nil, out, nil
}

func (a *API) mcpSearchReleases(_ context.Context, _ *mcp.CallToolRequest, in mcpSearchReleasesInput) (*mcp.CallToolResult, mcpReleaseHits, error) {
	query := metadata.ReleaseSearchQuery{
		Text: in.Query, Artist: in.Artist, ArtistID: in.ArtistMBID, Release: in.Title,
		Date: in.Date, Country: in.Country, Format: in.Format, Tracks: in.Tracks,
		Limit: min(in.limit(), 100), Offset: max(in.Offset, 0),
	}
	if query.Empty() {
		return nil, mcpReleaseHits{}, errors.New("give at least one of query, artist, artist_mbid, title, date, country, format or tracks")
	}
	page, err := a.meta().SearchReleases(query)
	if err != nil {
		return nil, mcpReleaseHits{}, fmt.Errorf("MusicBrainz search failed: %s", err.Error())
	}
	out := mcpReleaseHits{
		mcpPage:  newMCPPage(int64(page.Count), page.Offset, len(page.Releases)),
		Releases: make([]mcpReleaseHit, 0, len(page.Releases)),
	}
	for _, r := range page.Releases {
		formats := make([]string, 0, len(r.Media))
		tracks := 0
		for _, medium := range r.Media {
			if medium.Format != "" {
				formats = append(formats, medium.Format)
			}
			tracks += medium.TrackCount
		}
		var artist strings.Builder
		for _, credit := range r.ArtistCredit {
			artist.WriteString(credit.Name + credit.Joinphrase)
		}
		out.Releases = append(out.Releases, mcpReleaseHit{
			MBID: r.ID, Title: r.Title, Artist: artist.String(), Date: r.Date, Country: r.Country,
			Status: r.Status, Disambiguation: r.Disambiguation, Formats: strings.Join(formats, " + "),
			Tracks: tracks, AlbumMBID: r.ReleaseGroup.ID, AlbumType: r.ReleaseGroup.PrimaryType, Score: r.Score,
		})
	}
	return nil, out, nil
}

func (a *API) mcpBrowseArtistAlbums(_ context.Context, _ *mcp.CallToolRequest, in mcpBrowseArtistInput) (*mcp.CallToolResult, mcpUpstreamAlbums, error) {
	mbid := strings.TrimSpace(in.ArtistMBID)
	if mbid == "" {
		return nil, mcpUpstreamAlbums{}, errors.New("artist_mbid is required — search_artists finds one")
	}
	groups, _, err := a.meta().GetArtistReleaseGroups(mbid)
	if err != nil {
		return nil, mcpUpstreamAlbums{}, fmt.Errorf("could not load the artist's albums from MusicBrainz: %s", err.Error())
	}
	var stored []string
	a.DB.Model(&models.CollectionReleaseGroup{}).Where("artist_mb_id = ?", mbid).Pluck("mb_id", &stored)
	inCollection := make(map[string]bool, len(stored))
	for _, id := range stored {
		inCollection[id] = true
	}
	out := mcpUpstreamAlbums{Albums: make([]mcpUpstreamAlbum, 0, len(groups))}
	for _, g := range groups {
		out.Albums = append(out.Albums, mcpUpstreamAlbum{
			MBID: g.ID, Title: g.Title, PrimaryType: g.PrimaryType,
			SecondaryTypes: strings.Join(g.SecondaryTypes, ", "), FirstReleaseDate: g.FirstReleaseDate,
			InCollection: inCollection[g.ID],
		})
	}
	return nil, out, nil
}

func (a *API) collectionArtistSet() map[string]bool {
	var ids []string
	a.DB.Model(&models.CollectionArtist{}).Pluck("mb_id", &ids)
	out := make(map[string]bool, len(ids))
	for _, id := range ids {
		out[id] = true
	}
	return out
}

// --- the four verbs ---

type mcpVerbInput struct {
	LibraryID  string `json:"library_id,omitempty" jsonschema:"only this library (ids from get_configuration)"`
	ArtistMBID string `json:"artist_mbid,omitempty" jsonschema:"only this artist; give neither for everything"`
}

func (in mcpVerbInput) target() (verbTarget, error) {
	libraryID, artist := strings.TrimSpace(in.LibraryID), strings.TrimSpace(in.ArtistMBID)
	if libraryID != "" && artist != "" {
		return verbTarget{}, errors.New("give library_id or artist_mbid, not both")
	}
	if libraryID == "" {
		return verbTarget{ArtistMBID: artist}, nil
	}
	id, err := uuid.Parse(libraryID)
	if err != nil {
		return verbTarget{}, errors.New("library_id must be a library id from get_configuration")
	}
	return verbTarget{LibraryID: &id}, nil
}

type mcpRefreshInput struct {
	mcpVerbInput
	Force bool `json:"force,omitempty" jsonschema:"artist scope only: ignore the cache and re-read everything. Slow; for when cached data is known to be wrong"`
}

type mcpScanInput struct {
	ArtistMBID string `json:"artist_mbid,omitempty" jsonschema:"only this artist; omit for the whole collection"`
	WalkDisk   bool   `json:"walk_disk,omitempty" jsonschema:"walk the folders first to find files that moved or arrived; queued instead of inline"`
}

type mcpQueued struct {
	Status  string `json:"status"`
	Library string `json:"library,omitempty"`
	Artist  string `json:"artist,omitempty"`
	Files   int    `json:"files,omitempty" jsonschema:"files the job will rewrite"`
	Folders any    `json:"folders,omitempty" jsonschema:"artist folders the job will walk"`
	Follow  string `json:"follow" jsonschema:"where to watch the job"`
}

const mcpFollowJob = "queued: watch get_overview (jobs) for progress and list_activity for the result"

func newMCPQueued(res verbResult) mcpQueued {
	return mcpQueued{Status: res.Status, Library: res.Library, Artist: res.Artist, Files: res.Files, Folders: res.Folders, Follow: mcpFollowJob}
}

type mcpScanResult struct {
	Queued        bool   `json:"queued"`
	Artist        string `json:"artist,omitempty"`
	Artists       int    `json:"artists,omitempty"`
	OwnedAlbums   int    `json:"owned_albums,omitempty"`
	CreditChanges int    `json:"credit_changes,omitempty"`
	FilesRemoved  int    `json:"files_removed,omitempty" jsonschema:"index rows dropped because their file is gone"`
	EmptyReason   string `json:"empty_reason,omitempty" jsonschema:"why the pass had nothing to read"`
	Follow        string `json:"follow,omitempty"`
	Completed     bool   `json:"completed,omitempty" jsonschema:"the scan ran inline and these are its results"`
}

type mcpCancelResult struct {
	Stopping bool `json:"stopping"`
	Job      any  `json:"job" jsonschema:"the job asked to stop; it finishes the files it already started"`
}

func (a *API) addMCPVerbTools(server *mcp.Server) {
	mcp.AddTool(server, &mcp.Tool{
		Name: "process",
		Description: "Queue the full pipeline — walk folders, identify files, write their tags — for everything, " +
			"one library or one artist. The only verb that finds new files. Writes to audio files.",
		Annotations: actionTool("Process", true, true, true),
	}, a.mcpProcess)

	mcp.AddTool(server, &mcp.Tool{
		Name: "scan",
		Description: "Re-derive the collection (owned albums) from the files already indexed, for everything or one " +
			"artist. Inline and cheap; drops index rows whose file is gone. walk_disk walks the folders first (queued). " +
			"Writes no audio file.",
		Annotations: actionTool("Scan", false, true, false),
	}, a.mcpScan)

	mcp.AddTool(server, &mcp.Tool{
		Name: "refresh_metadata",
		Description: "Queue a re-read of MusicBrainz metadata that is due, for everything, one library or one " +
			"artist. Writes no files: what changed is applied by the next process or tag_files.",
		Annotations: actionTool("Refresh metadata", false, true, true),
	}, a.mcpRefresh)

	mcp.AddTool(server, &mcp.Tool{
		Name: "tag_files",
		Description: "Queue rewriting indexed files' tags from their stored identity, for everything, one library " +
			"or one artist. No folder walk, no network. Writes to audio files.",
		Annotations: actionTool("Tag files", true, true, false),
	}, a.mcpTag)

	mcp.AddTool(server, &mcp.Tool{
		Name:        "cancel_job",
		Description: "Ask the running job to stop between files. Jobs queued behind it are left alone.",
		Annotations: actionTool("Cancel job", false, true, false),
	}, a.mcpCancel)
}

func (a *API) mcpProcess(_ context.Context, _ *mcp.CallToolRequest, in mcpVerbInput) (*mcp.CallToolResult, mcpQueued, error) {
	t, err := in.target()
	if err != nil {
		return nil, mcpQueued{}, err
	}
	res, err := a.queueProcess(t)
	if err != nil {
		return nil, mcpQueued{}, mcpToolError(err)
	}
	return nil, newMCPQueued(res), nil
}

func (a *API) mcpRefresh(_ context.Context, _ *mcp.CallToolRequest, in mcpRefreshInput) (*mcp.CallToolResult, mcpQueued, error) {
	t, err := in.target()
	if err != nil {
		return nil, mcpQueued{}, err
	}
	if in.Force && t.ArtistMBID == "" {
		return nil, mcpQueued{}, errors.New("force is offered for one artist only")
	}
	res, err := a.queueRefresh(t, in.Force)
	if err != nil {
		return nil, mcpQueued{}, mcpToolError(err)
	}
	return nil, newMCPQueued(res), nil
}

func (a *API) mcpTag(_ context.Context, _ *mcp.CallToolRequest, in mcpVerbInput) (*mcp.CallToolResult, mcpQueued, error) {
	t, err := in.target()
	if err != nil {
		return nil, mcpQueued{}, err
	}
	res, err := a.queueTag(t)
	if err != nil {
		return nil, mcpQueued{}, mcpToolError(err)
	}
	return nil, newMCPQueued(res), nil
}

func (a *API) mcpScan(_ context.Context, _ *mcp.CallToolRequest, in mcpScanInput) (*mcp.CallToolResult, mcpScanResult, error) {
	res, err := a.runScan(verbTarget{ArtistMBID: strings.TrimSpace(in.ArtistMBID)}, in.WalkDisk)
	if err != nil {
		return nil, mcpScanResult{}, mcpToolError(err)
	}
	if res.Queued {
		return nil, mcpScanResult{Queued: true, Artist: res.Artist, Follow: mcpFollowJob}, nil
	}
	return nil, mcpScanResult{
		Artist: res.Artist, Artists: res.Stats.Artists, OwnedAlbums: res.Stats.Owned,
		CreditChanges: res.Stats.CreditChanges, FilesRemoved: res.Stats.FilesRemoved,
		EmptyReason: res.Stats.EmptyReason, Completed: true,
	}, nil
}

func (a *API) mcpCancel(_ context.Context, _ *mcp.CallToolRequest, _ mcpNoInput) (*mcp.CallToolResult, mcpCancelResult, error) {
	if a.Scan == nil {
		return nil, mcpCancelResult{}, errProcessorUnavailable
	}
	job, ok := a.Scan.CancelCurrent()
	if !ok {
		return nil, mcpCancelResult{}, errors.New("nothing is running")
	}
	return nil, mcpCancelResult{Stopping: true, Job: job}, nil
}

// --- collection edits ---

type mcpAddArtistInput struct {
	MBID string `json:"mbid" jsonschema:"artist MBID from search_artists"`
	Name string `json:"name,omitempty" jsonschema:"looked up from MusicBrainz when omitted"`
}

type mcpAddArtistResult struct {
	mcpArtistRef
	AlreadyInCollection bool   `json:"already_in_collection"`
	Next                string `json:"next"`
}

type mcpFollowInput struct {
	MBID            string  `json:"mbid" jsonschema:"artist MBID in the collection"`
	Monitored       *bool   `json:"monitored,omitempty" jsonschema:"follow (true) or stop following (false)"`
	FollowTypes     *string `json:"follow_types,omitempty" jsonschema:"comma-separated primary types following wants, e.g. Album,EP"`
	FollowSecondary *bool   `json:"follow_secondary,omitempty" jsonschema:"also want secondary types (live, compilation, ...)"`
	FollowFromYear  *int    `json:"follow_from_year,omitempty" jsonschema:"only want albums first released in or after this year; 0 clears it"`
}

type mcpFollowResult struct {
	mcpArtistRef
	Monitored       bool   `json:"monitored"`
	FollowTypes     string `json:"follow_types,omitempty"`
	FollowSecondary bool   `json:"follow_secondary"`
	FollowFromYear  int    `json:"follow_from_year,omitempty"`
	FollowGoverns   bool   `json:"follow_governs" jsonschema:"false when a manager decides what is wanted, so these settings are stored but not obeyed"`
	Wanted          int    `json:"wanted" jsonschema:"albums following now wants"`
}

type mcpWantInput struct {
	ArtistMBID     string   `json:"artist_mbid" jsonschema:"artist in the collection; add_artist first if not"`
	AlbumMBID      string   `json:"album_mbid" jsonschema:"release-group MBID"`
	ReleaseMBID    string   `json:"release_mbid,omitempty" jsonschema:"want this edition specifically; omit for any edition"`
	RecordingMBIDs []string `json:"recording_mbids,omitempty" jsonschema:"want only these songs; omit for the whole album"`
}

type mcpWantResult struct {
	AlbumMBID   string `json:"album_mbid"`
	Title       string `json:"title,omitempty"`
	ReleaseMBID string `json:"release_mbid,omitempty"`
	Songs       int    `json:"songs,omitempty"`
	Wanted      bool   `json:"wanted"`
}

type mcpUnwantInput struct {
	AlbumMBID   string `json:"album_mbid"`
	ReleaseMBID string `json:"release_mbid,omitempty" jsonschema:"clear only the want for this edition; omit for the album want"`
}

type mcpFileIDInput struct {
	FileID string `json:"file_id" jsonschema:"file id from list_files"`
}

type mcpAttachInput struct {
	FileID      string `json:"file_id" jsonschema:"file id from list_files"`
	ReleaseMBID string `json:"release_mbid" jsonschema:"edition (release) MBID from search_releases or get_album"`
	TrackMBID   string `json:"track_mbid" jsonschema:"track MBID from get_release_tracks (track_mbid, not recording_mbid)"`
}

type mcpTagOutcome struct {
	Done        bool   `json:"done"`
	TagsWritten int    `json:"tags_written"`
	Warning     string `json:"warning,omitempty" jsonschema:"the identity was saved but writing tags failed; the next run retries"`
}

type mcpMigrationIDInput struct {
	ID string `json:"id" jsonschema:"migration id from list_migrations"`
}

type mcpMigrationDecision struct {
	Status     string `json:"status" jsonschema:"the migration's status after the decision"`
	Queued     bool   `json:"queued,omitempty" jsonschema:"approving asked the manager to re-read the artist instead of applying anything; follow in list_activity"`
	ArtistMBID string `json:"artist_mbid,omitempty"`
	ArtistName string `json:"artist_name,omitempty"`
	Message    string `json:"message,omitempty"`
}

func (a *API) addMCPEditTools(server *mcp.Server) {
	mcp.AddTool(server, &mcp.Tool{
		Name: "add_artist",
		Description: "Add an artist to the collection by MBID, before any of their files are owned. Idempotent. " +
			"Then follow_artist to want their albums, or want_album for specific ones.",
		Annotations: actionTool("Add artist", false, true, true),
	}, a.mcpAddArtist)

	mcp.AddTool(server, &mcp.Tool{
		Name: "follow_artist",
		Description: "Change how an artist is followed — on or off, which album types, secondary types, a year " +
			"cutoff — then re-sync their discography from MusicBrainz so the wanted albums match at once.",
		Annotations: actionTool("Follow artist", false, true, true),
	}, a.mcpFollowArtist)

	mcp.AddTool(server, &mcp.Tool{
		Name: "want_album",
		Description: "Explicitly want an album, optionally one edition or only some songs. Refused for an artist " +
			"managed by Lidarr, where wants are Lidarr's to set.",
		Annotations: actionTool("Want album", false, true, true),
	}, a.mcpWantAlbum)

	mcp.AddTool(server, &mcp.Tool{
		Name: "unwant_album",
		Description: "Remove an explicit want. Owned files are never touched. A want that comes from following or " +
			"from the manager is not removed by this — change follow_artist or the manager instead.",
		Annotations: actionTool("Unwant album", true, true, false),
	}, a.mcpUnwantAlbum)

	mcp.AddTool(server, &mcp.Tool{
		Name: "attach_file",
		Description: "Identify a file by hand: pin it to a release and track, then write its tags. The pin " +
			"outlives automatic resolution. Refused for files in a Lidarr-managed library. Writes to the audio file.",
		Annotations: actionTool("Attach file", true, true, true),
	}, a.mcpAttachFile)

	mcp.AddTool(server, &mcp.Tool{
		Name: "release_pin",
		Description: "Remove a file's manual pin, handing it back to automatic identification on the next run. " +
			"Its tags are left as they are.",
		Annotations: actionTool("Release pin", true, true, false),
	}, a.mcpReleasePin)

	mcp.AddTool(server, &mcp.Tool{
		Name: "confirm_pin",
		Description: "Keep a held pin (pin_held) for the file's new audio, and write its tags. Only a held pin can " +
			"be confirmed. Writes to the audio file.",
		Annotations: actionTool("Confirm pin", true, false, false),
	}, a.mcpConfirmPin)

	mcp.AddTool(server, &mcp.Tool{
		Name: "approve_migration",
		Description: "Apply a held MusicBrainz identity change (merge, re-key or deletion), re-pointing the " +
			"collection's records and files. For an album only the manager can settle, it queues a manager " +
			"refresh instead. Check list_migrations' effect first.",
		Annotations: actionTool("Approve migration", true, false, false),
	}, a.mcpApproveMigration)

	mcp.AddTool(server, &mcp.Tool{
		Name:        "dismiss_migration",
		Description: "Record that a detected identity change will deliberately not be applied.",
		Annotations: actionTool("Dismiss migration", true, false, false),
	}, a.mcpDismissMigration)
}

func (a *API) mcpAddArtist(_ context.Context, _ *mcp.CallToolRequest, in mcpAddArtistInput) (*mcp.CallToolResult, mcpAddArtistResult, error) {
	mbid, name := strings.TrimSpace(in.MBID), strings.TrimSpace(in.Name)
	if mbid == "" {
		return nil, mcpAddArtistResult{}, errors.New("mbid is required — search_artists finds one")
	}
	_, lookupErr := a.targetArtist(mbid)
	existed := lookupErr == nil
	if !existed && name == "" {
		found, err := a.meta().GetArtist(mbid)
		if err != nil || found.Name == "" {
			return nil, mcpAddArtistResult{}, fmt.Errorf("could not find artist %s at MusicBrainz — check the MBID with search_artists", mbid)
		}
		name = found.Name
	}
	artist, err := collection.AddArtist(a.DB, mbid, name)
	if err != nil {
		return nil, mcpAddArtistResult{}, err
	}
	return nil, mcpAddArtistResult{
		mcpArtistRef: mcpArtistRef{MBID: artist.MBID, Name: artist.Name}, AlreadyInCollection: existed,
		Next: "follow_artist to want their albums, or want_album for specific ones",
	}, nil
}

func (a *API) mcpFollowArtist(_ context.Context, _ *mcp.CallToolRequest, in mcpFollowInput) (*mcp.CallToolResult, mcpFollowResult, error) {
	artist, wanted, err := a.updateFollowSettings(in.MBID, followUpdate{
		Monitored: in.Monitored, FollowTypes: in.FollowTypes,
		FollowSecondary: in.FollowSecondary, FollowFromYear: in.FollowFromYear,
	})
	if err != nil {
		var ae *apiError
		if errors.As(err, &ae) && ae.message == "artist not found" {
			return nil, mcpFollowResult{}, mcpArtistNotFound(in.MBID)
		}
		return nil, mcpFollowResult{}, mcpToolError(err)
	}
	return nil, mcpFollowResult{
		mcpArtistRef: mcpArtistRef{MBID: artist.MBID, Name: artist.Name},
		Monitored:    artist.Monitored, FollowTypes: artist.FollowTypes, FollowSecondary: artist.FollowSecondary,
		FollowFromYear: artist.FollowFromYear, FollowGoverns: collection.FollowGoverns(artist), Wanted: wanted,
	}, nil
}

// mcpWantAlbum fills in what the album is from the collection's own row, or from the
// artist's MusicBrainz discography for an album the collection has not seen yet — the
// UI sends those fields from the row it was clicked on, and an assistant has only the
// MBID.
func (a *API) mcpWantAlbum(_ context.Context, _ *mcp.CallToolRequest, in mcpWantInput) (*mcp.CallToolResult, mcpWantResult, error) {
	artist, err := a.targetArtist(in.ArtistMBID)
	if err != nil {
		return nil, mcpWantResult{}, fmt.Errorf("artist not in the collection: %s — add_artist first", in.ArtistMBID)
	}
	albumMBID := strings.TrimSpace(in.AlbumMBID)
	if albumMBID == "" {
		return nil, mcpWantResult{}, errors.New("album_mbid is required")
	}
	desire := collection.DesireInput{
		ArtistMBID: artist.MBID, ReleaseGroupMBID: albumMBID,
		ReleaseMBID: strings.TrimSpace(in.ReleaseMBID), RecordingMBIDs: in.RecordingMBIDs,
	}
	var row models.CollectionReleaseGroup
	if a.DB.Where("mb_id = ?", albumMBID).Limit(1).Find(&row).RowsAffected > 0 {
		desire.Title, desire.PrimaryType, desire.SecondaryTypes, desire.FirstReleaseDate =
			row.Title, row.PrimaryType, row.SecondaryTypes, row.FirstReleaseDate
	} else {
		groups, _, err := a.meta().GetArtistReleaseGroups(artist.MBID)
		if err != nil {
			return nil, mcpWantResult{}, fmt.Errorf("could not read %s's albums from MusicBrainz: %s", artist.Name, err.Error())
		}
		found := false
		for _, g := range groups {
			if g.ID == albumMBID {
				desire.Title, desire.PrimaryType = g.Title, g.PrimaryType
				desire.SecondaryTypes, desire.FirstReleaseDate = strings.Join(g.SecondaryTypes, ", "), g.FirstReleaseDate
				found = true
				break
			}
		}
		if !found {
			return nil, mcpWantResult{}, fmt.Errorf("album %s is not among %s's albums at MusicBrainz — browse_artist_albums lists them", albumMBID, artist.Name)
		}
	}

	saved, err := a.addDesire(desire)
	if err != nil {
		return nil, mcpWantResult{}, mcpToolError(err)
	}
	return nil, mcpWantResult{
		AlbumMBID: saved.ReleaseGroupMBID, Title: desire.Title, ReleaseMBID: saved.ReleaseMBID,
		Songs: len(saved.RecordingMBIDs), Wanted: true,
	}, nil
}

func (a *API) mcpUnwantAlbum(_ context.Context, _ *mcp.CallToolRequest, in mcpUnwantInput) (*mcp.CallToolResult, mcpWantResult, error) {
	albumMBID := strings.TrimSpace(in.AlbumMBID)
	if albumMBID == "" {
		return nil, mcpWantResult{}, errors.New("album_mbid is required")
	}
	// Deliberately not gated on the manager, like the REST route: clearing is a pure
	// removal, and a want left from before an artist became Lidarr-managed needs a way out.
	if err := collection.ClearDesire(a.DB, albumMBID, in.ReleaseMBID); err != nil {
		return nil, mcpWantResult{}, mcpStoreError(errors.New("failed to clear the want"))
	}
	return nil, mcpWantResult{AlbumMBID: albumMBID, ReleaseMBID: strings.TrimSpace(in.ReleaseMBID), Wanted: false}, nil
}

func parseFileID(raw string) (uuid.UUID, error) {
	id, err := uuid.Parse(strings.TrimSpace(raw))
	if err != nil {
		return id, errors.New("file_id must be a file id from list_files")
	}
	return id, nil
}

func newMCPTagOutcome(out tagOutcome) mcpTagOutcome {
	return mcpTagOutcome{Done: true, TagsWritten: out.TagsWritten, Warning: out.Warning}
}

func (a *API) mcpAttachFile(_ context.Context, _ *mcp.CallToolRequest, in mcpAttachInput) (*mcp.CallToolResult, mcpTagOutcome, error) {
	id, err := parseFileID(in.FileID)
	if err != nil {
		return nil, mcpTagOutcome{}, err
	}
	out, err := a.attachFile(id, in.ReleaseMBID, in.TrackMBID)
	if err != nil {
		return nil, mcpTagOutcome{}, mcpToolError(err)
	}
	return nil, newMCPTagOutcome(out), nil
}

func (a *API) mcpReleasePin(_ context.Context, _ *mcp.CallToolRequest, in mcpFileIDInput) (*mcp.CallToolResult, mcpTagOutcome, error) {
	id, err := parseFileID(in.FileID)
	if err != nil {
		return nil, mcpTagOutcome{}, err
	}
	if err := a.releasePin(id); err != nil {
		return nil, mcpTagOutcome{}, mcpToolError(err)
	}
	return nil, mcpTagOutcome{Done: true}, nil
}

func (a *API) mcpConfirmPin(_ context.Context, _ *mcp.CallToolRequest, in mcpFileIDInput) (*mcp.CallToolResult, mcpTagOutcome, error) {
	id, err := parseFileID(in.FileID)
	if err != nil {
		return nil, mcpTagOutcome{}, err
	}
	out, err := a.confirmHeldPin(id)
	if err != nil {
		return nil, mcpTagOutcome{}, mcpToolError(err)
	}
	return nil, newMCPTagOutcome(out), nil
}

func parseMigrationID(raw string) (uuid.UUID, error) {
	id, err := uuid.Parse(strings.TrimSpace(raw))
	if err != nil {
		return id, errors.New("id must be a migration id from list_migrations")
	}
	return id, nil
}

func (a *API) mcpApproveMigration(_ context.Context, _ *mcp.CallToolRequest, in mcpMigrationIDInput) (*mcp.CallToolResult, mcpMigrationDecision, error) {
	id, err := parseMigrationID(in.ID)
	if err != nil {
		return nil, mcpMigrationDecision{}, err
	}
	out, err := a.approveMigrationByID(id)
	if err != nil {
		return nil, mcpMigrationDecision{}, mcpToolError(err)
	}
	if out.Queued {
		return nil, mcpMigrationDecision{
			Status: "manager refresh queued", Queued: true,
			ArtistMBID: out.ArtistMBID, ArtistName: out.ArtistName, Message: out.Message,
		}, nil
	}
	return nil, mcpMigrationDecision{Status: out.Migration.Status}, nil
}

func (a *API) mcpDismissMigration(_ context.Context, _ *mcp.CallToolRequest, in mcpMigrationIDInput) (*mcp.CallToolResult, mcpMigrationDecision, error) {
	id, err := parseMigrationID(in.ID)
	if err != nil {
		return nil, mcpMigrationDecision{}, err
	}
	row, err := a.dismissMigrationByID(id)
	if err != nil {
		return nil, mcpMigrationDecision{}, mcpToolError(err)
	}
	return nil, mcpMigrationDecision{Status: row.Status}, nil
}
