// Package settings exposes config.json as an editable surface for the web UI.
//
// Everything Autotaggerr can be told at startup — a CLI flag, a Docker environment
// variable, a key in config.json — should be reachable from the UI, so that running
// the container is not a prerequisite for changing how it behaves. This package is
// the single description of that surface: which keys exist, what each one is called
// in human words, what it accepts, and whether changing it takes effect now or at the
// next start.
//
// The description is a table of closures rather than reflection over ConfigStruct.
// Struct tags would have to carry labels, help text, options and validation anyway,
// and a field that is *deliberately* absent from the UI (the private key) has to be a
// decision someone wrote down, not an omission nobody notices.
package settings

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"codnect.io/chrono"
	"github.com/aunefyren/autotaggerr/models"
	"github.com/sirupsen/logrus"
)

// Tiers say when an edit takes effect. The distinction is the honest part of the
// page: a setting the running process cannot adopt must not look like one it can.
const (
	// TierLive is re-applied to the running process the moment it is saved.
	TierLive = "live"
	// TierRestart is written to config.json now and read at the next start.
	TierRestart = "restart"
	// TierReadOnly is shown but never written — bootstrap config that the process
	// cannot change underneath itself (where the database is), or values it derives.
	TierReadOnly = "readonly"
)

// Field types the UI renders. `secret` is a string whose value never leaves the
// server; `cron` is a string validated as a schedule.
const (
	TypeString = "string"
	TypeInt    = "int"
	TypeBool   = "bool"
	TypeSelect = "select"
	TypeCron   = "cron"
	TypeSecret = "secret"
	TypeList   = "list"
)

// Field is one setting: how to describe it, how to read it, and how to write it.
type Field struct {
	Key         string   `json:"key"`
	Label       string   `json:"label"`
	Help        string   `json:"help,omitempty"`
	Type        string   `json:"type"`
	Tier        string   `json:"tier"`
	Options     []string `json:"options,omitempty"`
	Placeholder string   `json:"placeholder,omitempty"`

	// get reads the field's current value in the shape the UI wants.
	get func(models.ConfigStruct) any
	// set validates a raw JSON value and applies it. Nil means the field is not
	// editable, whatever its tier claims — the tier is the explanation, this is the
	// enforcement.
	set func(*models.ConfigStruct, json.RawMessage) error
}

// Section groups fields by what they do, which is the only grouping a reader of the
// page can act on. The order of sections and of fields within them is the order the
// page renders.
type Section struct {
	ID          string  `json:"id"`
	Title       string  `json:"title"`
	Description string  `json:"description,omitempty"`
	Fields      []Field `json:"fields"`
}

// Sections returns the full settings surface. It is a function rather than a package
// variable so that no caller can mutate the shared description.
func Sections() []Section {
	return []Section{
		{
			ID:          "identity",
			Title:       "Identity",
			Description: "What this instance calls itself and where it lives.",
			Fields: []Field{
				{
					Key: "autotaggerr_name", Label: "Instance name", Type: TypeString, Tier: TierRestart,
					Help:        "Shown in the browser title and returned by the API.",
					Placeholder: "Autotaggerr",
					get:         func(c models.ConfigStruct) any { return c.AutotaggerrName },
					set:         setString(func(c *models.ConfigStruct, v string) { c.AutotaggerrName = v }, required),
				},
				{
					Key: "autotaggerr_external_url", Label: "External URL", Type: TypeString, Tier: TierRestart,
					Help:        "The address others reach this instance on. Used to build external login redirects.",
					Placeholder: "https://autotaggerr.example.com",
					get:         func(c models.ConfigStruct) any { return c.AutotaggerrExternalURL },
					set:         setString(func(c *models.ConfigStruct, v string) { c.AutotaggerrExternalURL = v }, optionalURL),
				},
				{
					Key: "autotaggerr_port", Label: "Port", Type: TypeInt, Tier: TierRestart,
					Help: "The port the HTTP server listens on.",
					get:  func(c models.ConfigStruct) any { return c.AutotaggerrPort },
					set:  setInt(func(c *models.ConfigStruct, v int) { c.AutotaggerrPort = v }, intRange(1, 65535)),
				},
				{
					// Restart rather than live: the zone is a process-wide variable read by
					// every goroutine that formats a time, and rewriting it under a running
					// scan is a data race for a value that only matters at the next
					// scheduled run anyway.
					Key: "timezone", Label: "Timezone", Type: TypeString, Tier: TierRestart,
					Help:        "IANA name. Decides when cron schedules fire and how times are shown.",
					Placeholder: "Europe/Oslo",
					get:         func(c models.ConfigStruct) any { return c.Timezone },
					set:         setString(func(c *models.ConfigStruct, v string) { c.Timezone = v }, validTimezone),
				},
				{
					Key: "autotaggerr_version", Label: "Version", Type: TypeString, Tier: TierReadOnly,
					Help: "Set at release time; not editable.",
					get:  func(c models.ConfigStruct) any { return c.AutotaggerrVersion },
				},
			},
		},
		{
			ID:          "scanning",
			Title:       "Scanning",
			Description: "When libraries are walked and how hard the walk is pushed.",
			Fields: []Field{
				{
					Key: "autotaggerr_process_cron_schedule", Label: "Scan schedule", Type: TypeCron, Tier: TierLive,
					Help:        "Six-field cron (with seconds). Default is Sundays at 18:00.",
					Placeholder: "0 0 18 * * 7",
					get:         func(c models.ConfigStruct) any { return c.AutotaggerrProcessCronSchedule },
					set:         setString(func(c *models.ConfigStruct, v string) { c.AutotaggerrProcessCronSchedule = v }, validCron),
				},
				{
					Key: "autotaggerr_process_concurrency", Label: "Files in parallel", Type: TypeInt, Tier: TierLive,
					Help: "Workers per library scan. FLAC rewrites are disk-bound, so more is not always faster.",
					get:  func(c models.ConfigStruct) any { return c.AutotaggerrProcessConcurrency },
					set:  setInt(func(c *models.ConfigStruct, v int) { c.AutotaggerrProcessConcurrency = v }, intRange(1, 64)),
				},
				{
					Key: "autotaggerr_health_cron_schedule", Label: "Health-check schedule", Type: TypeCron, Tier: TierLive,
					Help:        "How often the Lidarr/Plex connections are probed. Only a change in health is recorded.",
					Placeholder: "0 */5 * * * *",
					get:         func(c models.ConfigStruct) any { return c.AutotaggerrHealthCronSchedule },
					set:         setString(func(c *models.ConfigStruct, v string) { c.AutotaggerrHealthCronSchedule = v }, validCron),
				},
			},
		},
		{
			// Plex is the one connection that is not a manager row: there is no Plex
			// manager type and no Managers entry for it, so config.json is where it
			// lives and main.go builds the process-level client from these at startup.
			//
			// It used to be listed as "managed elsewhere" alongside the lidarr_* keys,
			// pointing at a page that has never had a Plex field on it — and under a
			// heading promising that editing the file changes nothing, which was the
			// opposite of true. These are ordinary settings; this is where they belong.
			ID:          "plex",
			Title:       "Plex",
			Description: "The Plex server told to refresh an album after its files change. Optional — leave empty and nothing is notified.",
			Fields: []Field{
				{
					Key: "plex_base_url", Label: "Base URL", Type: TypeString, Tier: TierRestart,
					Help:        "e.g. http://192.168.1.10:32400. Both this and the token are needed before anything is sent.",
					Placeholder: "http://plex.local:32400",
					get:         func(c models.ConfigStruct) any { return c.PlexBaseURL },
					set:         setString(func(c *models.ConfigStruct, v string) { c.PlexBaseURL = v }, nil),
				},
				{
					Key: "plex_token", Label: "Token", Type: TypeSecret, Tier: TierRestart,
					Help: "A Plex authentication token (X-Plex-Token).",
					get:  func(c models.ConfigStruct) any { return c.PlexToken },
					set:  setString(func(c *models.ConfigStruct, v string) { c.PlexToken = v }, nil),
				},
			},
		},
		{
			// "Mirror" stays the package name, the config keys and the word the docs use
			// for the local copy. It is not a word the UI says: every surface a user
			// presses calls this one thing a metadata refresh, and a settings section
			// named after the implementation is how a second name gets learned.
			ID:          "mirror",
			Title:       "Metadata refresh",
			Description: "Keeping the local copy of your metadata sources current, so browsing reads it instead of their rate-limited APIs.",
			Fields: []Field{
				{
					// Stored as "disabled" but shown as "enabled": a bool missing from an
					// existing config.json decodes as false, and false has to keep meaning
					// the default. The inversion lives here so the UI can say the useful
					// thing without the file changing meaning.
					Key: "autotaggerr_mirror_enabled", Label: "Keep metadata refreshed", Type: TypeBool, Tier: TierLive,
					Help: "Off stops the scheduled refresh; on-demand lookups still work.",
					get:  func(c models.ConfigStruct) any { return !c.AutotaggerrMirrorDisabled },
					set:  setBool(func(c *models.ConfigStruct, v bool) { c.AutotaggerrMirrorDisabled = !v }),
				},
				{
					Key: "autotaggerr_mirror_cron_schedule", Label: "Refresh schedule", Type: TypeCron, Tier: TierLive,
					Help:        "Default is nightly at 03:00, away from the weekly scan.",
					Placeholder: "0 0 3 * * *",
					get:         func(c models.ConfigStruct) any { return c.AutotaggerrMirrorCronSchedule },
					set:         setString(func(c *models.ConfigStruct, v string) { c.AutotaggerrMirrorCronSchedule = v }, validCron),
				},
			},
		},
		{
			// Separate from the metadata section because artwork is a separate kind of
			// data source, configured in its own place on /data-sources and spending a
			// budget the metadata refresh never touches. Folding the two schedules into
			// one section would imply a relationship the code deliberately does not have.
			ID:          "artwork",
			Title:       "Artwork refresh",
			Description: "Fetching album covers and artist images ahead of the pages that show them, so a discography paints from disk instead of downloading a hundred thumbnails while you wait.",
			Fields: []Field{
				{
					// Stored as "disabled", shown as "enabled" — same inversion, same
					// reason, as the metadata switch above.
					Key: "autotaggerr_artwork_enabled", Label: "Keep artwork fetched", Type: TypeBool, Tier: TierLive,
					Help: "Off stops the scheduled fetch and the automatic one for new artists; images are still fetched when a page asks for them.",
					get:  func(c models.ConfigStruct) any { return !c.AutotaggerrArtworkDisabled },
					set:  setBool(func(c *models.ConfigStruct, v bool) { c.AutotaggerrArtworkDisabled = !v }),
				},
				{
					Key: "autotaggerr_artwork_cron_schedule", Label: "Fetch schedule", Type: TypeCron, Tier: TierLive,
					Help:        "Default is nightly at 05:00. New artists and albums fetch their own artwork as they arrive, so this is a backstop rather than the main path.",
					Placeholder: "0 0 5 * * *",
					get:         func(c models.ConfigStruct) any { return c.AutotaggerrArtworkCronSchedule },
					set:         setString(func(c *models.ConfigStruct, v string) { c.AutotaggerrArtworkCronSchedule = v }, validCron),
				},
			},
		},
		{
			// Named for what the setting governs, not for who supplies the data. A
			// metadata source is a component you configure zero, one or several of, and
			// only MusicBrainz is implemented today — but *whether an identity change
			// needs approval* is a policy about this library, not a property of the
			// service that reported it. Naming the section after the one current source
			// would have to be undone by the second, and would read as absent to anyone
			// running none.
			ID:    "migrations",
			Title: "Metadata migrations",
			Description: "Metadata sources merge and delete entities. Each switch holds that kind of change " +
				"for your approval instead of applying it; off means it is applied as it is found.",
			Fields: []Field{
				{
					Key: "autotaggerr_migration_review_releases", Label: "Review release changes", Type: TypeBool, Tier: TierLive,
					get: func(c models.ConfigStruct) any { return c.AutotaggerrMigrationReviewReleases },
					set: setBool(func(c *models.ConfigStruct, v bool) { c.AutotaggerrMigrationReviewReleases = v }),
				},
				{
					Key: "autotaggerr_migration_review_artists", Label: "Review artist changes", Type: TypeBool, Tier: TierLive,
					get: func(c models.ConfigStruct) any { return c.AutotaggerrMigrationReviewArtists },
					set: setBool(func(c *models.ConfigStruct, v bool) { c.AutotaggerrMigrationReviewArtists = v }),
				},
				{
					Key: "autotaggerr_migration_review_pinned", Label: "Review anything that rewrites a manual pin", Type: TypeBool, Tier: TierLive,
					get: func(c models.ConfigStruct) any { return c.AutotaggerrMigrationReviewPinned },
					set: setBool(func(c *models.ConfigStruct, v bool) { c.AutotaggerrMigrationReviewPinned = v }),
				},
				{
					Key: "autotaggerr_migration_review_deletions", Label: "Review deletions", Type: TypeBool, Tier: TierLive,
					Help: "A deleted entity un-identifies files rather than re-pointing them.",
					get:  func(c models.ConfigStruct) any { return c.AutotaggerrMigrationReviewDeletions },
					set:  setBool(func(c *models.ConfigStruct, v bool) { c.AutotaggerrMigrationReviewDeletions = v }),
				},
			},
		},
		{
			ID:          "email",
			Title:       "Email",
			Description: "The SMTP server Autotaggerr sends mail through.",
			Fields: []Field{
				{
					Key: "smtp_enabled", Label: "Send email", Type: TypeBool, Tier: TierRestart,
					get: func(c models.ConfigStruct) any { return c.SMTPEnabled },
					set: setBool(func(c *models.ConfigStruct, v bool) { c.SMTPEnabled = v }),
				},
				{
					Key: "smtp_host", Label: "Host", Type: TypeString, Tier: TierRestart,
					Placeholder: "smtp.example.com",
					get:         func(c models.ConfigStruct) any { return c.SMTPHost },
					set:         setString(func(c *models.ConfigStruct, v string) { c.SMTPHost = v }, nil),
				},
				{
					Key: "smtp_port", Label: "Port", Type: TypeInt, Tier: TierRestart,
					get: func(c models.ConfigStruct) any { return c.SMTPPort },
					set: setInt(func(c *models.ConfigStruct, v int) { c.SMTPPort = v }, intRange(0, 65535)),
				},
				{
					Key: "smtp_tls", Label: "Encryption", Type: TypeSelect, Tier: TierRestart,
					Options: models.SMTPTLSModes,
					Help:    "auto reads it from the port: 465 is implicit TLS, anything else upgrades with STARTTLS when the server offers it. starttls refuses to send if it is not offered; none never encrypts.",
					get:     func(c models.ConfigStruct) any { return c.SMTPTLS },
					set: setString(func(c *models.ConfigStruct, v string) { c.SMTPTLS = v },
						oneOf(models.SMTPTLSModes...)),
				},
				{
					Key: "smtp_username", Label: "Username", Type: TypeString, Tier: TierRestart,
					get: func(c models.ConfigStruct) any { return c.SMTPUsername },
					set: setString(func(c *models.ConfigStruct, v string) { c.SMTPUsername = v }, nil),
				},
				{
					Key: "smtp_password", Label: "Password", Type: TypeSecret, Tier: TierRestart,
					get: func(c models.ConfigStruct) any { return c.SMTPPassword },
					set: setString(func(c *models.ConfigStruct, v string) { c.SMTPPassword = v }, nil),
				},
				{
					Key: "smtp_from", Label: "From address", Type: TypeString, Tier: TierRestart,
					Placeholder: "autotaggerr@example.com",
					get:         func(c models.ConfigStruct) any { return c.SMTPFrom },
					set:         setString(func(c *models.ConfigStruct, v string) { c.SMTPFrom = v }, nil),
				},
				{
					Key: "autotaggerr_test_email", Label: "Test recipient", Type: TypeString, Tier: TierRestart,
					Help: "Address used when sending a test message.",
					get:  func(c models.ConfigStruct) any { return c.AutotaggerrTestEmail },
					set:  setString(func(c *models.ConfigStruct, v string) { c.AutotaggerrTestEmail = v }, nil),
				},
			},
		},
		{
			ID:          "diagnostics",
			Title:       "Diagnostics",
			Description: "How much the service says about what it is doing.",
			Fields: []Field{
				{
					Key: "autotaggerr_log_level", Label: "Log level", Type: TypeSelect, Tier: TierLive,
					Options: logLevels(),
					Help:    "trace and debug are loud enough to matter on a large scan.",
					get:     func(c models.ConfigStruct) any { return c.AutotaggerrLogLevel },
					set:     setString(func(c *models.ConfigStruct, v string) { c.AutotaggerrLogLevel = v }, validLogLevel),
				},
				{
					Key: "autotaggerr_environment", Label: "Environment", Type: TypeSelect, Tier: TierRestart,
					Options: []string{"prod", "test"},
					Help:    "test keeps Gin in debug mode; prod is what you want in normal use.",
					get:     func(c models.ConfigStruct) any { return c.AutotaggerrEnvironment },
					set:     setString(func(c *models.ConfigStruct, v string) { c.AutotaggerrEnvironment = v }, oneOf("prod", "test")),
				},
				// Both are TierRestart because the two runners resolve retention once, when
				// they are built. That is deliberate rather than a limitation: a pass that
				// reports "showing 500 of 3120" counted those rows against the cap it
				// started with, and moving it underneath a running job would make the pair
				// a lie.
				{
					Key: "autotaggerr_event_retention", Label: "Activity runs kept", Type: TypeInt, Tier: TierRestart,
					Help: "How far back the Activity feed goes. Counted in runs, so a run's stages are never pruned out from under it.",
					get:  func(c models.ConfigStruct) any { return c.AutotaggerrEventRetention },
					set:  setInt(func(c *models.ConfigStruct, v int) { c.AutotaggerrEventRetention = v }, intRange(1, 10000)),
				},
				{
					Key: "autotaggerr_event_detail_retention", Label: "Detail rows per activity", Type: TypeInt, Tier: TierRestart,
					Help: "Per-file detail kept for one activity. Rows past the cap are still counted, so the total stays honest — but this is what dominates database size on a large library.",
					get:  func(c models.ConfigStruct) any { return c.AutotaggerrEventDetailRetention },
					set:  setInt(func(c *models.ConfigStruct, v int) { c.AutotaggerrEventDetailRetention = v }, intRange(1, 100000)),
				},
			},
		},
		{
			ID:    "storage",
			Title: "Storage",
			Description: "Bootstrap configuration: it is read before anything else, so the running process " +
				"cannot change it underneath itself. Edit config.json and restart.",
			Fields: []Field{
				{
					Key: "database.type", Label: "Database type", Type: TypeString, Tier: TierReadOnly,
					get: func(c models.ConfigStruct) any { return c.Database.Type },
				},
				{
					Key: "database.dsn", Label: "Database DSN", Type: TypeString, Tier: TierReadOnly,
					Help: "A file path for sqlite; a connection string otherwise.",
					get:  func(c models.ConfigStruct) any { return c.Database.DSN },
				},
				{
					Key: "private_key", Label: "Session signing key", Type: TypeSecret, Tier: TierReadOnly,
					Help: "Signs session tokens. Generated on first run; replacing it signs everyone out.",
					get:  func(c models.ConfigStruct) any { return c.PrivateKey },
				},
			},
		},
	}
}

// View is the settings surface plus the current values, as the API returns it.
//
// It used to carry a second list — the config keys that had moved into the database
// and were edited on the Libraries and Tagger profiles pages — because those keys
// were still sitting in config.json, and a page that silently omitted them invited
// someone to edit the file and wonder why nothing changed. The keys are gone from the
// file now, so there is nothing left to explain away: every key config.json holds is
// a field below.
type View struct {
	Sections []ViewSection `json:"sections"`
}

type ViewSection struct {
	ID          string      `json:"id"`
	Title       string      `json:"title"`
	Description string      `json:"description,omitempty"`
	Fields      []ViewField `json:"fields"`
}

// ViewField is a Field with its value resolved. A secret never carries its value —
// only whether one is set — so reading the page cannot leak a password, and an
// unchanged secret is expressed by omitting the key on save.
type ViewField struct {
	Field
	Value     any  `json:"value,omitempty"`
	SecretSet bool `json:"secret_set,omitempty"`
	Editable  bool `json:"editable"`
}

// Describe resolves the whole surface against a config.
func Describe(cfg models.ConfigStruct) View {
	sections := Sections()
	out := View{Sections: make([]ViewSection, 0, len(sections))}

	for _, section := range sections {
		viewSection := ViewSection{
			ID:          section.ID,
			Title:       section.Title,
			Description: section.Description,
			Fields:      make([]ViewField, 0, len(section.Fields)),
		}
		for _, field := range section.Fields {
			viewField := ViewField{Field: field, Editable: field.set != nil}
			if field.Type == TypeSecret {
				secret, _ := field.get(cfg).(string)
				viewField.SecretSet = strings.TrimSpace(secret) != ""
			} else {
				viewField.Value = field.get(cfg)
			}
			viewSection.Fields = append(viewSection.Fields, viewField)
		}
		out.Sections = append(out.Sections, viewSection)
	}
	return out
}

// Apply validates a set of key → value edits and applies them to a copy of cfg. It
// is all-or-nothing: a rejected value leaves the returned config untouched, because
// a half-saved settings page is a state nobody can reason about. The returned keys
// are the ones that actually changed, in a stable order.
func Apply(cfg models.ConfigStruct, values map[string]json.RawMessage) (models.ConfigStruct, []string, error) {
	fields := map[string]Field{}
	for _, section := range Sections() {
		for _, field := range section.Fields {
			fields[field.Key] = field
		}
	}

	updated := cfg
	for _, key := range sortedKeys(values) {
		field, ok := fields[key]
		if !ok {
			return cfg, nil, fmt.Errorf("%q is not a setting", key)
		}
		if field.set == nil {
			return cfg, nil, fmt.Errorf("%s cannot be changed here", field.Label)
		}
		if err := field.set(&updated, values[key]); err != nil {
			return cfg, nil, fmt.Errorf("%s: %w", field.Label, err)
		}
	}

	changed := []string{}
	for _, key := range sortedKeys(values) {
		field := fields[key]
		if !sameValue(field.get(cfg), field.get(updated)) {
			changed = append(changed, key)
		}
	}
	return updated, changed, nil
}

// LiveKeys reports which of the changed keys the running process can adopt without a
// restart, and which have to wait for one. Read-only keys never reach here — Apply
// rejects them — so every key falls into exactly one of the two.
//
// Both slices are non-nil so the JSON carries [] rather than null: the page reads
// their length, and a null there is a runtime error rather than an empty list.
func LiveKeys(changed []string) (live, deferred []string) {
	live, deferred = []string{}, []string{}
	tiers := map[string]string{}
	for _, section := range Sections() {
		for _, field := range section.Fields {
			tiers[field.Key] = field.Tier
		}
	}
	for _, key := range changed {
		if tiers[key] == TierLive {
			live = append(live, key)
		} else {
			deferred = append(deferred, key)
		}
	}
	return live, deferred
}

func sortedKeys(values map[string]json.RawMessage) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func sameValue(a, b any) bool { return fmt.Sprintf("%v", a) == fmt.Sprintf("%v", b) }

// --- setters -----------------------------------------------------------------
//
// Each returns a set func that decodes the raw JSON into the right Go type, runs the
// field's validator, and only then writes. Decoding first means a wrong type is
// reported as a wrong type rather than as a validation failure.

func setString(assign func(*models.ConfigStruct, string), validate func(string) error) func(*models.ConfigStruct, json.RawMessage) error {
	return func(cfg *models.ConfigStruct, raw json.RawMessage) error {
		var value string
		if err := json.Unmarshal(raw, &value); err != nil {
			return fmt.Errorf("expected text")
		}
		value = strings.TrimSpace(value)
		if validate != nil {
			if err := validate(value); err != nil {
				return err
			}
		}
		assign(cfg, value)
		return nil
	}
}

func setInt(assign func(*models.ConfigStruct, int), validate func(int) error) func(*models.ConfigStruct, json.RawMessage) error {
	return func(cfg *models.ConfigStruct, raw json.RawMessage) error {
		var value int
		if err := json.Unmarshal(raw, &value); err != nil {
			return fmt.Errorf("expected a whole number")
		}
		if validate != nil {
			if err := validate(value); err != nil {
				return err
			}
		}
		assign(cfg, value)
		return nil
	}
}

func setBool(assign func(*models.ConfigStruct, bool)) func(*models.ConfigStruct, json.RawMessage) error {
	return func(cfg *models.ConfigStruct, raw json.RawMessage) error {
		var value bool
		if err := json.Unmarshal(raw, &value); err != nil {
			return fmt.Errorf("expected true or false")
		}
		assign(cfg, value)
		return nil
	}
}

// --- validators ---------------------------------------------------------------

func required(value string) error {
	if value == "" {
		return fmt.Errorf("cannot be empty")
	}
	return nil
}

func intRange(low, high int) func(int) error {
	return func(value int) error {
		if value < low || value > high {
			return fmt.Errorf("must be between %d and %d", low, high)
		}
		return nil
	}
}

func oneOf(allowed ...string) func(string) error {
	return func(value string) error {
		for _, candidate := range allowed {
			if value == candidate {
				return nil
			}
		}
		return fmt.Errorf("must be one of %s", strings.Join(allowed, ", "))
	}
}

func optionalURL(value string) error {
	if value == "" {
		return nil
	}
	if !strings.HasPrefix(value, "http://") && !strings.HasPrefix(value, "https://") {
		return fmt.Errorf("must start with http:// or https://")
	}
	return nil
}

// validTimezone accepts an empty value (meaning "the host's zone") and otherwise
// requires a name the Go runtime can actually load, since a name it cannot load
// would silently leave every schedule on the old zone.
func validTimezone(value string) error {
	if value == "" {
		return nil
	}
	if _, err := time.LoadLocation(value); err != nil {
		return fmt.Errorf("not a timezone this system knows (try Europe/Oslo)")
	}
	return nil
}

// validCron parses the expression with the same trigger the scheduler uses, so an
// expression accepted here cannot fail when it is scheduled.
func validCron(value string) error {
	if strings.TrimSpace(value) == "" {
		return fmt.Errorf("cannot be empty")
	}
	if _, err := chrono.CreateCronTrigger(value, time.Local); err != nil {
		return fmt.Errorf("not a valid schedule — six fields, starting with seconds (e.g. 0 0 3 * * *)")
	}
	return nil
}

func validLogLevel(value string) error {
	if _, err := logrus.ParseLevel(value); err != nil {
		return fmt.Errorf("must be one of %s", strings.Join(logLevels(), ", "))
	}
	return nil
}

func logLevels() []string {
	levels := make([]string, 0, len(logrus.AllLevels))
	for _, level := range logrus.AllLevels {
		levels = append(levels, level.String())
	}
	return levels
}
