package routers

import (
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
// Phase 1 only reads. TestMCPToolsAreReadOnly holds that line.

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
// hard part of reading this data correctly, so it is stated once here rather than in
// every tool description.
const mcpInstructions = `Autotaggerr enriches a music library with MusicBrainz metadata and tracks what is owned versus wanted. This server is read-only.

Start with get_overview: it says whether anything needs attention and what is running.

Identifiers are MusicBrainz IDs (MBIDs) for artists, albums (release-groups), editions (releases) and tracks; files and activity events have their own IDs. Every row carries the ID the next tool takes.

Vocabulary:
- owned: at least one file of the album is on disk. complete: an owned edition has every track.
- discrepancy: the disk view and the manager's (Lidarr's) catalog view disagree about the album. Worth a look, not an error.
- wanted / wanted_source: explicit = the user picked it and can unpick it; auto = it follows from following the artist (or a want narrowed to the edition whose files landed); manager = Lidarr monitors it.
- file status is what the last attempt did: ok, unmatched (no identity found), error. error_transient = it will retry by itself (an outage), not something a person must fix.
- pin: an identity set by hand. pin_held = the file's audio changed since, so the pin waits for a person.
- The four verbs are separate and do not cascade: Process (walk files, identify, tag), Scan (re-derive the collection from the file index), Refresh metadata (re-read MusicBrainz), Tag files (rewrite tags).

Tools marked open-world may fetch from MusicBrainz, which is rate-limited to about one request per second and shared with background jobs: call them for what you need, not in bulk.`

// mcpHandler builds the endpoint: two servers, one per role, chosen per request.
//
// A tool a user may not call is then not listed for them at all, rather than listed
// and refused. In phase 1 every tool is open to any authenticated user, so the two
// are the same set; the split exists so a later admin-only tool is one registration,
// not a rework.
//
// Stateless with JSON responses: read tools need no server-to-client requests, so a
// session would hold nothing, and stateless survives restarts and buffering proxies.
func (a *API) mcpHandler() http.Handler {
	member := a.newMCPServer(false)
	admin := a.newMCPServer(true)
	return mcp.NewStreamableHTTPHandler(func(r *http.Request) *mcp.Server {
		if user, ok := auth.UserFromContext(r.Context()); ok && user.Role == models.UserRoleAdmin {
			return admin
		}
		return member
	}, &mcp.StreamableHTTPOptions{
		Stateless:    true,
		JSONResponse: true,
		// The spec asks servers to refuse DNS-rebinding browsers. The app's CORS
		// policy allows every origin, which suits a header-authenticated JSON API but
		// is not a decision to inherit here. MCP clients send no Origin and pass.
		CrossOriginProtection: http.NewCrossOriginProtection(),
	})
}

// newMCPServer registers the tools a role may see. admin is unused until a tool
// needs it; see mcpHandler.
func (a *API) newMCPServer(admin bool) *mcp.Server {
	server := mcp.NewServer(&mcp.Implementation{
		Name:    "autotaggerr",
		Title:   a.AppName,
		Version: a.Version,
	}, &mcp.ServerOptions{Instructions: mcpInstructions})

	a.addMCPOverviewTools(server)
	a.addMCPCollectionTools(server)
	a.addMCPFileTools(server)
	a.addMCPActivityTools(server)
	return server
}

// registerMCP mounts the endpoint on the v1 group. It sits outside the protected
// group because it needs its own middleware options: an API key is accepted as a
// Bearer token here (auth.BearerAPIKey), which many MCP clients can send when they
// cannot send X-Api-Key. The switch is checked before authentication and on every
// request, so turning it off in Settings applies at once and reveals nothing.
func (a *API) registerMCP(rg *gin.RouterGroup) {
	handler := gin.WrapH(a.mcpHandler())
	rg.Any(mcpPath, mcpEnabled, auth.Middleware(a.DB, a.SigningKey, auth.BearerAPIKey()), handler)
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
