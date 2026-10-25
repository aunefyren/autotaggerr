package routers

import (
	"context"
	"fmt"
	"net/http"

	"github.com/aunefyren/autotaggerr/auth"
	"github.com/aunefyren/autotaggerr/files"
	"github.com/aunefyren/autotaggerr/models"
	"github.com/gin-gonic/gin"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// The MCP server: the same API, spoken as the Model Context Protocol so AI assistants
// can read the library (see docs/mcp.md). Tools call the query methods the REST
// handlers call — never the handlers themselves — and return their own compact
// shapes, because an assistant pays for every byte of a UI payload in context.
//
// A read-only credential is shown only the tools that read; TestMCPScopeSplit holds
// that line.

// mcpPath is where the endpoint is mounted, under /api/v1.
const mcpPath = "/mcp"

const (
	// mcpDefaultLimit is a list tool's page size when the caller names none. Smaller
	// than the REST defaults: an assistant reads every row it is given.
	mcpDefaultLimit = 50
	// mcpMaxArtists caps a list_artists / list_missing page. Those page in Go over
	// rows already in memory, so the cap is about context, not cost.
	mcpMaxArtists = 200
)

// mcpInstructions is the glossary every session starts with. The vocabulary is the
// hard part of using this data correctly, so it is stated once here rather than in
// every tool description. write adds how the actions behave.
func mcpInstructions(write bool) string {
	if !write {
		return fmt.Sprintf(mcpGlossary, "This connection is read-only.", "")
	}
	return fmt.Sprintf(mcpGlossary, "This connection can also act: run the four verbs, follow artists, want albums, identify files and decide migrations.", mcpActionNotes)
}

const mcpGlossary = `Autotaggerr enriches a music library with MusicBrainz metadata and tracks what is owned versus wanted. %s

Start with get_overview: it says whether anything needs attention and what is running.

Identifiers are MusicBrainz IDs (MBIDs) for artists, albums (release-groups), editions (releases) and tracks; files and activity events have their own IDs. Every row carries the ID the next tool takes.

Vocabulary:
- owned: at least one file of the album is on disk. complete: an owned edition has every track.
- discrepancy: the disk view and the manager's (Lidarr's) catalog view disagree about the album. Worth a look, not an error.
- wanted / wanted_source: explicit = the user picked it and can unpick it; auto = it follows from following the artist (or a want narrowed to the edition whose files landed); manager = Lidarr monitors it.
- file status is what the last attempt did: ok, unmatched (no identity found), error. error_transient = it will retry by itself (an outage), not something a person must fix.
- pin: an identity set by hand. pin_held = the file's audio changed since, so the pin waits for a person.
- The four verbs are separate and do not cascade: Process (walk files, identify, tag), Scan (re-derive the collection from the file index), Refresh metadata (re-read MusicBrainz), Tag files (rewrite tags).

Tools marked open-world may fetch from MusicBrainz, which is rate-limited to about one request per second and shared with background jobs: call them for what you need, not in bulk.%s`

const mcpActionNotes = `

Acting:
- process, refresh_metadata and tag_files are queued, not run: they return at once, and the work shows in get_overview (jobs) and list_activity. Duplicates of a queued job collapse into it. scan without walk_disk runs inline and returns its result.
- Only tag_files, process, attach_file and confirm_pin write to audio files. Everything else changes Autotaggerr's own records.
- An artist or library managed by Lidarr has its identity and wants decided in Lidarr: wanting an album or attaching a file there is refused, by design.
- Search MusicBrainz (search_artists, search_releases, browse_artist_albums) to find the IDs an action needs; each search is a rate-limited request.`

// mcpServerKey is what decides which tools a caller is shown: their role, and
// whether their credential may write.
type mcpServerKey struct{ admin, write bool }

// mcpHandler builds the endpoint: one server per role × scope, built once and chosen
// per request.
//
// A tool a caller may not use is then not listed for them at all, rather than listed
// and refused — a read-only key is never shown a tool that writes. This is the MCP
// endpoint's scope enforcement: it is all POSTs, so the REST rule (a read key may
// only GET) cannot apply here, and auth.ScopeCheckedByEndpoint lets it through. Today
// role changes nothing (no tool is admin-only); the split is kept so the first one is
// one registration, not a rework.
//
// Stateless with JSON responses: no tool makes a server-to-client request, so a
// session would hold nothing, and stateless survives restarts and buffering proxies.
func (a *API) mcpHandler() http.Handler {
	servers := map[mcpServerKey]*mcp.Server{}
	for _, admin := range []bool{false, true} {
		for _, write := range []bool{false, true} {
			key := mcpServerKey{admin: admin, write: write}
			servers[key] = a.newMCPServer(key)
		}
	}
	return mcp.NewStreamableHTTPHandler(func(r *http.Request) *mcp.Server {
		return servers[mcpServerKeyFor(r.Context())]
	}, &mcp.StreamableHTTPOptions{
		Stateless:    true,
		JSONResponse: true,
		// The spec asks servers to refuse DNS-rebinding browsers. The app's CORS
		// policy allows every origin, which suits a header-authenticated JSON API but
		// is not a decision to inherit here. MCP clients send no Origin and pass.
		CrossOriginProtection: http.NewCrossOriginProtection(),
	})
}

// mcpServerKeyFor reads the caller off the request context. Anything missing reads as
// the least it could be: not admin, read-only.
func mcpServerKeyFor(ctx context.Context) mcpServerKey {
	user, _ := auth.UserFromContext(ctx)
	cred, _ := auth.CredentialFromContext(ctx)
	return mcpServerKey{admin: user.Role == models.UserRoleAdmin, write: cred.CanWrite()}
}

// newMCPServer registers the tools a role and scope may see.
func (a *API) newMCPServer(key mcpServerKey) *mcp.Server {
	server := mcp.NewServer(&mcp.Implementation{
		Name:    "autotaggerr",
		Title:   a.AppName,
		Version: a.Version,
	}, &mcp.ServerOptions{Instructions: mcpInstructions(key.write)})

	a.addMCPOverviewTools(server)
	a.addMCPCollectionTools(server)
	a.addMCPFileTools(server)
	a.addMCPActivityTools(server)
	if key.write {
		a.addMCPSearchTools(server)
		a.addMCPVerbTools(server)
		a.addMCPEditTools(server)
	}
	return server
}

// registerMCP mounts the endpoint on the v1 group. It sits outside the protected
// group because it needs its own middleware options: an API key is accepted as a
// Bearer token here (auth.BearerAPIKey), which many MCP clients can send when they
// cannot send X-Api-Key, and a read key's scope is enforced by the tools it is shown
// rather than by HTTP method (auth.ScopeCheckedByEndpoint). The switch is checked before authentication and on every
// request, so turning it off in Settings applies at once and reveals nothing.
func (a *API) registerMCP(rg *gin.RouterGroup) {
	handler := gin.WrapH(a.mcpHandler())
	rg.Any(mcpPath, mcpEnabled, auth.Middleware(a.DB, a.SigningKey, auth.BearerAPIKey(), auth.ScopeCheckedByEndpoint()), handler)
}

// mcpEnabled answers 404 while autotaggerr_mcp_disabled is set — the endpoint is
// simply not there.
func mcpEnabled(c *gin.Context) {
	if files.ConfigFile.AutotaggerrMCPDisabled {
		c.AbortWithStatusJSON(http.StatusNotFound, gin.H{"error": "the MCP endpoint is turned off in Settings"})
		return
	}
	c.Next()
}

// readOnlyTool is the annotation every phase 1 tool carries. openWorld marks a tool
// that may fetch from MusicBrainz rather than answer from the database alone.
func readOnlyTool(title string, openWorld bool) *mcp.ToolAnnotations {
	return &mcp.ToolAnnotations{Title: title, ReadOnlyHint: true, OpenWorldHint: &openWorld}
}

// mcpPage is the paging block every list tool returns. NextOffset is absent on the
// last page, so "is there more" never needs arithmetic.
type mcpPage struct {
	Total      int64 `json:"total" jsonschema:"rows matching the filters, across all pages"`
	Offset     int   `json:"offset" jsonschema:"offset of the first row returned"`
	NextOffset *int  `json:"next_offset,omitempty" jsonschema:"pass as offset to get the next page; absent on the last page"`
}

func newMCPPage(total int64, offset, returned int) mcpPage {
	page := mcpPage{Total: total, Offset: offset}
	if next := offset + returned; returned > 0 && int64(next) < total {
		page.NextOffset = &next
	}
	return page
}

// mcpPaging is the input block every list tool takes.
type mcpPaging struct {
	Limit  int `json:"limit,omitempty" jsonschema:"rows per page; default 50"`
	Offset int `json:"offset,omitempty" jsonschema:"rows to skip; use next_offset from the previous page"`
}

// window clamps the paging to [0, max] and slices n rows to it, for lists paged in Go.
func (p mcpPaging) window(n, max int) (lo, hi int) {
	limit := p.Limit
	if limit <= 0 {
		limit = mcpDefaultLimit
	}
	if limit > max {
		limit = max
	}
	lo = p.Offset
	if lo < 0 {
		lo = 0
	}
	if lo > n {
		lo = n
	}
	hi = lo + limit
	if hi > n {
		hi = n
	}
	return lo, hi
}

// limit is the page size to hand a query method that clamps it itself.
func (p mcpPaging) limit() int {
	if p.Limit <= 0 {
		return mcpDefaultLimit
	}
	return p.Limit
}
