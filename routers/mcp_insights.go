package routers

import (
	"context"
	"errors"
	"strings"

	"github.com/aunefyren/autotaggerr/insights"
	"github.com/google/uuid"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// The Insights page's reads, for assistants: the same insights functions the REST
// handlers call (docs/insights.md), so the page and a "library health" answer cannot
// disagree.

func (a *API) addMCPInsightTools(server *mcp.Server) {
	mcp.AddTool(server, &mcp.Tool{
		Name: "get_library_stats",
		Description: "What the library is made of: file, recording, album and artist counts, size and play time; " +
			"breakdowns by format, release type, decade, genre, country and label; tag coverage (ISRC, cover, " +
			"ReplayGain, BPM, identified) and files unmatched, failing or pinned; and, collection-wide, albums " +
			"wanted with no file, incomplete, or in no catalogue. Reads the index and release cache only.",
		Annotations: readOnlyTool("Library stats", false),
	}, a.mcpGetLibraryStats)

	mcp.AddTool(server, &mcp.Tool{
		Name: "list_duplicates",
		Description: "Files that are the same MusicBrainz recording, grouped and classified: identical (same audio " +
			"twice in one album — one copy can go), mixed_format (a lossy copy beside the lossless one), same_format " +
			"(a re-rip or another master; audio_unchecked means it may still be identical), single_on_album (a single " +
			"whose track is also held on its album — the single can go), across_releases (another album, or another " +
			"version's edition such as a remix or demo edition — usually intended, hidden unless asked for). Copies of " +
			"different versions are never compared. counts includes hidden groups.",
		Annotations: readOnlyTool("List duplicates", false),
	}, a.mcpListDuplicates)
}

type mcpLibraryInput struct {
	LibraryID string `json:"library_id,omitempty" jsonschema:"only this library (ids from get_configuration); default every enabled library"`
}

// mcpLibraryID parses an optional library id, naming where valid ones come from.
func mcpLibraryID(raw string) (*uuid.UUID, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	id, err := uuid.Parse(raw)
	if err != nil {
		return nil, errors.New("library_id must be a library id from get_configuration")
	}
	return &id, nil
}

func (a *API) mcpGetLibraryStats(_ context.Context, _ *mcp.CallToolRequest, in mcpLibraryInput) (*mcp.CallToolResult, insights.Stats, error) {
	library, err := mcpLibraryID(in.LibraryID)
	if err != nil {
		return nil, insights.Stats{}, err
	}
	stats, err := insights.LibraryStats(a.DB, library)
	return nil, stats, err
}

type mcpListDuplicatesInput struct {
	mcpLibraryInput
	Level           string `json:"level,omitempty" jsonschema:"only this level: identical, mixed_format, same_format, single_on_album or across_releases"`
	AcrossLibraries bool   `json:"across_libraries,omitempty" jsonschema:"compare files in different libraries (off: a lossy mirror library is not a duplicate)"`
	AcrossReleases  bool   `json:"across_releases,omitempty" jsonschema:"include the same recording on other albums and versions"`
	Compilations    bool   `json:"compilations,omitempty" jsonschema:"with across_releases, include compilations"`
	Limit           int    `json:"limit,omitempty" jsonschema:"groups to return; default 50, at most 200"`
}

// mcpDuplicates is the report with Matched counting the groups at the asked level,
// before the tool's limit.
type mcpDuplicates struct {
	insights.DuplicatesReport
}

func (a *API) mcpListDuplicates(_ context.Context, _ *mcp.CallToolRequest, in mcpListDuplicatesInput) (*mcp.CallToolResult, mcpDuplicates, error) {
	library, err := mcpLibraryID(in.LibraryID)
	if err != nil {
		return nil, mcpDuplicates{}, err
	}
	level := insights.Level(strings.TrimSpace(in.Level))
	if level != "" {
		known := false
		for _, l := range insights.Levels {
			known = known || l == level
		}
		if !known {
			return nil, mcpDuplicates{}, errors.New("level must be identical, mixed_format, same_format, single_on_album or across_releases")
		}
	}
	report, err := insights.Duplicates(a.DB, insights.DuplicateOptions{
		Library:               library,
		AcrossLibraries:       in.AcrossLibraries,
		IncludeAcrossReleases: in.AcrossReleases || level == insights.LevelAcrossReleases,
		IncludeCompilations:   in.Compilations,
	})
	if err != nil {
		return nil, mcpDuplicates{}, err
	}
	if level != "" {
		kept := report.Groups[:0]
		for _, g := range report.Groups {
			if g.Level == level {
				kept = append(kept, g)
			}
		}
		report.Groups = kept
	}
	limit := in.Limit
	if limit <= 0 {
		limit = 50
	}
	if limit > 200 {
		limit = 200
	}
	out := mcpDuplicates{DuplicatesReport: report}
	out.Matched = len(report.Groups)
	if len(out.Groups) > limit {
		out.Groups = out.Groups[:limit]
	}
	return nil, out, nil
}
