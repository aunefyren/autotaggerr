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
under **Data sources** — the same split that moved managers, libraries and tagger profiles off this
page entirely (see [below](#every-key-on-this-page-is-a-key-the-process-reads)).

Naming a section after the current implementation costs twice: the second source makes the name
wrong, and someone running none reads a section about a service they do not use as irrelevant when
it in fact governs them. This is the same discipline
[mirror.md](mirror.md#the-three-verbs) applies to *mirror* — the package name, never a word the UI
says.

## Tiers: when an edit takes effect

| Tier | Meaning | Examples |
|---|---|---|
| `live` | Re-applied to the running process on save | log level, all three cron schedules, scan concurrency, the mirror switch, migration review flags |
| `restart` | Written to `config.json` now, read at the next start | port, instance name, external URL, timezone, SMTP, environment, Activity retention |
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
- **Scan concurrency**, through `process.Runner.SetConcurrency`. The worker count is an atomic because
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

## Every key on this page is a key the process reads

The page used to end with a **Managed elsewhere** block: config keys that had moved into the
database, listed with a link to the page that owned them because the keys were still sitting in
`config.json` and a page that silently dropped them invited someone to edit the file and wonder why
nothing changed.

That block is gone, and so are the keys it described — the eight tag-writing flags (now per
**Tagger profile**) and `autotaggerr_libraries` (now a row per folder on **Libraries**). They went
the way the three `lidarr_*` keys went before them: a key that exists, is documented and does
nothing is a trap whether or not a page explains it, and explaining it is strictly worse than not
having it. `files.SaveConfig` round-trips the struct on every start, so a file written by an older
version is cleaned of them the first time the new binary boots.

`plex_base_url` and `plex_token` left the same block in the opposite direction. Plex is not a
manager and never had a field on that page, while `main.go` reads both at every start — being told
that editing the file "changes nothing" was the opposite of true for them. They are ordinary
`restart`-tier fields under **Plex**.

`TestEveryConfigKeyHasAHome` (`settings/schema_test.go`) is what keeps the claim in this heading
true: it walks `models.ConfigStruct` by reflection and fails for any JSON key that is not a field in
`Sections()`. There is no second answer to give it now.

## Nothing is started by starting

Two keys used to run a verb at boot — `autotaggerr_process_on_start_up` (a full scan) and
`autotaggerr_mirror_on_start_up` (a metadata refresh). Both are gone. They date from before there
was a UI: with no button to press, a restart was the only way to make something happen on demand,
and an unattended pass on every boot was the price. Both verbs now have a button and a schedule, so
a restart is a restart — which matters most for the case the keys were worst for, a container that
restarts on a health check and re-walks the whole library each time.

## Email, and the one action on this page

The **Email** section configures an SMTP server (`mail`, stdlib `net/smtp`). PLAIN auth is used
only when a username is set — a local relay on 25 that wants no credentials is a normal deployment.

**Encryption is `smtp_tls`**, and its default `auto` reads the answer off the port: 465 is implicit
TLS (wrapped before the greeting), anything else is upgraded with STARTTLS when the server
advertises it and sent in clear when it does not. That is right for every hosted provider, and the
explicit modes exist for the self-hosted relay it is wrong for — `starttls` **refuses to send** to a
server that does not offer the upgrade, rather than silently falling back to plaintext; `none` never
upgrades, for a relay on localhost whose certificate nobody issued; `implicit` forces SMTPS on a
non-standard port. An empty or unrecognised value resolves to `auto`, so a `config.json` written
before the setting existed keeps behaving exactly as it did.

**A test instance never mails anyone but the test recipient.** When `autotaggerr_environment` is
`test`, `mail.Send` rewrites the recipients of *every* message to `autotaggerr_test_email` — the
envelope and the `To:` header both — and logs that it did. There is no exception and no override:
`SendTest`'s explicit address is ignored too, and the address it reports back is the one actually
delivered to, so the page cannot claim a delivery that did not happen. With no test recipient set
there is nowhere safe to send, so the send is refused rather than falling back to the real address.
The rule lives in `Send` rather than in its callers precisely because a caller is a place to forget
it, and a test instance is usually pointed at a copy of the real database — the same users, the same
addresses.

`POST /settings/email/test` (admin, like everything else here) sends one message to
`autotaggerr_test_email`, or to an address in the request body. It is the section's only action and
the only caller of `mail.Send` today: nothing in Autotaggerr sends mail on its own yet, so these
settings exist to be ready for something that does — password reset being the obvious candidate.

Two properties are deliberate. The test sends through the **stored** configuration, not the page's
pending edits, and the button says so, because a success against the host you just replaced is
worse than no test at all. And the SMTP server's own refusal is returned verbatim — `535
authentication failed` is the answer the admin came for, and summarising it away leaves them with
nothing to act on.

## Tests

- `settings/schema_test.go` — the surface (no secret values, no read-only field claiming to be
  editable, unique keys) and validation, including the all-or-nothing property and the
  `autotaggerr_mirror_disabled` inversion.
- `settings/apply_test.go` — scheduling, rescheduling, cancelling a disabled job, the live effects,
  and the full save path against a temp `config.json` (`files.SetConfigPaths` is the seam).
- `routers/settings_test.go` — the admin gate from both sides, 401 vs 403, that the response never
  contains a secret, and that a test email on a half-configured instance names what is missing.
- `mail/mail_test.go` — the config checks, the message format (CRLF, a Date header, no header
  injection through the subject), the test-environment redirect (including that the override is
  ignored and that a missing sink refuses the send) and how `smtp_tls` resolves against the port,
  with the transport stubbed.
- `mail/transport_test.go` — the transport itself against a scripted in-process SMTP server: with
  and without auth, each place the server can say no, and each TLS mode. The fake advertises
  STARTTLS but speaks plaintext, so whether the client attempted the upgrade is legible from
  whether the send succeeded — no certificate needed.

## Related

- [media-manager.md](media-manager.md) — the components (managers, data sources, libraries, tagger
  profiles) that own the settings this page deliberately does not.
- [scanning.md](scanning.md) — what the scan and refresh schedules drive.
- [style-guide.md](style-guide.md) — the setting row, the save bar and the inline note.
