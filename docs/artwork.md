# Refresh artwork

Autotaggerr fetches album covers and artist images into a local cache ahead of the pages that show
them, so a discography paints from disk instead of downloading a hundred thumbnails while someone
waits.

## The problem this solves

Every other cache Autotaggerr keeps is filled by a scheduled pass ([mirror.md](mirror.md)). Artwork
was filled entirely by whoever opened the page: an artist with an eighty-album discography fired
eighty Cover Art Archive requests at roughly two per second and painted its rows over the following
forty seconds.

It recurred, too. "There is no cover for this MBID" is remembered for **seven days**, and most of a
followed artist's back catalogue has no cover — so a large artist page re-paid a big share of that
cost every week, to re-learn the same nothing.

## Why this is its own verb

It was built once as a fifth phase of the metadata refresh. That was wrong on three counts, and the
reasoning is worth keeping because the coupling looks cheap from the outside:

- **The component model already draws the line.** Artwork providers are their own kind of data
  source, configured in their own section of /data-sources, and both that page and /libraries say so
  in prose. Attaching artwork to the verb named after the *other* kind of source works against a
  distinction the product already makes.
- **The verb has one name for a reason.** *Refresh metadata* accumulated three names before
  [mirror.md](mirror.md#one-name-two-forms) fixed them. Making it mean "refresh metadata and
  download a few hundred megabytes of images" is the same quiet scope-widening.
- **The counters do not fit.** A mirror pass's `Fetched` is what cost a **MusicBrainz** rate-limit
  slot, and the reading that matters — *mostly `Fetched` means the TTLs are shorter than the
  schedule* — is about that budget. Images come from the artwork hosts' own throttle, and their
  negative entries expire weekly by design, so folding them in left `Fetched` permanently large and
  meaning nothing.

Keeping artwork inside the pass required six exceptions: holding it out of three counters, counting
errors in two places, a `Planned` field because the scope could not predict the unit count, rewriting
the progress total mid-pass, carving `Force` out, and hiding the stats conditionally. Six exceptions
inside one pass is two verbs.

### *Refresh* is the same word on purpose

This verb honours a TTL, skips what is fresh, and re-reads what expired — exactly what *Refresh
metadata* means. Two operations that behave identically get the same word, and the naming rule from
[mirror.md](mirror.md#one-name-two-forms) applies unchanged:

- **A control says the verb**: *Refresh artwork*.
- **A record says the noun**: *Artwork refresh* — event rows, log lines, job titles. A pass that
  ignored the cache is a **Full artwork refresh**, the only variant that reads differently because it
  is the only one that behaved differently.

## What it warms

| Entity | Image | Size |
| --- | --- | --- |
| artist | portrait (`thumb`) | — |
| artist | backdrop (`background`) | — |
| release-group | front cover | 250 |

**250 is the size a *list* asks for**, and a list is where the cost is: the discography table and the
collection rows render covers at 250 (`components/Artwork.tsx` doubles a 26 px tile and floors at
250), and one page can ask for a hundred of them. The 500 px hero on the album page is deliberately
left on demand — it is one image on a page opened one album at a time, so warming it would double
this pass and the disk it uses to make an already-fast page marginally faster.

Artist images have **no size dimension at all** ([mirror.md](mirror.md#artwork)): fanart.tv serves
whatever it has, so one fetch serves the 24 px row, the 96 px header and the 1200 px backdrop alike.

Release-groups with a confirmed deletion are excluded. Their ID resolves nowhere, so asking about
them buys a guaranteed 404 every night — the same reason the metadata mirror skips them.

### A provider that cannot deliver is never asked

`components.CanServeCovers` / `CanServeArtistImages` gate every pass with the same two predicates
`/artwork-capabilities` reports to the UI.

This is not an optimisation. `modules.GetArtwork` turns a switched-off provider into `ErrNoArtwork`,
and `ErrNoArtwork` is **remembered** — so a pass that asked anyway would record "this album has no
cover" for every album in the collection, and the UI would keep showing monogram tiles for a week
after the source was enabled.

## New rows fetch their own artwork

The scheduled pass is a backstop. The main path is that **artwork is fetched at the moment the
collection gains the row**, so an artist added at 14:00 does not wait until 05:00 for their covers.

*Add artist*, a Lidarr sync and a discography sync are the same event wearing three hats — the
collection gained a row it did not have — so the hook sits at the two places rows are created
(`collection.upsertArtist`, `collection.upsertReleaseGroup`) plus `collection.AddArtist` for the
manual path, rather than on the three verbs. Hooking verbs would have been three integration points
that can drift, and a fourth case that silently would not warm.

Four properties keep it safe, and each is structural rather than a rule to remember:

- **It enqueues, never fetches.** *Add artist* returns immediately; the work happens on the runner's
  own goroutine.
- **It is silent on failure.** `collection.ArtworkWarmer` returns no error at all, so a cover that
  will not download cannot make an *Add artist* or a sync report a failure. Artwork is decorative and
  its failures belong in the artwork event.
- **It never forces.** A row created seconds ago cannot have a stale cached copy.
- **It fires on create only.** Both upserts run for every row on every rebuild, overwhelmingly as
  updates; firing on those would hand the whole collection to the warmer several times a day.

An artist added by hand warms only their portrait and backdrop, because at that moment they have no
release-groups. Those arrive later through the discography sync, which notifies from
`upsertReleaseGroup` as it creates them — the two hooks compose rather than one knowing about the
other.

**This is not the cascade [mirror.md](mirror.md#the-three-verbs) forbids.** That rule exists because
a button about reading must not rewrite the user's audio files. Artwork writes only to
`config/artwork/`, asynchronously, and nothing the user pressed gets slower or does more than it says.

## The queue

One runner serves two very differently shaped requests: a collection-wide pass that may run for an
hour, and the handful of images a newly created artist needs right now.

**Targeted work jumps ahead.** Pending targets are checked *between images* of a running pass and
drained first — not merely picked up when it ends. Without that, adding an artist while a first pass
grinds through three thousand cold covers puts their twelve images twenty minutes into the queue,
which is exactly the wait this exists to remove. It is the same reasoning `process.Runner`'s queue
uses when it puts file-writing work ahead of pending metadata work: a long background job must not
starve a short one with a user behind it.

**Notifications coalesce.** A rebuild creating two hundred release-groups notifies two hundred times;
each merges into one pending target set rather than queueing a job, so the worker sees one piece of
work with two hundred MBIDs in it. That coalescing lives in the runner because otherwise every hook
site would need its own copy of it.

### It does not yield to file-writing work

The metadata mirror pauses while a scan runs, because both spend the same one-request-per-second
MusicBrainz budget and the scan is the one with a user attached. Nothing here touches that budget:
images come from the artwork hosts' own throttle (~2 req/s per host), and the only disk this writes
is `config/artwork/`. Yielding would buy a scan nothing measurable, and would make the case this was
built for — an artist added mid-scan whose covers someone is about to want — the slowest one.

For the same reason it is **not** enqueued on `process.Runner`'s queue.

## Forcing

"Disregard the cache" covers both halves of the artwork cache, at wildly different costs:

| Half | What forcing does | Cost |
| --- | --- | --- |
| positive entries | re-download images already on disk | a transfer each — hundreds of megabytes |
| negative entries | re-ask about the ones a provider said do not exist | one request each |

Force does both, which is the literal reading and what force means for metadata. The **negative half
is usually the point**: *"fanart.tv had nothing for this artist last week"* is exactly what someone
wants re-checked after art gets uploaded. The confirm dialog's estimate is nevertheless driven by the
image count, because that is what dominates the time.

Expiring rather than deleting (`modules.ArtworkExpire`), so the stale copy stays on disk as the
fallback if the re-fetch then fails — a forced pass during a provider outage must not empty a page
that had covers on it yesterday.

**Forcing is reachable from exactly one place**: the button on /data-sources, behind
`ForceArtworkDialog`. Nothing scheduled and nothing automatic forces — the same rule
[mirror.md](mirror.md#forcing-is-always-deliberate) holds for metadata. The two rules that make it
deliberate carry over: a forced pass confirms and an ordinary one does not, and the checkbox resets
once a pass starts so one considered decision does not become a setting.

`ForceArtworkDialog` is a sibling of `ForceRefreshDialog` rather than a reuse of it. The metadata
dialog measures itself in rate-limited *requests*, which is honest for JSON lookups and useless here;
a dialog that said "requests" for a few hundred megabytes of transfer would understate the cost by
orders of magnitude, which is the one failure a confirm step cannot have.

## What a pass records

The scheduled pass **always** records an `artwork_refresh` event. A targeted warm records one **only
if it actually fetched an image** — adding twenty artists should not put twenty rows in the feed, and
a warm that found everything already cached did nothing worth reading about. Real work and real
failures still surface.

| Stat | Meaning |
| --- | --- |
| Images checked | every image the pass considered |
| Fetched | cost an upstream request and got an image |
| Already cached | on disk and current — no request |
| **No image available** | the provider was asked and has none |
| Failed | a provider error; logged, skipped, the pass continues |

*No image available* is its own figure rather than a share of *Fetched*, because on a page about
artwork providers it is the number that explains a screen full of monogram tiles. It gets **no detail
rows** — one row per coverless album would be thousands of rows saying nothing happened. Failures do
get rows.

Errors never fail the pass: one image a provider cannot serve must not end a pass with a thousand
others to warm.

## Configuration

| Key | Default | Meaning |
| --- | --- | --- |
| `autotaggerr_artwork_disabled` | `false` | Turns the scheduled pass *and* the create hooks off. |
| `autotaggerr_artwork_cron_schedule` | `0 0 5 * * *` | When the backstop pass runs. |

`disabled` rather than `enabled` for the same reason the mirror key is phrased that way: a bool
absent from an existing `config.json` decodes as `false`, and `false` has to mean the default
behaviour.

**The hour is not load-bearing.** 05:00 leaves the metadata refresh (03:00) two hours to find albums
added upstream, but since row creation warms its own artwork this pass is a backstop for expiry
rather than the thing that catches up with the mirror.

Turning artwork off entirely is done by disabling its data sources on /data-sources — the config keys
govern *when Autotaggerr fetches ahead*, not whether artwork exists.

## API

| Route | Purpose |
| --- | --- |
| `GET /api/v1/artwork/status` | Current/last pass, cache coverage, and whether the providers can serve anything. |
| `POST /api/v1/artwork/refresh` | Queue a collection-wide pass. `?force=true` ignores cached copies. |
| `POST /api/v1/artwork/cancel` | Stop the running pass at the next image. |

`refresh` returns `202` — a first pass over a cold collection runs for the better part of an hour.
Cancelling is safe at any point because a pass keeps no cursor: the next one resumes by skipping
whatever is already cached.

The status payload carries `covers_enabled` / `artist_enabled` as well as the counters. Without them
the UI cannot tell *"nothing was fetched because everything is current"* from *"nothing was fetched
because no provider is configured"* — opposite situations that look identical in a row of zeroes.

## UI

The control lives in the **artwork section of /data-sources**, next to the providers it uses, rather
than on a page of its own. It is genuinely niche — new rows fetch their own images and the schedule
covers expiry — so it is for filling a cold cache on an existing install, which is why it is a
secondary button and a quiet status strip rather than a page.

The strip states the two cache figures (*images cached*, *with no artwork*) between passes, because
those are meaningful when nothing is running; a progress bar replaces them while a pass is in flight.
The button is disabled with an explanation when no provider can serve an image, since a pass would
otherwise start and fetch nothing.
