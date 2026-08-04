package settings

import (
	"encoding/json"
	"fmt"
	"sync"

	"github.com/aunefyren/autotaggerr/files"
	"github.com/aunefyren/autotaggerr/logger"
)

// saveMu serialises whole save operations. Two admins saving at once would otherwise
// interleave read-modify-write on the process-global config and lose one of the two
// edits — rare, but silent, and settings are exactly where a silently dropped edit is
// worst.
var saveMu sync.Mutex

// Result is what a save did, in the words the page reports back.
type Result struct {
	// Changed is every key whose value actually moved. A save that only re-sends what
	// is already stored changes nothing and says so.
	Changed []string `json:"changed"`
	// Applied are the live effects, one sentence each ("log level is now debug").
	Applied []string `json:"applied"`
	// RestartRequired are the changed keys that the running process cannot adopt.
	RestartRequired []string `json:"restart_required"`
}

// Save validates the edits, writes config.json, and re-applies what the running
// process can adopt.
//
// The order is deliberate: validate everything, then persist, then apply. Applying
// before persisting would leave a process running settings that a failed write means
// it will not have after a restart — the one inconsistency a user cannot see and
// cannot explain.
func Save(runtime *Runtime, values map[string]json.RawMessage) (Result, error) {
	saveMu.Lock()
	defer saveMu.Unlock()

	updated, changed, err := Apply(files.ConfigFile, values)
	if err != nil {
		return Result{}, err
	}
	if len(changed) == 0 {
		return Result{Changed: []string{}, Applied: []string{}, RestartRequired: []string{}}, nil
	}

	previous := files.ConfigFile
	files.ConfigFile = updated
	if err := files.SaveConfig(); err != nil {
		// Put the old config back: the process must not keep running settings that are
		// not in the file it will read at the next start.
		files.ConfigFile = previous
		logger.Log.Errorf("failed to save config: %s", err.Error())
		return Result{}, fmt.Errorf("could not write config.json — check that the config directory is writable")
	}

	live, deferred := LiveKeys(changed)
	result := Result{
		Changed:         changed,
		Applied:         runtime.Apply(updated, live),
		RestartRequired: deferred,
	}
	logger.Log.Infof("settings saved: %v", changed)
	return result, nil
}

// Current describes the settings surface against the live config.
func Current() View { return Describe(files.ConfigFile) }

// Reveal returns a secret's stored value, for the "show me what I set" affordance on
// the settings page. It is a separate, deliberate request rather than part of the
// page load: a password that is only sent when someone asks for it is not sitting in
// every settings response, in the browser's memory, or in a proxy's log.
//
// Read-only secrets are never revealed. The session signing key is the one that
// matters: it is not editable, so showing it serves no purpose the user can act on,
// while anything that could read it back could forge sessions.
func Reveal(key string) (string, error) {
	for _, section := range Sections() {
		for _, field := range section.Fields {
			if field.Key != key {
				continue
			}
			if field.Type != TypeSecret {
				return "", fmt.Errorf("%s is not a secret", field.Label)
			}
			if field.set == nil {
				return "", fmt.Errorf("%s cannot be shown", field.Label)
			}
			value, _ := field.get(files.ConfigFile).(string)
			return value, nil
		}
	}
	return "", fmt.Errorf("%q is not a setting", key)
}
