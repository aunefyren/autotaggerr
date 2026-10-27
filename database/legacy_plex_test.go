package database

import (
	"testing"

	"github.com/aunefyren/autotaggerr/models"
)

// TestMigrateLegacyPlex: an upgrade keeps refreshing what it refreshed — the config
// keys become one server, linked to every library that did not skip Plex, with its
// section — and the old columns go. Running it again changes nothing.
func TestMigrateLegacyPlex(t *testing.T) {
	db := testDB(t)
	for _, stmt := range []string{
		"ALTER TABLE libraries ADD COLUMN plex_section_key text",
		"ALTER TABLE libraries ADD COLUMN plex_skip numeric",
	} {
		if err := db.Exec(stmt).Error; err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
	linked := models.Library{Name: "Music", Path: "/music", Enabled: true}
	skipped := models.Library{Name: "Audiobooks", Path: "/books", Enabled: true}
	for _, l := range []*models.Library{&linked, &skipped} {
		if err := db.Create(l).Error; err != nil {
			t.Fatal(err)
		}
	}
	db.Exec("UPDATE libraries SET plex_section_key = '7', plex_skip = false WHERE id = ?", linked.ID)
	db.Exec("UPDATE libraries SET plex_skip = true WHERE id = ?", skipped.ID)

	for run := 0; run < 2; run++ {
		if err := MigrateLegacyPlex(db, "http://plex:32400/", "tok"); err != nil {
			t.Fatalf("run %d: %v", run, err)
		}
	}

	var servers []models.MediaServer
	db.Find(&servers)
	if len(servers) != 1 || servers[0].BaseURL != "http://plex:32400" || servers[0].Token != "tok" || !servers[0].Enabled {
		t.Fatalf("servers = %+v, want the one from config.json", servers)
	}
	var links []models.LibraryMediaServer
	db.Find(&links)
	if len(links) != 1 || links[0].LibraryID != linked.ID || links[0].SectionKey != "7" {
		t.Errorf("links = %+v, want the unskipped library in section 7", links)
	}
	if db.Migrator().HasColumn(&models.Library{}, "plex_section_key") || db.Migrator().HasColumn(&models.Library{}, "plex_skip") {
		t.Error("the legacy Plex columns are still there")
	}
}

// Without keys nothing is created, and the columns still go.
func TestMigrateLegacyPlexWithoutKeys(t *testing.T) {
	db := testDB(t)
	if err := db.Exec("ALTER TABLE libraries ADD COLUMN plex_skip numeric").Error; err != nil {
		t.Fatal(err)
	}
	if err := MigrateLegacyPlex(db, "", ""); err != nil {
		t.Fatal(err)
	}
	var count int64
	db.Model(&models.MediaServer{}).Count(&count)
	if count != 0 {
		t.Errorf("servers = %d, want none", count)
	}
	if db.Migrator().HasColumn(&models.Library{}, "plex_skip") {
		t.Error("plex_skip survived")
	}
}
