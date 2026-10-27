package routers

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/aunefyren/autotaggerr/migration"
	"github.com/aunefyren/autotaggerr/models"
	"github.com/google/uuid"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// MCP tools for the state of the instance: the overview, the Activity feed, the
// migration queue and the configured components.

type mcpNoInput struct{}

type mcpOverview struct {
	Name    string `json:"name"`
	Version string `json:"version"`
	Counts  struct {
		Libraries      int64 `json:"libraries"`
		Managers       int64 `json:"managers"`
		DataSources    int64 `json:"data_sources"`
		TaggerProfiles int64 `json:"tagger_profiles"`
		Files          int64 `json:"files" jsonschema:"files in the index"`
		Artists        int64 `json:"artists" jsonschema:"artists in the collection"`
	} `json:"counts"`
	Attention any `json:"attention" jsonschema:"files waiting on something: errors (need a person), retrying (transient, will clear by themselves), pins_held, unmatched_wait (waiting on the manager); each is a list_files filter"`
	// PendingMigrations is the migration queue's length — identity changes held for
	// approval.
	PendingMigrations int64 `json:"pending_migrations" jsonschema:"MusicBrainz identity changes held for approval; see list_migrations"`
	Jobs              any   `json:"jobs,omitempty" jsonschema:"the serial job queue: what is running and what waits behind it"`
	MetadataRefresh   any   `json:"metadata_refresh,omitempty" jsonschema:"the MusicBrainz mirror refresh pass"`
	ArtworkRefresh    any   `json:"artwork_refresh,omitempty"`
	TempoAnalysis     any   `json:"tempo_analysis,omitempty"`
}

type mcpEventRow struct {
	ID          string     `json:"id"`
	Type        string     `json:"type"`
	Status      string     `json:"status" jsonschema:"running, ok or error"`
	Title       string     `json:"title"`
	Summary     string     `json:"summary,omitempty"`
	StartedAt   time.Time  `json:"started_at"`
	FinishedAt  *time.Time `json:"finished_at,omitempty"`
	ParentID    string     `json:"parent_id,omitempty" jsonschema:"the run this stage belongs to"`
	ParentTitle string     `json:"parent_title,omitempty"`
	ChildCount  int        `json:"child_count,omitempty" jsonschema:"stages this run spawned; list them with list_activity(parent_id)"`
	Total       int        `json:"total,omitempty"`
	Done        int        `json:"done,omitempty"`
	Phase       string     `json:"phase,omitempty"`
}

func newMCPEventRow(ev models.Event) mcpEventRow {
	row := mcpEventRow{
		ID: ev.ID.String(), Type: ev.Type, Status: ev.Status, Title: ev.Title, Summary: ev.Summary,
		StartedAt: ev.StartedAt, FinishedAt: ev.FinishedAt, ParentTitle: ev.ParentTitle,
		ChildCount: ev.ChildCount, Total: ev.Total, Done: ev.Done, Phase: ev.Phase,
	}
	if ev.ParentID != nil {
		row.ParentID = ev.ParentID.String()
	}
	return row
}

type mcpListActivityInput struct {
	Type          string `json:"type,omitempty" jsonschema:"event type, e.g. process, scan, refresh, retag, mirror, artwork, tempo, health, migration"`
	Status        string `json:"status,omitempty" jsonschema:"running, ok or error"`
	TitleContains string `json:"title_contains,omitempty" jsonschema:"case-insensitive substring of the title"`
	ParentID      string `json:"parent_id,omitempty" jsonschema:"one run and the stages it spawned"`
	RunsOnly      bool   `json:"runs_only,omitempty" jsonschema:"only top-level runs, no stages"`
	mcpPaging
}

type mcpActivityList struct {
	mcpPage
	Events []mcpEventRow `json:"events"`
}

type mcpGetActivityInput struct {
	ID        string `json:"id" jsonschema:"event id from list_activity"`
	ItemLimit int    `json:"item_limit,omitempty" jsonschema:"per-file detail rows to return; default 100, at most 500"`
}

type mcpEventItem struct {
	Path        string `json:"path,omitempty"`
	Kind        string `json:"kind,omitempty"`
	Status      string `json:"status"`
	Phase       string `json:"phase,omitempty"`
	TagsWritten int    `json:"tags_written,omitempty"`
	Error       string `json:"error,omitempty"`
	// Related names what an MBID-keyed row is about, when the collection knows it.
	Related any `json:"related,omitempty" jsonschema:"what the row's identifier is in this collection: an artist, album or edition, and how many files point at it"`
}

type mcpActivityDetail struct {
	mcpEventRow
	Details        any            `json:"details,omitempty"`
	Stats          any            `json:"stats,omitempty"`
	Items          []mcpEventItem `json:"items"`
	ItemsTruncated bool           `json:"items_truncated,omitempty" jsonschema:"more detail rows are stored than were returned; raise item_limit"`
	Stages         []mcpEventRow  `json:"stages,omitempty" jsonschema:"the stages this run performed, in order"`
}

type mcpListMigrationsInput struct {
	Status string `json:"status,omitempty" jsonschema:"open (the queue: pending and failed), pending, failed, applied, dismissed or resolved; empty is every row"`
	Query  string `json:"query,omitempty" jsonschema:"matches the entity's name or old MBID"`
	mcpPaging
}

type mcpMigrationRow struct {
	ID              string     `json:"id"`
	EntityType      string     `json:"entity_type" jsonschema:"artist, release-group or release"`
	Kind            string     `json:"kind" jsonschema:"merge, rekey or deleted"`
	Status          string     `json:"status"`
	Name            string     `json:"name"`
	OldMBID         string     `json:"old_mbid"`
	NewMBID         string     `json:"new_mbid,omitempty"`
	ArtistMBID      string     `json:"artist_mbid,omitempty"`
	ArtistName      string     `json:"artist_name,omitempty"`
	AffectedFiles   int        `json:"affected_files"`
	AffectedDesires int        `json:"affected_desires"`
	TouchesPinned   bool       `json:"touches_pinned,omitempty"`
	Problem         string     `json:"problem" jsonschema:"what is wrong, in words"`
	Effect          string     `json:"effect" jsonschema:"what approving would do"`
	Blocker         string     `json:"blocker,omitempty"`
	DetectedAt      time.Time  `json:"detected_at"`
	ResolvedAt      *time.Time `json:"resolved_at,omitempty"`
	Error           string     `json:"error,omitempty"`
}

type mcpMigrationList struct {
	mcpPage
	Pending    int64             `json:"pending" jsonschema:"rows waiting on a decision, across all pages"`
	Migrations []mcpMigrationRow `json:"migrations"`
}

type mcpConfiguration struct {
	Libraries      []mcpLibraryRow       `json:"libraries"`
	Managers       []mcpManagerRow       `json:"managers"`
	DataSources    []mcpDataSourceRow    `json:"data_sources"`
	TaggerProfiles []mcpTaggerProfileRow `json:"tagger_profiles"`
}

type mcpLibraryRow struct {
	ID            string     `json:"id"`
	Name          string     `json:"name"`
	Path          string     `json:"path"`
	Enabled       bool       `json:"enabled"`
	Manager       string     `json:"manager,omitempty" jsonschema:"the correlation authority; empty means the native manager"`
	TaggerProfile string     `json:"tagger_profile,omitempty"`
	UseAcoustID   bool       `json:"use_acoustid"`
	AnalyseTempo  bool       `json:"analyse_tempo"`
	LastScan      *time.Time `json:"last_scan,omitempty"`
}

type mcpManagerRow struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Type    string `json:"type" jsonschema:"lidarr or autotaggerr (native)"`
	Enabled bool   `json:"enabled"`
	Health  string `json:"health,omitempty"`
	BaseURL string `json:"base_url,omitempty"`
}

type mcpDataSourceRow struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Type     string `json:"type"`
	Category string `json:"category" jsonschema:"metadata, fingerprint or artwork"`
	Enabled  bool   `json:"enabled"`
	Health   string `json:"health,omitempty"`
	BaseURL  string `json:"base_url,omitempty"`
}

type mcpTaggerProfileRow struct {
	ID                 string `json:"id"`
	Name               string `json:"name"`
	WriteTags          bool   `json:"write_tags"`
	MaxGenres          int    `json:"max_genres"`
	MP3MultiValueTags  bool   `json:"mp3_multi_value_tags"`
	SingleRecordLabel  bool   `json:"single_record_label"`
	NamingArtistFolder string `json:"naming_artist_folder,omitempty"`
	NamingAlbumFolder  string `json:"naming_album_folder,omitempty"`
	NamingMediaFolder  string `json:"naming_media_folder,omitempty"`
	NamingTrackFile    string `json:"naming_track_file,omitempty"`
}

func (a *API) addMCPOverviewTools(server *mcp.Server) {
	mcp.AddTool(server, &mcp.Tool{
		Name: "get_overview",
		Description: "Start here. Instance version and sizes, files needing attention (by kind), the migration " +
			"queue's length, and what the background job queue and refresh passes are doing right now.",
		Annotations: readOnlyTool("Overview", false),
	}, a.mcpGetOverview)

	mcp.AddTool(server, &mcp.Tool{
		Name: "get_configuration",
		Description: "The configured components: libraries (with their manager, data source and tagger profile), " +
			"managers, data sources and tagger profiles. Credentials are never included.",
		Annotations: readOnlyTool("Configuration", false),
	}, a.mcpGetConfiguration)
}

func (a *API) addMCPActivityTools(server *mcp.Server) {
	mcp.AddTool(server, &mcp.Tool{
		Name: "list_activity",
		Description: "The Activity feed, newest first: every run and every stage it spawned. Filter by type, status " +
			"or title; parent_id narrows to one run. Use get_activity for a row's per-file detail.",
		Annotations: readOnlyTool("List activity", false),
	}, a.mcpListActivity)

	mcp.AddTool(server, &mcp.Tool{
		Name: "get_activity",
		Description: "One Activity event with its per-file detail rows (what happened to each file, with errors) " +
			"and, for a run, the stages it performed.",
		Annotations: readOnlyTool("Get activity", false),
	}, a.mcpGetActivity)

	mcp.AddTool(server, &mcp.Tool{
		Name: "list_migrations",
		Description: "MusicBrainz merges, re-keys and deletions detected against this collection, with what is wrong " +
			"and what approving each would do. status=open is the queue waiting on a person.",
		Annotations: readOnlyTool("List migrations", false),
	}, a.mcpListMigrations)
}

func (a *API) mcpGetOverview(_ context.Context, _ *mcp.CallToolRequest, _ mcpNoInput) (*mcp.CallToolResult, mcpOverview, error) {
	count := func(model any) int64 {
		var n int64
		a.DB.Model(model).Count(&n)
		return n
	}
	out := mcpOverview{Name: a.AppName, Version: a.Version, Attention: a.attention()}
	out.Counts.Libraries = count(&models.Library{})
	out.Counts.Managers = count(&models.Manager{})
	out.Counts.DataSources = count(&models.DataSource{})
	out.Counts.TaggerProfiles = count(&models.TaggerProfile{})
	out.Counts.Files = count(&models.LibraryItem{})
	out.Counts.Artists = count(&models.CollectionArtist{})
	if pending, err := migration.PendingCount(a.DB); err == nil {
		out.PendingMigrations = pending
	}
	// Each runner may be absent (tests, a stripped-down wiring); an absent one is
	// left out rather than reported as idle.
	if a.Scan != nil {
		out.Jobs = a.Scan.Status()
	}
	if a.Mirror != nil {
		out.MetadataRefresh = a.Mirror.Status()
	}
	if a.Artwork != nil {
		out.ArtworkRefresh = a.Artwork.Status()
	}
	if a.Tempo != nil {
		out.TempoAnalysis = a.Tempo.Status()
	}
	return nil, out, nil
}

func (a *API) mcpGetConfiguration(_ context.Context, _ *mcp.CallToolRequest, _ mcpNoInput) (*mcp.CallToolResult, mcpConfiguration, error) {
	var out mcpConfiguration
	var (
		libraries []models.Library
		managers  []models.Manager
		sources   []models.DataSource
		profiles  []models.TaggerProfile
	)
	for _, q := range []struct {
		dest any
		what string
	}{{&libraries, "libraries"}, {&managers, "managers"}, {&sources, "data sources"}, {&profiles, "tagger profiles"}} {
		if err := a.DB.Order("name").Find(q.dest).Error; err != nil {
			return nil, out, fmt.Errorf("failed to list %s — the database may be busy, retry in a moment", q.what)
		}
	}

	names := map[uuid.UUID]string{}
	for _, m := range managers {
		names[m.ID] = m.Name
	}
	for _, s := range sources {
		names[s.ID] = s.Name
	}
	for _, p := range profiles {
		names[p.ID] = p.Name
	}
	nameOf := func(id *uuid.UUID) string {
		if id == nil {
			return ""
		}
		return names[*id]
	}

	out.Libraries = make([]mcpLibraryRow, 0, len(libraries))
	for _, l := range libraries {
		out.Libraries = append(out.Libraries, mcpLibraryRow{
			ID: l.ID.String(), Name: l.Name, Path: l.Path, Enabled: l.Enabled,
			Manager: nameOf(l.ManagerID), TaggerProfile: nameOf(l.TaggerProfileID),
			UseAcoustID: l.UseAcoustID, AnalyseTempo: l.AnalyseTempo, LastScan: l.LastScan,
		})
	}
	out.Managers = make([]mcpManagerRow, 0, len(managers))
	for _, m := range managers {
		out.Managers = append(out.Managers, mcpManagerRow{
			ID: m.ID.String(), Name: m.Name, Type: m.Type, Enabled: m.Enabled, Health: m.Health, BaseURL: m.LidarrBaseURL,
		})
	}
	out.DataSources = make([]mcpDataSourceRow, 0, len(sources))
	for _, s := range sources {
		out.DataSources = append(out.DataSources, mcpDataSourceRow{
			ID: s.ID.String(), Name: s.Name, Type: s.Type, Category: models.DataSourceCategory(s.Type),
			Enabled: s.Enabled, Health: s.Health, BaseURL: s.BaseURL,
		})
	}
	out.TaggerProfiles = make([]mcpTaggerProfileRow, 0, len(profiles))
	for _, p := range profiles {
		out.TaggerProfiles = append(out.TaggerProfiles, mcpTaggerProfileRow{
			ID: p.ID.String(), Name: p.Name, WriteTags: p.WriteTags, MaxGenres: p.MaxGenres, MP3MultiValueTags: p.MP3MultiValueTags, SingleRecordLabel: p.SingleRecordLabel,
			NamingArtistFolder: p.NamingArtistFolder, NamingAlbumFolder: p.NamingAlbumFolder,
			NamingMediaFolder: p.NamingMediaFolder, NamingTrackFile: p.NamingTrackFile,
		})
	}
	return nil, out, nil
}

func (a *API) mcpListActivity(_ context.Context, _ *mcp.CallToolRequest, in mcpListActivityInput) (*mcp.CallToolResult, mcpActivityList, error) {
	page, err := a.eventFeed(eventFilter{
		Type: in.Type, Status: in.Status, Search: in.TitleContains, Parent: in.ParentID,
		RunsOnly: in.RunsOnly, Limit: in.limit(), Offset: in.Offset,
	})
	if err != nil {
		if errors.Is(err, errBadParent) {
			return nil, mcpActivityList{}, errors.New("parent_id must be an event id from list_activity")
		}
		return nil, mcpActivityList{}, mcpStoreError(err)
	}
	out := mcpActivityList{
		mcpPage: newMCPPage(page.Total, page.Offset, len(page.Events)),
		Events:  make([]mcpEventRow, 0, len(page.Events)),
	}
	for _, ev := range page.Events {
		out.Events = append(out.Events, newMCPEventRow(ev))
	}
	return nil, out, nil
}

func (a *API) mcpGetActivity(_ context.Context, _ *mcp.CallToolRequest, in mcpGetActivityInput) (*mcp.CallToolResult, mcpActivityDetail, error) {
	id, err := uuid.Parse(in.ID)
	if err != nil {
		return nil, mcpActivityDetail{}, errors.New("id must be an event id from list_activity")
	}
	ev, err := a.eventDetail(id)
	if err != nil {
		return nil, mcpActivityDetail{}, fmt.Errorf("event not found: %s — list_activity returns event ids", in.ID)
	}

	limit := in.ItemLimit
	if limit <= 0 {
		limit = 100
	}
	if limit > 500 {
		limit = 500
	}
	out := mcpActivityDetail{
		mcpEventRow: newMCPEventRow(ev),
		Items:       make([]mcpEventItem, 0, min(limit, len(ev.Items))),
	}
	if len(ev.Details) > 0 {
		out.Details = ev.Details
	}
	if len(ev.Stats) > 0 {
		out.Stats = ev.Stats
	}
	for i, item := range ev.Items {
		if i == limit {
			out.ItemsTruncated = true
			break
		}
		row := mcpEventItem{
			Path: item.Path, Kind: item.Kind, Status: item.Status, Phase: item.Phase,
			TagsWritten: item.TagsWritten, Error: item.Error,
		}
		if item.Related != nil {
			row.Related = item.Related
		}
		out.Items = append(out.Items, row)
	}
	for _, child := range ev.Children {
		out.Stages = append(out.Stages, newMCPEventRow(child))
	}
	return nil, out, nil
}

func (a *API) mcpListMigrations(_ context.Context, _ *mcp.CallToolRequest, in mcpListMigrationsInput) (*mcp.CallToolResult, mcpMigrationList, error) {
	limit := in.limit()
	if limit > maxMigrationsLimit {
		limit = maxMigrationsLimit
	}
	offset := max(in.Offset, 0)
	rows, total, err := migration.List(a.DB, migration.ListOptions{
		Status: in.Status, Query: in.Query, Limit: limit, Offset: offset,
	})
	if err != nil {
		return nil, mcpMigrationList{}, mcpStoreError(errors.New("failed to list migrations"))
	}
	pending, _ := migration.PendingCount(a.DB)

	reviews := migration.Reviews(a.DB, rows)
	out := mcpMigrationList{
		mcpPage:    newMCPPage(total, offset, len(reviews)),
		Pending:    pending,
		Migrations: make([]mcpMigrationRow, 0, len(reviews)),
	}
	for _, r := range reviews {
		out.Migrations = append(out.Migrations, mcpMigrationRow{
			ID: r.ID.String(), EntityType: r.EntityType, Kind: r.Kind, Status: r.Status, Name: r.Name,
			OldMBID: r.OldMBID, NewMBID: r.NewMBID, ArtistMBID: r.ArtistMBID, ArtistName: r.ArtistName,
			AffectedFiles: r.AffectedFiles, AffectedDesires: r.AffectedDesires, TouchesPinned: r.TouchesPinned,
			Problem: r.Problem, Effect: r.Effect, Blocker: r.Blocker,
			DetectedAt: r.DetectedAt, ResolvedAt: r.ResolvedAt, Error: r.MusicbrainzMigration.Error,
		})
	}
	return nil, out, nil
}

// mcpStoreError words a query method's failure for an assistant. The query methods'
// errors are already safe to show; the suffix is what stops an agent reading a busy
// database as "there is no such data".
func mcpStoreError(err error) error {
	return fmt.Errorf("%s — the database may be busy, retry in a moment", err.Error())
}
