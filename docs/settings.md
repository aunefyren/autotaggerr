# Feature: settings

Everything Autotaggerr can be told at startup — a CLI flag, a Docker environment variable, a key in
`config/config.json` — is editable at **/settings**, so running the container is not a prerequisite
for changing how it behaves.

## The one description

`settings/schema.go` is the single description of the surface: sections, and per field a key, a
label, help text, a type, a tier, and closures to read and write it on `models.ConfigStruct`.
`GET /settings` returns that description *with* the current values, and the SPA renders from it —
adding a setting is one entry in the table, not an entry plus a matching form control in TypeScript.

It is closures rather than struct tags over `ConfigStruct` for two reasons. Tags would have to carry
labels, help, options and validation anyway, and a field that is *deliberately* absent from the UI
has to be a decision someone wrote down rather than an omission nobody notices.

## Sections are named for what they govern, not who supplies the data

A setting on this page is **tenant-wide policy**. It holds whether you have configured zero, one or
several of the components it touches, which is why the naming avoids the name of any one of them:
the *Metadata refresh* and *Metadata migrations* sections say what they decide, not that MusicBrainz
is currently the only metadata source implemented.

The distinction that keeps this honest is *policy vs. connection*. "Hold merged releases for my
approval" is a decision about this library and belongs here, whatever answered the lookup. A base
URL, a credential or a rate limit is a property of one configured source and belongs on its own row
under **Data sources** — see [Managed elsewhere](#managed-elsewhere) for the same split applied to
managers, libraries and tagger profiles.

Naming a section after the current implementation costs twice: the second source makes the name
wrong, and someone running none reads a section about a service they do not use as irrelevant when
it in fact governs them. This is the same discipline
[mirror.md](mirror.md#the-three-verbs) applies to *mirror* — the package name, never a word the UI
says.

## Tiers: when an edit takes effect

| Tier | Meaning | Examples |
|---|---|---|
| `live` | Re-applied to the running process on save | log level, all three cron schedules, scan concurrency, the mirror switch, migration review flags |
| `restart` | Written to `config.json` now, read at the next start | port, instance name, external URL, timezone, SMTP, environment |
| `readonly` | Shown, never written | database type/DSN, version, session signing key |

The split is the honest part of the page. A setting the running process cannot adopt must not look
like one it can, so the tier is rendered next to the label as a badge and the save response says
exactly what was applied and what is waiting.

Two placements are worth explaining:

- **Timezone is `restart`, not `live`.** `time.Local` is a process-wide variable read by every
  goroutine that formats a time; rewriting it under a running scan is a data race, for a value that
  only matters at the next scheduled run anyway.
- **`database.*` is `readonly`** because it is bootstrap config: it is read before anything else
  exists, so the process cannot change it underneath itself.

## Applying without a restart

`settings.Runtime` (`settings/apply.go`) owns everything that can change live:

- **The recurring schedules.** Every cron job — scan, metadata refresh, health check — is described
  once as a `settings.CronJob` and installed by the runtime at startup (`Schedule`). Saving a
  schedule cancels that job's task and installs a new one; turning the mirror off cancels its task
  rather than only recording the preference. Owning the schedules here is what makes them editable at
  all: `main` used to schedule them inline, where nothing held the handle needed to reschedule.
- **The log level**, straight onto the logger.
- **Scan concurrency**, through `scan.Runner.SetConcurrency`. The worker count is an atomic because
  the API writes it from a request goroutine while a scan reads it; a scan already running keeps the
  pool it started with.

A `nil *Runtime` is usable and applies only the process-global effects, so a caller with no scheduler
(the one-shot `--file` mode, a test) needs no branch.

## Saving

`settings.Save` validates every edit, writes `config.json`, then applies. The order matters: applying
before persisting would leave a process running settings that a failed write means it will not have
after a restart — the one inconsistency a user cannot see and cannot explain. A failed write rolls the
in-memory config back.

It is **all-or-nothing**. One rejected value rejects the whole save, because a half-saved settings
page is a state nobody can reason about. Saves are serialised by a package mutex, so two admins
saving at once cannot lose one of the two edits in a read-modify-write.

The request carries only what the user touched (`{"values": {key: value}}`), which is also how an
untouched secret stays untouched.

## Secrets

A secret's value is never part of the settings response — the surface says only whether one is
**stored**. Showing one is a separate, deliberate `GET /settings/secrets/:key`, which logs who asked.
Read-only secrets are never revealed: the session signing key is not editable, so showing it serves
nothing the user can act on, while anything that could read it back could forge sessions.

Saving an empty string for a secret clears it; omitting the key leaves it alone.

## Access

Settings are the first routes to check the `role` column: `auth.RequireAdmin` (mounted after
`auth.Middleware`, and failing closed if it is ever mounted before). The nav entry is hidden from
non-admins and the route redirects them home, but the server is what enforces it — the page can
change the port, the schedules and the SMTP credentials, which is a different kind of power from
re-tagging an album.

Everything else in the API remains open to any authenticated user. That is deliberate, not an
oversight, and this doc is where to revisit it if per-role permissions ever grow.

## Managed elsewhere

Some `config.json` keys are no longer read at runtime: they seeded the database on first start and
are edited on their own page now — Lidarr/Plex credentials (**Managers**), the tag-writing flags
(**Tagger profiles**) and the library list (**Libraries**). They are listed at the bottom of the page
with a link to their owner, rather than omitted, because the keys are still in the file and a page
that silently drops them invites someone to edit the file and wonder why nothing changed.

## Tests

- `settings/schema_test.go` — the surface (no secret values, no read-only field claiming to be
  editable, unique keys) and validation, including the all-or-nothing property and the
  `autotaggerr_mirror_disabled` inversion.
- `settings/apply_test.go` — scheduling, rescheduling, cancelling a disabled job, the live effects,
  and the full save path against a temp `config.json` (`files.SetConfigPaths` is the seam).
- `routers/settings_test.go` — the admin gate from both sides, 401 vs 403, and that the response
  never contains a secret.

## Related

- [media-manager.md](media-manager.md) — the components (managers, data sources, libraries, tagger
  profiles) that own the settings this page deliberately does not.
- [scanning.md](scanning.md) — what the scan and refresh schedules drive.
- [style-guide.md](style-guide.md) — the setting row, the save bar and the inline note.
