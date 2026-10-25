package routers

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/aunefyren/autotaggerr/models"
	"github.com/google/uuid"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// MCP tools for the file index: what each file is identified as, what the last
// attempt at it did, and what a tag write would change.

type mcpFileRow struct {
	ID                string     `json:"id" jsonschema:"file id; pass to get_file"`
	Path              string     `json:"path"`
	LibraryID         string     `json:"library_id"`
	Status            string     `json:"status" jsonschema:"what the last attempt did: ok, unmatched or error"`
	Error             string     `json:"error,omitempty"`
	ErrorTransient    bool       `json:"error_transient,omitempty" jsonschema:"the failure will clear by itself (an outage), not something a person must fix"`
	LastErrorAt       *time.Time `json:"last_error_at,omitempty"`
	ReleaseMBID       string     `json:"release_mbid,omitempty"`
	RecordingMBID     string     `json:"recording_mbid,omitempty"`
	TrackMBID         string     `json:"track_mbid,omitempty"`
	CorrelationSource string     `json:"correlation_source,omitempty" jsonschema:"how the identity was found, e.g. lidarr, tags, manual, acoustid"`
	Pinned            bool       `json:"pinned,omitempty" jsonschema:"identity set by hand; automatic resolution never overrides it"`
	PinHeld           bool       `json:"pin_held,omitempty" jsonschema:"the audio changed since the pin was made; the file waits for a person"`
	LastTaggedAt      *time.Time `json:"last_tagged_at,omitempty"`
}

func newMCPFileRow(item models.LibraryItem) mcpFileRow {
	return mcpFileRow{
		ID: item.ID.String(), Path: item.Path, LibraryID: item.LibraryID.String(),
		Status: item.Status, Error: item.Error, ErrorTransient: item.LastErrorTransient, LastErrorAt: item.LastErrorAt,
		ReleaseMBID: item.MBReleaseID, RecordingMBID: item.MBRecordingID, TrackMBID: item.MBReleaseTrackID,
		CorrelationSource: item.CorrelationSource, Pinned: item.Pinned, PinHeld: item.PinHeld,
		LastTaggedAt: item.LastTaggedAt,
	}
}

type mcpListFilesInput struct {
	LibraryID    string `json:"library_id,omitempty" jsonschema:"only this library (ids from get_configuration)"`
	Status       string `json:"status,omitempty" jsonschema:"ok, unmatched or error"`
	Transient    *bool  `json:"transient,omitempty" jsonschema:"with status=error: true for failures that will retry by themselves, false for ones a person must look at"`
	PathContains string `json:"path_contains,omitempty" jsonschema:"substring of the file path"`
	MBID         string `json:"mbid,omitempty" jsonschema:"files under this artist, album (release-group) or edition (release) MBID"`
	PinHeld      bool   `json:"pin_held,omitempty" jsonschema:"only files whose pin is held for review"`
	mcpPaging
}

type mcpFileList struct {
	mcpPage
	Files []mcpFileRow `json:"files"`
}

type mcpGetFileInput struct {
	ID             string `json:"id" jsonschema:"file id from list_files or get_release_tracks"`
	IncludeTagDiff bool   `json:"include_tag_diff,omitempty" jsonschema:"also compute the file's current-vs-desired tags and path; may fetch from MusicBrainz"`
	IncludeTempo   bool   `json:"include_tempo,omitempty" jsonschema:"also report the file's analysed BPM and what its library does with it"`
}

type mcpTagDiff struct {
	Changed int                   `json:"changed" jsonschema:"tags a write would change"`
	Tags    []models.TagDiffEntry `json:"tags"`
	Path    *models.PathDiff      `json:"path,omitempty" jsonschema:"where the tagger profile's naming would put the file"`
	// PathError says why no path could be rendered; the tags are still valid.
	PathError string `json:"path_error,omitempty"`
}

type mcpFileDetail struct {
	mcpFileRow
	Library             string        `json:"library,omitempty"`
	Size                int64         `json:"size"`
	ModTime             *time.Time    `json:"mod_time,omitempty"`
	CorrelatedByManager string        `json:"correlated_by_manager,omitempty"`
	CorrelatedAt        *time.Time    `json:"correlated_at,omitempty"`
	LastScannedAt       *time.Time    `json:"last_scanned_at,omitempty"`
	ProcessedVersion    string        `json:"processed_version,omitempty"`
	TagDiff             *mcpTagDiff   `json:"tag_diff,omitempty"`
	TagDiffError        string        `json:"tag_diff_error,omitempty" jsonschema:"why no tag diff exists, e.g. the file is not identified yet"`
	Tempo               *itemTempoMCP `json:"tempo,omitempty"`
}

// itemTempoMCP is itemTempoView with the stored analysis reduced to what it says.
type itemTempoMCP struct {
	BPM             float64 `json:"bpm,omitempty"`
	Confidence      float64 `json:"confidence,omitempty"`
	Analysed        bool    `json:"analysed" jsonschema:"a tempo has been measured for this file"`
	LibraryAnalyses bool    `json:"library_analyses" jsonschema:"the file's library is opted in to tempo analysis"`
	WritesTags      bool    `json:"writes_tags"`
	BPMWrite        string  `json:"bpm_write" jsonschema:"what the library does with a result"`
	MinConfidence   float64 `json:"min_confidence"`
}

func (a *API) addMCPFileTools(server *mcp.Server) {
	mcp.AddTool(server, &mcp.Tool{
		Name: "list_files",
		Description: "Indexed audio files, by path, with what each is identified as and what the last attempt at it " +
			"did. Filter by library, status (error, unmatched), transient, path, an MBID, or held pins.",
		Annotations: readOnlyTool("List files", false),
	}, a.mcpListFiles)

	mcp.AddTool(server, &mcp.Tool{
		Name: "get_file",
		Description: "One indexed file in full. include_tag_diff adds what a tag write would change (may fetch " +
			"from MusicBrainz); include_tempo adds its analysed BPM.",
		Annotations: readOnlyTool("Get file", true),
	}, a.mcpGetFile)
}

func (a *API) mcpListFiles(_ context.Context, _ *mcp.CallToolRequest, in mcpListFilesInput) (*mcp.CallToolResult, mcpFileList, error) {
	filter := itemFilter{
		Status: strings.TrimSpace(in.Status), Transient: in.Transient, Search: in.PathContains,
		MBID: in.MBID, PinHeld: in.PinHeld, Limit: in.limit(), Offset: in.Offset,
	}
	if raw := strings.TrimSpace(in.LibraryID); raw != "" {
		id, err := uuid.Parse(raw)
		if err != nil {
			return nil, mcpFileList{}, errors.New("library_id must be a library id from get_configuration")
		}
		filter.LibraryID = &id
	}
	switch filter.Status {
	case "", models.LibraryItemStatusOK, models.LibraryItemStatusUnmatched, models.LibraryItemStatusError:
	default:
		return nil, mcpFileList{}, fmt.Errorf("unknown status %q — use ok, unmatched or error", filter.Status)
	}

	page, err := a.libraryItems(filter)
	if err != nil {
		return nil, mcpFileList{}, mcpStoreError(err)
	}
	out := mcpFileList{
		mcpPage: newMCPPage(page.Total, page.Offset, len(page.Items)),
		Files:   make([]mcpFileRow, 0, len(page.Items)),
	}
	for _, view := range page.Items {
		out.Files = append(out.Files, newMCPFileRow(view.LibraryItem))
	}
	return nil, out, nil
}

func (a *API) mcpGetFile(_ context.Context, _ *mcp.CallToolRequest, in mcpGetFileInput) (*mcp.CallToolResult, mcpFileDetail, error) {
	id, err := uuid.Parse(strings.TrimSpace(in.ID))
	if err != nil {
		return nil, mcpFileDetail{}, errors.New("id must be a file id from list_files")
	}
	var item models.LibraryItem
	if err := a.DB.First(&item, "id = ?", id).Error; err != nil {
		return nil, mcpFileDetail{}, fmt.Errorf("file not found: %s — list_files returns file ids", in.ID)
	}

	out := mcpFileDetail{
		mcpFileRow: newMCPFileRow(item),
		Size:       item.Size, ModTime: item.ModTime,
		CorrelatedByManager: item.CorrelatedByManager, CorrelatedAt: item.CorrelatedAt,
		LastScannedAt: item.LastScannedAt, ProcessedVersion: item.ProcessedVersion,
	}
	var library models.Library
	if a.DB.Select("name").Where("id = ?", item.LibraryID).Limit(1).Find(&library).RowsAffected > 0 {
		out.Library = library.Name
	}

	if in.IncludeTagDiff {
		// A file with no diff is still a file worth describing, so the reason goes
		// beside the rest rather than failing the call.
		if diff, err := a.itemTagDiff(item); err != nil {
			out.TagDiffError = err.Error()
		} else {
			td := &mcpTagDiff{Tags: diff.Tags, Path: diff.Path, PathError: diff.PathError}
			for _, tag := range diff.Tags {
				if tag.Changed {
					td.Changed++
				}
			}
			out.TagDiff = td
		}
	}

	if in.IncludeTempo {
		info := a.itemTempoInfo(item)
		tempo := &itemTempoMCP{
			LibraryAnalyses: info.LibraryAnalyses, WritesTags: info.WritesTags,
			BPMWrite: info.BPMWrite, MinConfidence: info.MinConfidence,
		}
		if info.Tempo != nil {
			tempo.Analysed = true
			tempo.BPM, tempo.Confidence = info.Tempo.BPM, info.Tempo.Confidence
		}
		out.Tempo = tempo
	}
	return nil, out, nil
}
