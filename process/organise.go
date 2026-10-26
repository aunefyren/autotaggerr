package process

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/aunefyren/autotaggerr/collection"
	"github.com/aunefyren/autotaggerr/components"
	"github.com/aunefyren/autotaggerr/events"
	"github.com/aunefyren/autotaggerr/logger"
	"github.com/aunefyren/autotaggerr/models"
	"github.com/aunefyren/autotaggerr/modules"
	"github.com/google/uuid"
	"golang.org/x/text/unicode/norm"
	"gorm.io/gorm"
)

// Organise: moving files to where their library's naming templates put them. See
// docs/naming.md#organise.
//
// Two steps, always: a plan that moves nothing, reviewed as a table, then a job that
// moves exactly what was reviewed. The plan renders each identified file through
// modules.RenderLibraryPath — the renderer the import and the item's PATH preview use,
// so the three cannot disagree. A Lidarr library is never organised: Lidarr tracks its
// files by path, and a file moved under it is a file Lidarr has lost.

// Why a planned move cannot be made as it stands.
const (
	// OrganiseConflictClash: two files of the plan render one path. One of them is
	// misidentified, or the templates do not tell them apart; either way, picking a
	// winner would overwrite the other.
	OrganiseConflictClash = "clash"
	// OrganiseConflictExists: something not moved by this plan already sits at the
	// target. Organise never deletes, so that file has to go first.
	OrganiseConflictExists = "exists"
)

// PhaseOrganising is the organise job's file stage.
const PhaseOrganising = "organising"

// ErrOrganiseManaged is organising a library a Lidarr manager names the files of.
var ErrOrganiseManaged = errors.New("Lidarr names this library's files, so Autotaggerr will not move them — rename them in Lidarr")

// ErrOrganiseNotReady is a reviewed plan with a conflict left in it.
var ErrOrganiseNotReady = errors.New("the plan has conflicts")

// OrganiseScope is what an organise covers: one library, one artist's files, or one
// album's. Exactly one is set.
type OrganiseScope struct {
	LibraryID        uuid.UUID `json:"library_id"`
	ArtistMBID       string    `json:"artist_mb_id"`
	ReleaseGroupMBID string    `json:"release_group_mb_id"`
}

func (s OrganiseScope) valid() bool {
	n := 0
	for _, set := range []bool{s.LibraryID != uuid.Nil, s.ArtistMBID != "", s.ReleaseGroupMBID != ""} {
		if set {
			n++
		}
	}
	return n == 1
}

// OrganiseRow is one file: a move the plan proposes, or one it cannot make (Reason).
type OrganiseRow struct {
	ItemID    uuid.UUID `json:"item_id"`
	LibraryID uuid.UUID `json:"library_id"`
	From      string    `json:"from"`
	To        string    `json:"to,omitempty"`
	// FromRel and ToRel are the same below the library root, with forward slashes.
	FromRel  string `json:"from_rel"`
	ToRel    string `json:"to_rel,omitempty"`
	Conflict string `json:"conflict,omitempty"`
	// Reason says why a file is not moved at all: no identity, a release that cannot be
	// read, a Lidarr library.
	Reason string `json:"reason,omitempty"`
	// Skip is the reviewer leaving a proposed move out.
	Skip bool `json:"skip,omitempty"`
}

// OrganisePlan is a dry run: the moves, the files that cannot be moved, and how many
// are already where they belong.
type OrganisePlan struct {
	Moves     []OrganiseRow `json:"moves"`
	Skipped   []OrganiseRow `json:"skipped"`
	InPlace   int           `json:"in_place"`
	Libraries []string      `json:"libraries"`
}

// PlanOrganise renders every identified file of the scope and lists those whose path
// would change. It reads releases cache-first, so a library processed recently costs no
// request; it moves nothing.
func (r *Runner) PlanOrganise(scope OrganiseScope) (OrganisePlan, error) {
	plan := OrganisePlan{Moves: []OrganiseRow{}, Skipped: []OrganiseRow{}, Libraries: []string{}}
	if !scope.valid() {
		return plan, errors.New("organise one library, one artist or one album")
	}
	items, err := r.organiseItems(scope)
	if err != nil {
		return plan, err
	}
	libraries, err := r.organiseLibraries(items, scope)
	if err != nil {
		return plan, err
	}
	for _, l := range libraries {
		plan.Libraries = append(plan.Libraries, l.library.Name)
	}
	sort.Strings(plan.Libraries)

	releases := map[string]models.MusicBrainzReleaseResponse{}
	for _, item := range items {
		lib, ok := libraries[item.LibraryID]
		if !ok {
			continue
		}
		row := OrganiseRow{ItemID: item.ID, LibraryID: item.LibraryID, From: item.Path, FromRel: relSlash(lib.library.Path, item.Path)}
		if lib.managed {
			row.Reason = ErrOrganiseManaged.Error()
			plan.Skipped = append(plan.Skipped, row)
			continue
		}
		release, seen := releases[item.MBReleaseID]
		if !seen {
			if release, err = r.meta.GetRelease(item.MBReleaseID); err != nil {
				row.Reason = "its release could not be read: " + err.Error()
				plan.Skipped = append(plan.Skipped, row)
				continue
			}
			releases[item.MBReleaseID] = release
		}
		path, err := modules.RenderLibraryPath(lib.naming, release, item.MBReleaseTrackID, filepath.Ext(item.Path))
		if err != nil {
			row.Reason = "its path could not be rendered: " + err.Error()
			plan.Skipped = append(plan.Skipped, row)
			continue
		}
		row.To = filepath.Join(lib.library.Path, path.Rel())
		row.ToRel = relSlash(lib.library.Path, row.To)
		if norm.NFC.String(row.To) == norm.NFC.String(row.From) {
			plan.InPlace++
			continue
		}
		plan.Moves = append(plan.Moves, row)
	}
	markOrganiseConflicts(plan.Moves)
	sort.Slice(plan.Moves, func(i, j int) bool { return plan.Moves[i].From < plan.Moves[j].From })
	sort.Slice(plan.Skipped, func(i, j int) bool { return plan.Skipped[i].From < plan.Skipped[j].From })
	return plan, nil
}

// markOrganiseConflicts flags the moves that cannot be made: two rendering one target,
// and a target something outside the plan already occupies. A target another move of
// the plan is leaving is free — the job moves in two phases, so a swap or a chain
// within the plan cannot collide. Compared without case: on a case-insensitive
// filesystem two such paths are one file. A move whose target differs from its own
// source only by case is a rename of itself.
func markOrganiseConflicts(moves []OrganiseRow) {
	targets := map[string]int{}
	leaving := map[string]bool{}
	for _, m := range moves {
		if m.Skip {
			continue
		}
		targets[strings.ToLower(m.To)]++
		leaving[strings.ToLower(m.From)] = true
	}
	for i := range moves {
		m := &moves[i]
		m.Conflict = ""
		if m.Skip {
			continue
		}
		key := strings.ToLower(m.To)
		switch {
		case targets[key] > 1:
			m.Conflict = OrganiseConflictClash
		case leaving[key]:
			// Freed by this plan, possibly by this very file (a change of case).
		default:
			if _, err := os.Lstat(m.To); err == nil {
				m.Conflict = OrganiseConflictExists
			}
		}
	}
}

// CheckOrganisePlan reports the first reviewed move that blocks the commit.
func CheckOrganisePlan(rows []OrganiseRow) error {
	marked := append([]OrganiseRow(nil), rows...)
	markOrganiseConflicts(marked)
	moves := 0
	for _, row := range marked {
		if row.Skip {
			continue
		}
		moves++
		switch row.Conflict {
		case OrganiseConflictClash:
			return fmt.Errorf("%w: %s and another file would both be named %s — skip one", ErrOrganiseNotReady, row.FromRel, row.ToRel)
		case OrganiseConflictExists:
			return fmt.Errorf("%w: %s already exists — move or remove it first, or skip %s", ErrOrganiseNotReady, row.ToRel, row.FromRel)
		}
	}
	if moves == 0 {
		return fmt.Errorf("%w: every move is skipped", ErrOrganiseNotReady)
	}
	return nil
}

// Organise queues the reviewed moves. The job plans again when it runs and makes only
// the moves that are still exactly what was reviewed.
func (r *Runner) Organise(scope OrganiseScope, reviewed []OrganiseRow) error {
	if !scope.valid() {
		return errors.New("organise one library, one artist or one album")
	}
	if err := CheckOrganisePlan(reviewed); err != nil {
		return err
	}
	title := "Organise files"
	r.enqueue(job{jobOrganise, "organise:" + uuid.NewString(), title, func() { r.organiseNow(scope, reviewed) }})
	return nil
}

func (r *Runner) organiseNow(scope OrganiseScope, reviewed []OrganiseRow) {
	event := events.Begin(r.db, models.EventTypeOrganise, "Organise files")
	approved := map[uuid.UUID]OrganiseRow{}
	for _, row := range reviewed {
		if !row.Skip {
			approved[row.ItemID] = row
		}
	}

	plan, err := r.PlanOrganise(scope)
	if err != nil {
		events.Finish(r.db, event, models.EventStatusError, err.Error(), map[string]any{"error": err.Error()})
		return
	}
	var moves []OrganiseRow
	var items []models.EventItem
	failed := 0
	fail := func(row OrganiseRow, msg string) {
		failed++
		items = append(items, models.EventItem{Path: row.From, From: row.From, Status: models.EventItemStatusError, Error: msg})
	}
	planned := map[uuid.UUID]bool{}
	for _, row := range plan.Moves {
		want, ok := approved[row.ItemID]
		if !ok {
			continue
		}
		planned[row.ItemID] = true
		switch {
		case want.From != row.From || want.To != row.To:
			fail(row, "its move changed since the review — organise again to review it")
		case row.Conflict != "":
			fail(row, "a conflict appeared since the review: "+row.Conflict)
		default:
			moves = append(moves, row)
		}
	}
	for id, row := range approved {
		if !planned[id] {
			fail(row, "it no longer needs moving, or can no longer be moved — organise again to review it")
		}
	}

	r.progTotal.Store(int64(len(moves)))
	r.setPhase(PhaseOrganising)
	stopProgress := events.StartProgress(r.db, event, r.progressSnapshot)
	moved, moveItems, oldDirs := r.organiseMoves(moves)
	stopProgress()
	items = append(items, moveItems...)
	for _, item := range moveItems {
		if item.Status == models.EventItemStatusError {
			failed++
		}
	}

	// Folders the moves emptied, deepest first so a parent left empty by its emptied
	// children goes too. A folder holding anything else — a cover, a cue sheet — stays.
	var libraries []models.Library
	r.db.Find(&libraries)
	for _, lib := range libraries {
		dirs := oldDirs[lib.ID]
		if len(dirs) == 0 {
			continue
		}
		sorted := make([]string, 0, len(dirs))
		for d := range dirs {
			sorted = append(sorted, d)
			modules.ForgetPlexFolder(d)
		}
		sort.Slice(sorted, func(i, j int) bool { return len(sorted[i]) > len(sorted[j]) })
		for _, d := range sorted {
			modules.RemoveEmptyDirs(d, lib.Path)
		}
		// Plex finds a moved file by scanning; refreshing the album it knew would only
		// re-read files that are no longer there.
		if client := r.plex.ForLibrary(lib); client != nil && moved > 0 {
			if err := client.ScanSections(); err != nil {
				logger.Log.Warnf("organise: could not ask Plex to scan %s: %s", lib.Name, err.Error())
			}
		}
	}

	status := models.EventStatusOK
	if failed > 0 {
		status = models.EventStatusError
	}
	event.Stats = []models.EventStat{
		{Label: "Moved", Value: moved, Filter: models.EventItemStatusChanged},
		{Label: "Failed", Value: failed, Kind: models.EventStatBad, Filter: models.EventItemStatusError},
	}
	events.Finish(r.db, event, status, fmt.Sprintf("%d file(s) moved · %d failed", moved, failed),
		map[string]any{"moved": moved, "failed": failed, "libraries": plan.Libraries})
	events.AddItems(r.db, event, items)
}

// organiseMoves makes the moves in two phases. First every file goes to a temporary
// name beside its target, which frees every source; then each goes into place, so a
// swap or a chain within the plan cannot land on a file it is about to move. It
// returns how many moved, a row per file, and the folders they left, by library.
func (r *Runner) organiseMoves(moves []OrganiseRow) (int, []models.EventItem, map[uuid.UUID]map[string]bool) {
	var items []models.EventItem
	fail := func(row OrganiseRow, msg string) {
		items = append(items, models.EventItem{Path: row.From, From: row.From, Status: models.EventItemStatusError, Error: msg})
	}
	type staged struct {
		row OrganiseRow
		tmp string
	}
	var stagedMoves []staged
	for _, m := range moves {
		r.setCurrent(filepath.Base(m.From))
		tmp := filepath.Join(filepath.Dir(m.To), "."+filepath.Base(m.To)+".organising-"+uuid.NewString()[:8])
		if err := modules.TransferFile(m.From, tmp, modules.TransferMove); err != nil {
			fail(m, "could not move it: "+err.Error())
			r.progDone.Add(1)
			continue
		}
		// The index follows in the same two phases: a path is unique there too, so a
		// row cannot take a path another row is about to leave.
		if err := r.repointPath(m.From, tmp); err != nil {
			if back := modules.TransferFile(tmp, m.From, modules.TransferMove); back != nil {
				logger.Log.Errorf("organise: %s is left at %s: %s", m.From, tmp, back.Error())
			}
			fail(m, "could not update the index, so it was not moved: "+err.Error())
			r.progDone.Add(1)
			continue
		}
		stagedMoves = append(stagedMoves, staged{m, tmp})
	}

	moved := 0
	oldDirs := map[uuid.UUID]map[string]bool{}
	for _, s := range stagedMoves {
		if err := os.Rename(s.tmp, s.row.To); err != nil {
			// Put it back where it was rather than leave a hidden file behind.
			if back := modules.TransferFile(s.tmp, s.row.From, modules.TransferMove); back != nil {
				logger.Log.Errorf("organise: %s is left at %s: %s", s.row.From, s.tmp, back.Error())
				fail(s.row, fmt.Sprintf("could not move it into place (%s), and it is left at %s", err.Error(), s.tmp))
			} else {
				_ = r.repointPath(s.tmp, s.row.From)
				fail(s.row, "could not move it into place: "+err.Error())
			}
			r.progDone.Add(1)
			continue
		}
		if err := r.repointPath(s.tmp, s.row.To); err != nil {
			logger.Log.Errorf("organise: moved %s to %s but could not update the index: %s", s.row.From, s.row.To, err.Error())
			fail(s.row, "moved to "+s.row.To+", but the index could not be updated: "+err.Error())
		} else {
			moved++
			items = append(items, models.EventItem{Path: s.row.To, From: s.row.From, Status: models.EventItemStatusChanged})
		}
		if oldDirs[s.row.LibraryID] == nil {
			oldDirs[s.row.LibraryID] = map[string]bool{}
		}
		oldDirs[s.row.LibraryID][filepath.Dir(s.row.From)] = true
		r.progDone.Add(1)
	}
	return moved, items, oldDirs
}

// repointPath carries every row keyed by a file's path to its new one: the index row
// (identity, pin and all), the tempo analysis and the fingerprint lookup. In one
// transaction, so a failure leaves the index describing where the file was.
func (r *Runner) repointPath(from, to string) error {
	return r.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&models.LibraryItem{}).Where("path = ?", from).Update("path", to).Error; err != nil {
			return err
		}
		for _, model := range []any{&models.TrackTempo{}, &models.AcoustIDLookup{}} {
			// A row already at the target describes a file that is gone.
			if err := tx.Where("path = ?", to).Delete(model).Error; err != nil {
				return err
			}
			if err := tx.Model(model).Where("path = ?", from).Update("path", to).Error; err != nil {
				return err
			}
		}
		return nil
	})
}

// organiseItems lists the identified files a scope covers.
func (r *Runner) organiseItems(scope OrganiseScope) ([]models.LibraryItem, error) {
	switch {
	case scope.LibraryID != uuid.Nil:
		var items []models.LibraryItem
		err := r.db.Where("library_id = ?", scope.LibraryID).Scopes(models.TaggableItems).Order("path").Find(&items).Error
		return items, err
	case scope.ArtistMBID != "":
		return collection.ArtistItems(r.db, scope.ArtistMBID)
	default:
		return collection.ReleaseGroupItems(r.db, scope.ReleaseGroupMBID)
	}
}

type organiseLibrary struct {
	library models.Library
	naming  models.NamingTemplates
	managed bool
}

// organiseLibraries resolves the libraries of the files in scope, with their naming
// templates and whether Lidarr names their files. A library scope that is Lidarr's is
// refused outright, rather than listed as one long column of skipped files.
func (r *Runner) organiseLibraries(items []models.LibraryItem, scope OrganiseScope) (map[uuid.UUID]organiseLibrary, error) {
	ids := map[uuid.UUID]bool{}
	for _, item := range items {
		ids[item.LibraryID] = true
	}
	if scope.LibraryID != uuid.Nil {
		ids[scope.LibraryID] = true
	}
	out := map[uuid.UUID]organiseLibrary{}
	for id := range ids {
		var lib models.Library
		if err := r.db.First(&lib, "id = ?", id).Error; err != nil {
			if id == scope.LibraryID {
				return nil, fmt.Errorf("library not found: %w", err)
			}
			continue
		}
		manager, _, err := components.BuildForLibrary(r.db, lib)
		if err != nil {
			return nil, fmt.Errorf("could not resolve the manager of %s: %w", lib.Name, err)
		}
		managed := manager.Type() == models.ManagerTypeLidarr
		if managed && id == scope.LibraryID {
			return nil, ErrOrganiseManaged
		}
		out[id] = organiseLibrary{library: lib, naming: components.TaggerForLibrary(r.db, lib).Naming(), managed: managed}
	}
	return out, nil
}

// relSlash is path below root with forward slashes, or path itself outside it.
func relSlash(root, path string) string {
	rel, err := filepath.Rel(root, path)
	if err != nil || strings.HasPrefix(rel, "..") {
		return filepath.ToSlash(path)
	}
	return filepath.ToSlash(rel)
}
