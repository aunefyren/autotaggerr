package settings

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/aunefyren/autotaggerr/models"
)

func baseConfig() models.ConfigStruct {
	return models.ConfigStruct{
		Timezone:                       "Europe/Oslo",
		Database:                       models.DatabaseConfig{Type: "sqlite", DSN: "config/autotaggerr.db"},
		PrivateKey:                     "a-key",
		AutotaggerrPort:                8080,
		AutotaggerrName:                "Autotaggerr",
		AutotaggerrEnvironment:         "prod",
		AutotaggerrLogLevel:            "info",
		AutotaggerrProcessCronSchedule: "0 0 18 * * 7",
		AutotaggerrMirrorCronSchedule:  "0 0 3 * * *",
		AutotaggerrHealthCronSchedule:  "0 */5 * * * *",
		AutotaggerrProcessConcurrency:  4,
		SMTPPassword:                   "hunter2",
	}
}

func values(pairs map[string]any) map[string]json.RawMessage {
	out := map[string]json.RawMessage{}
	for key, value := range pairs {
		raw, err := json.Marshal(value)
		if err != nil {
			panic(err)
		}
		out[key] = raw
	}
	return out
}

// TestDescribeHidesSecrets is the property that keeps a password out of every page
// load: the surface says whether one is set, never what it is.
func TestDescribeHidesSecrets(t *testing.T) {
	view := Describe(baseConfig())

	found := false
	for _, section := range view.Sections {
		for _, field := range section.Fields {
			if field.Type != TypeSecret {
				continue
			}
			if field.Value != nil {
				t.Errorf("%s carried a value in the settings view", field.Key)
			}
			if field.Key == "smtp_password" {
				found = true
				if !field.SecretSet {
					t.Error("smtp_password should report that a value is stored")
				}
			}
		}
	}
	if !found {
		t.Fatal("smtp_password was not in the described surface")
	}

	// Every field must resolve, and read-only ones must not claim to be editable.
	for _, section := range view.Sections {
		if len(section.Fields) == 0 {
			t.Errorf("section %s has no fields", section.ID)
		}
		for _, field := range section.Fields {
			if field.Tier == TierReadOnly && field.Editable {
				t.Errorf("%s is read-only but reports as editable", field.Key)
			}
			if field.Label == "" || field.Type == "" || field.Tier == "" {
				t.Errorf("%s is missing label/type/tier", field.Key)
			}
		}
	}
}

func TestApplyChangesValues(t *testing.T) {
	cfg := baseConfig()

	updated, changed, err := Apply(cfg, values(map[string]any{
		"autotaggerr_log_level":           "debug",
		"autotaggerr_process_concurrency": 8,
		"autotaggerr_name":                "Autotaggerr", // unchanged: must not be reported
	}))
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if updated.AutotaggerrLogLevel != "debug" || updated.AutotaggerrProcessConcurrency != 8 {
		t.Fatalf("values not applied: %+v", updated)
	}
	if len(changed) != 2 {
		t.Errorf("changed = %v, want only the two that moved", changed)
	}
	// The caller's config is untouched — Apply works on a copy.
	if cfg.AutotaggerrLogLevel != "info" {
		t.Error("Apply mutated the config it was given")
	}
}

// TestApplyMirrorInversion covers the one field whose UI meaning is the inverse of
// its stored meaning: "keep the mirror refreshed" on == autotaggerr_mirror_disabled
// false. Getting this backwards silently stops the refresh.
func TestApplyMirrorInversion(t *testing.T) {
	cfg := baseConfig()

	off, changed, err := Apply(cfg, values(map[string]any{"autotaggerr_mirror_enabled": false}))
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if !off.AutotaggerrMirrorDisabled {
		t.Error("turning the mirror off should set autotaggerr_mirror_disabled")
	}
	if len(changed) != 1 {
		t.Errorf("changed = %v, want the mirror key", changed)
	}

	on, _, err := Apply(off, values(map[string]any{"autotaggerr_mirror_enabled": true}))
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if on.AutotaggerrMirrorDisabled {
		t.Error("turning the mirror on should clear autotaggerr_mirror_disabled")
	}
}

func TestApplyRejects(t *testing.T) {
	cfg := baseConfig()

	cases := []struct {
		name  string
		edits map[string]any
		want  string
	}{
		{"unknown key", map[string]any{"nope": "x"}, "not a setting"},
		{"read-only key", map[string]any{"database.dsn": "/tmp/x.db"}, "cannot be changed"},
		{"wrong type", map[string]any{"autotaggerr_port": "eight thousand"}, "whole number"},
		{"port out of range", map[string]any{"autotaggerr_port": 99999}, "between 1 and 65535"},
		{"empty name", map[string]any{"autotaggerr_name": "  "}, "cannot be empty"},
		{"bad cron", map[string]any{"autotaggerr_process_cron_schedule": "every tuesday"}, "valid schedule"},
		{"bad timezone", map[string]any{"timezone": "Mars/Olympus"}, "timezone"},
		{"bad log level", map[string]any{"autotaggerr_log_level": "shouty"}, "must be one of"},
		{"bad external url", map[string]any{"autotaggerr_external_url": "example.com"}, "http://"},
		{"bad environment", map[string]any{"autotaggerr_environment": "staging"}, "must be one of"},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			updated, changed, err := Apply(cfg, values(c.edits))
			if err == nil {
				t.Fatalf("expected a rejection, got %+v", changed)
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Errorf("error %q should explain the problem (%q)", err.Error(), c.want)
			}
			// All-or-nothing: a rejected edit leaves the config exactly as it was.
			if !reflect.DeepEqual(updated, cfg) {
				t.Error("a rejected edit must not change anything")
			}
		})
	}
}

// TestApplyIsAllOrNothing pairs a good edit with a bad one. A settings page that
// half-saves is a state nobody can reason about.
func TestApplyIsAllOrNothing(t *testing.T) {
	cfg := baseConfig()
	updated, _, err := Apply(cfg, values(map[string]any{
		"autotaggerr_log_level": "debug",
		"autotaggerr_port":      0,
	}))
	if err == nil {
		t.Fatal("expected the invalid port to reject the whole save")
	}
	if updated.AutotaggerrLogLevel != "info" {
		t.Error("the valid half of a rejected save was applied anyway")
	}
}

func TestLiveKeysSplitsByTier(t *testing.T) {
	live, deferred := LiveKeys([]string{
		"autotaggerr_log_level",             // live
		"autotaggerr_process_cron_schedule", // live
		"autotaggerr_port",                  // restart
		"timezone",                          // restart
	})
	if len(live) != 2 || live[0] != "autotaggerr_log_level" {
		t.Errorf("live = %v", live)
	}
	if len(deferred) != 2 || deferred[0] != "autotaggerr_port" {
		t.Errorf("deferred = %v", deferred)
	}
}

// TestKeysAreUnique guards the table itself: two fields sharing a key would make one
// of them unreachable, and the duplicate is invisible until someone edits the wrong
// one.
func TestKeysAreUnique(t *testing.T) {
	seen := map[string]bool{}
	for _, section := range Sections() {
		for _, field := range section.Fields {
			if seen[field.Key] {
				t.Errorf("duplicate settings key %q", field.Key)
			}
			seen[field.Key] = true
		}
	}
}

// TestEveryConfigKeyHasAHome is the guard behind the promise the page makes: anything
// Autotaggerr can be told at startup is reachable from the UI. Every JSON key on
// ConfigStruct must be a field on the page — so adding a config key without a home
// fails here rather than being discovered by a user editing a file the UI never
// mentions.
//
// It used to allow a second answer, a list of keys named as "managed elsewhere". That
// list is gone with the keys it described: a setting that lives in the database is a
// row on its own page and no longer has a config.json key at all, so "on the page" is
// now the only valid home. "Nowhere" still is not one.
func TestEveryConfigKeyHasAHome(t *testing.T) {
	homed := map[string]bool{}
	for _, section := range Sections() {
		for _, field := range section.Fields {
			homed[field.Key] = true
		}
	}
	// The mirror switch is stored inverted, so the page's key is not the file's.
	homed["autotaggerr_mirror_disabled"] = true

	structType := reflect.TypeOf(models.ConfigStruct{})
	for i := 0; i < structType.NumField(); i++ {
		tag := structType.Field(i).Tag.Get("json")
		key, _, _ := strings.Cut(tag, ",")
		if key == "" || key == "-" {
			continue
		}
		// database is a nested object; its leaves are keyed "database.<field>".
		if key == "database" {
			nested := reflect.TypeOf(models.DatabaseConfig{})
			for j := 0; j < nested.NumField(); j++ {
				nestedKey, _, _ := strings.Cut(nested.Field(j).Tag.Get("json"), ",")
				if nestedKey != "" && !homed["database."+nestedKey] {
					t.Errorf("config key database.%s has no home on the settings page", nestedKey)
				}
			}
			continue
		}
		if !homed[key] {
			t.Errorf("config key %q has no home on the settings page — add it to Sections()", key)
		}
	}
}
