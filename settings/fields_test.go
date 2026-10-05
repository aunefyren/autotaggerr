package settings

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/aunefyren/autotaggerr/files"
	"github.com/aunefyren/autotaggerr/models"
)

// editFor picks a value for a field that its validator accepts and that differs from
// what cfg already holds, so applying it has to go through the field's setter and
// come back out of its getter.
func editFor(t *testing.T, field Field, cfg models.ConfigStruct) any {
	t.Helper()
	current := field.get(cfg)
	switch field.Type {
	case TypeBool:
		shown, _ := current.(bool)
		return !shown
	case TypeInt:
		shown, _ := current.(int)
		if shown == 7 {
			return 8
		}
		return 7
	case TypeCron:
		return "0 30 4 * * *"
	case TypeSelect:
		for _, option := range field.Options {
			if !sameValue(option, current) {
				return option
			}
		}
		t.Fatalf("%s has no option other than its current value", field.Key)
	case TypeSecret:
		return "a-new-secret"
	case TypeString:
		switch field.Key {
		case "timezone":
			return "America/New_York"
		case "autotaggerr_external_url":
			return "https://autotaggerr.example.com"
		}
		return "an edited value"
	}
	t.Fatalf("%s has a type (%s) this test does not know how to edit", field.Key, field.Type)
	return nil
}

// TestEveryEditableFieldRoundTrips is the property the table of closures has to hold
// for every row, not only the few the other tests name: a value the field accepts is
// reported as changed, and reads back out of its getter as what was sent. A setter
// that wrote the wrong struct field — the easy copy-paste mistake in this file —
// fails here.
func TestEveryEditableFieldRoundTrips(t *testing.T) {
	cfg := baseConfig()
	editable := 0

	for _, section := range Sections() {
		for _, field := range section.Fields {
			if field.set == nil {
				continue
			}
			editable++
			t.Run(field.Key, func(t *testing.T) {
				value := editFor(t, field, cfg)
				updated, changed, err := Apply(cfg, values(map[string]any{field.Key: value}))
				if err != nil {
					t.Fatalf("Apply(%v): %v", value, err)
				}
				if !reflect.DeepEqual(changed, []string{field.Key}) {
					t.Errorf("changed = %v, want [%s]", changed, field.Key)
				}
				if got := field.get(updated); !sameValue(got, value) {
					t.Errorf("getter returned %v after setting %v", got, value)
				}
			})
		}
	}
	if editable == 0 {
		t.Fatal("no editable fields found")
	}
}

// TestApplyRejectsWrongTypes covers the decode-before-validate rule for each setter
// kind: a wrong type is reported as a wrong type.
func TestApplyRejectsWrongTypes(t *testing.T) {
	cfg := baseConfig()
	cases := []struct {
		key   string
		value any
		want  string
	}{
		{"autotaggerr_name", 42, "expected text"},
		{"smtp_enabled", "yes", "true or false"},
		{"autotaggerr_process_concurrency", true, "whole number"},
	}
	for _, c := range cases {
		t.Run(c.key, func(t *testing.T) {
			_, _, err := Apply(cfg, values(map[string]any{c.key: c.value}))
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Errorf("error = %v, want one containing %q", err, c.want)
			}
		})
	}
}

// TestApplyAcceptsEmptyOptionalValues: an empty external URL and an empty timezone
// both mean "not set" (no redirect base; the host's zone), not a validation failure.
func TestApplyAcceptsEmptyOptionalValues(t *testing.T) {
	cfg := baseConfig()
	cfg.AutotaggerrExternalURL = "https://autotaggerr.example.com"

	updated, changed, err := Apply(cfg, values(map[string]any{
		"autotaggerr_external_url": "",
		"timezone":                 "",
	}))
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if updated.AutotaggerrExternalURL != "" || updated.Timezone != "" {
		t.Errorf("empty values not applied: %q, %q", updated.AutotaggerrExternalURL, updated.Timezone)
	}
	if len(changed) != 2 {
		t.Errorf("changed = %v, want both keys", changed)
	}
}

// TestSaveFailureRestoresTheRunningConfig: when config.json cannot be written, the
// process must not keep running settings the next start will not read.
func TestSaveFailureRestoresTheRunningConfig(t *testing.T) {
	blocker := filepath.Join(t.TempDir(), "not-a-dir")
	if err := os.WriteFile(blocker, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(files.SetConfigPaths(filepath.Join(blocker, "config")))

	previous := files.ConfigFile
	t.Cleanup(func() { files.ConfigFile = previous })
	files.ConfigFile = baseConfig()

	if _, err := Save(nil, values(map[string]any{"autotaggerr_port": 9090})); err == nil ||
		!strings.Contains(err.Error(), "could not write config.json") {
		t.Fatalf("Save error = %v, want a write failure", err)
	}
	if files.ConfigFile.AutotaggerrPort != 8080 {
		t.Errorf("running port = %d, want the unsaved edit rolled back to 8080", files.ConfigFile.AutotaggerrPort)
	}
}

// TestCurrentDescribesTheLiveConfig: the page load reads the process global.
func TestCurrentDescribesTheLiveConfig(t *testing.T) {
	previous := files.ConfigFile
	t.Cleanup(func() { files.ConfigFile = previous })
	files.ConfigFile = baseConfig()
	files.ConfigFile.AutotaggerrPort = 4242

	for _, section := range Current().Sections {
		for _, field := range section.Fields {
			if field.Key == "autotaggerr_port" {
				if field.Value != 4242 {
					t.Errorf("port value = %v, want 4242", field.Value)
				}
				return
			}
		}
	}
	t.Error("autotaggerr_port missing from the current view")
}

// TestRuntimeSkipsAnUnschedulableExpression: an expression the scheduler refuses is
// logged and leaves no task behind, rather than keeping the stale one.
func TestRuntimeSkipsAnUnschedulableExpression(t *testing.T) {
	runtime, _ := newRuntimeForTest(t)
	cfg := baseConfig()
	runtime.Schedule(cfg)

	cfg.AutotaggerrProcessCronSchedule = "not a cron"
	runtime.Schedule(cfg)

	runtime.mu.Lock()
	_, scan := runtime.tasks["scan"]
	_, mirror := runtime.tasks["metadata refresh"]
	runtime.mu.Unlock()
	if scan {
		t.Error("a task is still installed for the unschedulable scan expression")
	}
	if !mirror {
		t.Error("the valid metadata refresh schedule should still be installed")
	}
}
