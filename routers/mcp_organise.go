package routers

import (
	"context"
	"errors"
	"strings"

	"github.com/aunefyren/autotaggerr/process"
	"github.com/google/uuid"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Organise over MCP: the dry run is a read, the commit a write-scoped action. The
// commit never takes a list of moves from the assistant — it plans again from the
// scope and leaves out the item IDs it was asked to skip — so what moves is always
// what Autotaggerr itself proposed, exactly as the review table guarantees in the UI.

// mcpOrganisePreviewLimit bounds the moves listed in one preview. A library's worth can
// run to thousands; the counts say how many there are, and a narrower scope lists them.
const mcpOrganisePreviewLimit = 100

type mcpOrganiseScopeInput struct {
	Library    string `json:"library,omitempty" jsonschema:"organise one library (name or ID)"`
	ArtistMBID string `json:"artist_mbid,omitempty" jsonschema:"organise one artist's files"`
	AlbumMBID  string `json:"album_mbid,omitempty" jsonschema:"organise one album's (release-group's) files"`
}

func (a *API) mcpOrganiseScope(in mcpOrganiseScopeInput) (process.OrganiseScope, error) {
	scope := process.OrganiseScope{ArtistMBID: strings.TrimSpace(in.ArtistMBID), ReleaseGroupMBID: strings.TrimSpace(in.AlbumMBID)}
	if strings.TrimSpace(in.Library) != "" {
		id, err := a.mcpLibrary(in.Library)
		if err != nil {
			return scope, err
		}
		scope.LibraryID = *id
	}
	return scope, nil
}

type mcpOrganiseMove struct {
	ItemID   string `json:"item_id"`
	From     string `json:"from" jsonschema:"path below the library root"`
	To       string `json:"to"`
	Conflict string `json:"conflict,omitempty" jsonschema:"clash (two files would get this name) or exists (something else is there); a conflict blocks organise_files until the row is skipped"`
}

type mcpOrganisePreview struct {
	Moves         []mcpOrganiseMove `json:"moves"`
	MovesTotal    int               `json:"moves_total"`
	Truncated     bool              `json:"truncated,omitempty" jsonschema:"more moves than listed; narrow the scope to see them all"`
	Conflicts     int               `json:"conflicts"`
	InPlace       int               `json:"in_place" jsonschema:"files already where their naming templates put them"`
	CannotMove    int               `json:"cannot_move" jsonschema:"files left alone: no identity, an unreadable release, or a Lidarr library"`
	CannotMoveWhy []string          `json:"cannot_move_why,omitempty" jsonschema:"the distinct reasons"`
	AlbumFiles    int               `json:"album_files" jsonschema:"covers, cue sheets and logs that would move with albums moving whole"`
}

func (a *API) mcpPreviewOrganise(_ context.Context, _ *mcp.CallToolRequest, in mcpOrganiseScopeInput) (*mcp.CallToolResult, mcpOrganisePreview, error) {
	if a.Scan == nil {
		return nil, mcpOrganisePreview{}, errProcessorUnavailable
	}
	scope, err := a.mcpOrganiseScope(in)
	if err != nil {
		return nil, mcpOrganisePreview{}, err
	}
	plan, err := a.Scan.PlanOrganise(scope)
	if err != nil {
		return nil, mcpOrganisePreview{}, err
	}
	out := mcpOrganisePreview{Moves: []mcpOrganiseMove{}, MovesTotal: len(plan.Moves), InPlace: plan.InPlace,
		CannotMove: len(plan.Skipped), AlbumFiles: len(plan.Extras)}
	for _, m := range plan.Moves {
		if m.Conflict != "" {
			out.Conflicts++
		}
		if len(out.Moves) < mcpOrganisePreviewLimit {
			out.Moves = append(out.Moves, mcpOrganiseMove{ItemID: m.ItemID.String(), From: m.FromRel, To: m.ToRel, Conflict: m.Conflict})
		}
	}
	out.Truncated = len(plan.Moves) > len(out.Moves)
	seen := map[string]bool{}
	for _, s := range plan.Skipped {
		if !seen[s.Reason] {
			seen[s.Reason] = true
			out.CannotMoveWhy = append(out.CannotMoveWhy, s.Reason)
		}
	}
	return nil, out, nil
}

type mcpOrganiseInput struct {
	mcpOrganiseScopeInput
	Skip       []string `json:"skip,omitempty" jsonschema:"item IDs from preview_organise to leave where they are — required for every conflicting row"`
	MoveExtras *bool    `json:"move_album_files,omitempty" jsonschema:"move covers, cue sheets and logs with albums moving whole; default true"`
}

type mcpOrganiseResult struct {
	Queued bool   `json:"queued"`
	Moves  int    `json:"moves"`
	Follow string `json:"follow"`
}

func (a *API) mcpOrganise(_ context.Context, _ *mcp.CallToolRequest, in mcpOrganiseInput) (*mcp.CallToolResult, mcpOrganiseResult, error) {
	if a.Scan == nil {
		return nil, mcpOrganiseResult{}, errProcessorUnavailable
	}
	scope, err := a.mcpOrganiseScope(in.mcpOrganiseScopeInput)
	if err != nil {
		return nil, mcpOrganiseResult{}, err
	}
	plan, err := a.Scan.PlanOrganise(scope)
	if err != nil {
		return nil, mcpOrganiseResult{}, err
	}
	skip := map[uuid.UUID]bool{}
	for _, raw := range in.Skip {
		id, err := uuid.Parse(strings.TrimSpace(raw))
		if err != nil {
			return nil, mcpOrganiseResult{}, errors.New("skip holds item IDs from preview_organise; " + raw + " is not one")
		}
		skip[id] = true
	}
	moves := 0
	for i := range plan.Moves {
		plan.Moves[i].Skip = skip[plan.Moves[i].ItemID]
		if !plan.Moves[i].Skip {
			moves++
		}
	}
	extras := in.MoveExtras == nil || *in.MoveExtras
	if err := a.Scan.Organise(scope, plan.Moves, extras); err != nil {
		return nil, mcpOrganiseResult{}, err
	}
	return nil, mcpOrganiseResult{Queued: true, Moves: moves, Follow: mcpFollowJob}, nil
}

// addMCPOrganiseTools registers the dry run for every key, and the commit for write keys.
func (a *API) addMCPOrganiseTools(server *mcp.Server, write bool) {
	mcp.AddTool(server, &mcp.Tool{
		Name: "preview_organise",
		Description: "Dry run of moving files to where their library's naming templates put them, for one library, " +
			"artist or album: every proposed move, conflicts, files already in place and files that cannot be moved. " +
			"Moves nothing. A Lidarr library is refused — Lidarr names its files. May read MusicBrainz for an uncached release.",
		Annotations: readOnlyTool("Preview organise", true),
	}, a.mcpPreviewOrganise)
	if !write {
		return
	}
	mcp.AddTool(server, &mcp.Tool{
		Name: "organise_files",
		Description: "Queue the moves preview_organise proposed for the same scope, leaving out the item IDs in skip. " +
			"Plans again and moves only what it proposes itself; refused while any unskipped move has a conflict. " +
			"Moves audio files (and, by default, each whole album's covers and cue sheets) within the library; deletes nothing. " +
			"Show the person the preview first.",
		Annotations: actionTool("Organise files", true, false, true),
	}, a.mcpOrganise)
}
