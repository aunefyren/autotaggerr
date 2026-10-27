package components

import (
	"testing"

	"github.com/aunefyren/autotaggerr/models"
)

// TestMediaServersForLibrary: a library is refreshed on the enabled servers it is
// linked to, each in its own section — never on a server it is not linked to, and not
// on a linked one that is disabled.
func TestMediaServersForLibrary(t *testing.T) {
	db := testDB(t)
	home := models.MediaServer{Name: "Home", Type: models.MediaServerTypePlex, Enabled: true, BaseURL: "http://home:32400", Token: "a"}
	cabin := models.MediaServer{Name: "Cabin", Type: models.MediaServerTypePlex, Enabled: true, BaseURL: "http://cabin:32400", Token: "b"}
	off := models.MediaServer{Name: "Off", Type: models.MediaServerTypePlex, Enabled: true, BaseURL: "http://off:32400", Token: "c"}
	unlinked := models.MediaServer{Name: "Elsewhere", Type: models.MediaServerTypePlex, Enabled: true, BaseURL: "http://else:32400", Token: "d"}
	lib := models.Library{Name: "Music", Path: "/music", Enabled: true}
	for _, row := range []any{&home, &cabin, &off, &unlinked, &lib} {
		if err := db.Create(row).Error; err != nil {
			t.Fatal(err)
		}
	}
	db.Model(&off).Update("enabled", false)
	for _, link := range []models.LibraryMediaServer{
		{LibraryID: lib.ID, MediaServerID: home.ID, SectionKey: "4"},
		{LibraryID: lib.ID, MediaServerID: cabin.ID},
		{LibraryID: lib.ID, MediaServerID: off.ID},
	} {
		if err := db.Create(&link).Error; err != nil {
			t.Fatal(err)
		}
	}

	got := map[string]string{}
	for _, client := range MediaServersForLibrary(db, lib) {
		got[client.Name] = client.SectionKey
		if client.ServerID == "" {
			t.Errorf("%s has no server ID; its cache entries would not be scoped", client.Name)
		}
	}
	if len(got) != 2 || got["Home"] != "4" || got["Cabin"] != "" {
		t.Errorf("targets = %v, want Home in section 4 and Cabin in every section", got)
	}

	if clients := MediaServerClients(db); len(clients) != 3 || clients[off.ID.String()] != nil {
		t.Errorf("clients = %d, want the three enabled servers", len(clients))
	}
	if MediaServersForLibrary(nil, lib) != nil || len(MediaServerClients(nil)) != 0 {
		t.Error("no database gave servers")
	}
	if MediaServersForLibrary(db, models.Library{Name: "Bare"}) != nil {
		t.Error("a library with no links gave servers")
	}
}
