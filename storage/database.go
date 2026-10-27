package storage

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// database measures the database's files, the free pages inside it, and its tables.
// SQLite's dbstat table gives each table and index its size in pages; indexes are
// counted with their table, because "how big is the Activity history" means both.
func (m *Measurer) database() Database {
	out := Database{Groups: []TableGroup{}}
	if m.db.Dialector.Name() != "sqlite" {
		out.Error = "sizes are only measured for SQLite"
		return out
	}

	var file struct{ File string }
	if err := m.db.Raw("SELECT file FROM pragma_database_list WHERE name = 'main'").Scan(&file).Error; err != nil {
		out.Error = err.Error()
		return out
	}
	out.Path = file.File
	if out.Path != "" {
		out.File = fileSize(out.Path)
		out.WAL = fileSize(out.Path + "-wal")
		out.SHM = fileSize(out.Path + "-shm")
	}

	var free, pageSize int64
	m.db.Raw("PRAGMA freelist_count").Scan(&free)
	m.db.Raw("PRAGMA page_size").Scan(&pageSize)
	out.Reclaimable = free * pageSize

	type object struct{ Name, Table string }
	var objects []object
	if err := m.db.Raw("SELECT name, tbl_name AS \"table\" FROM sqlite_master WHERE type IN ('table', 'index')").Scan(&objects).Error; err != nil {
		out.Error = err.Error()
		return out
	}
	tableOf := map[string]string{}
	for _, o := range objects {
		tableOf[o.Name] = o.Table
	}

	type size struct {
		Name  string
		Bytes int64
	}
	var sizes []size
	if err := m.db.Raw("SELECT name, SUM(pgsize) AS bytes FROM dbstat GROUP BY name").Scan(&sizes).Error; err != nil {
		out.Error = "the per-table breakdown is unavailable: " + err.Error()
		return out
	}
	tables := map[string]*Table{}
	for _, s := range sizes {
		name := tableOf[s.Name]
		if name == "" {
			name = s.Name
		}
		t := tables[name]
		if t == nil {
			t = &Table{Name: name}
			tables[name] = t
		}
		t.Bytes += s.Bytes
	}

	groups := map[string]*TableGroup{}
	for name, t := range tables {
		if strings.HasPrefix(name, "sqlite_") {
			// SQLite's own bookkeeping (sqlite_master, sqlite_sequence).
			continue
		}
		m.db.Raw(fmt.Sprintf("SELECT COUNT(*) FROM %q", name)).Scan(&t.Rows)
		label := tableGroup(name)
		g := groups[label]
		if g == nil {
			g = &TableGroup{Label: label, Tables: []Table{}}
			groups[label] = g
		}
		g.Tables = append(g.Tables, *t)
		g.Bytes += t.Bytes
		g.Rows += t.Rows
	}
	for _, g := range groups {
		sort.Slice(g.Tables, func(i, j int) bool { return g.Tables[i].Bytes > g.Tables[j].Bytes })
		out.Groups = append(out.Groups, *g)
	}
	sort.Slice(out.Groups, func(i, j int) bool { return out.Groups[i].Bytes > out.Groups[j].Bytes })
	return out
}

// tableGroup names a table by what it holds, in the words the rest of the UI uses.
func tableGroup(table string) string {
	switch {
	case strings.HasPrefix(table, "event"):
		return "Activity history"
	case strings.HasPrefix(table, "musicbrainz_migration"):
		return "MusicBrainz changes"
	case strings.HasPrefix(table, "musicbrainz_"):
		return "MusicBrainz cache"
	case strings.HasPrefix(table, "provider_cache"), strings.HasPrefix(table, "manager_link"):
		return "Lidarr and Plex cache"
	case table == "library_items":
		return "File index"
	case strings.HasPrefix(table, "collection_"):
		return "Collection"
	case strings.HasPrefix(table, "track_"), strings.HasPrefix(table, "acoust"):
		return "Analysis and fingerprints"
	case strings.HasPrefix(table, "artwork_"):
		return "Artwork index"
	case strings.HasPrefix(table, "download_"):
		return "Downloads"
	}
	return "Settings and accounts"
}

func fileSize(path string) int64 {
	info, err := os.Stat(filepath.Clean(path))
	if err != nil {
		return 0
	}
	return info.Size()
}
