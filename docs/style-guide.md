# Autotaggerr style guide

The single source of truth for the web UI's look and feel. **Every UI change must consult this
guide and either follow it or deliberately reshape it — updating the guide in the same change.**
Reuse the tokens and components below; do not introduce one-off colors, spacings, or type sizes.
(See `docs/development.md` → "UI follows the style guide".)

Tokens live as CSS custom properties in the SPA's `theme.css`; the canonical values are in this
document. When they diverge, this document wins until reconciled.

---

## Thesis & principles

Autotaggerr is a **control surface for music metadata** for self-hosters who already live in
Lidarr / Plex / Jellyfin. It should feel like a precise instrument — an audio console crossed with a
code editor — not a generic admin template.

- **Identifiers are content.** MusicBrainz IDs, ISRCs, catalog numbers, file paths, and tag keys are
  the material of the app. They are set in **monospace** and treated as first-class — this is the
  primary source of the UI's character.
- **The diff is the story.** The app's job is changing tags. A git-style **old → new** color
  language (removed = red, added = green) is a recurring, subject-rooted motif, distinct from the
  violet brand accent.
- **Dense but legible.** Compact rows and controls so large tables (thousands of items) show a lot at
  once. Density comes from tight spacing and small type, never from cramped hit targets or low contrast.
- **Quiet chrome, loud data.** Surfaces, borders, and nav stay understated; color is spent on status
  and the accent, so the data and its state read instantly.
- **Restraint.** One accent, a disciplined neutral scale, minimal shadow, purposeful motion. No
  gradients-as-decoration, no ornament that doesn't encode meaning. The artist backdrop is the one
  named exception, and it is fenced (see *Artwork*).
- **Artwork is structure, not decoration.** A cover is what makes a hundred rows of releases
  scannable, so it is treated as a column with a fixed square — never as a flourish, never allowed
  to shift a layout or delay a render. Covers earn their place on browsing surfaces (collection,
  artist, album) and stay off working surfaces (items, scans, settings).
- **Direct manipulation beats a mode switch.** If a user can express something by ticking the thing
  itself, do not also give them a control that says which *kind* of thing they are about to tick.
  Two controls for one intention means one of them is always the weaker, and the weaker one is what
  people click first. State the result in words instead (see *Derived summary line*).

Dark theme only.

---

## Color

Neutrals carry a subtle violet undertone so they belong to the accent family (a "violet ink" dark),
rather than being pure gray. All values are final; use the token, not the hex.

### Surfaces & structure
```css
--bg:            #0D0B14; /* app background (deepest) */
--surface-1:     #14121C; /* panels, cards, table body */
--surface-2:     #1C1926; /* raised: inputs, table header, hover fill */
--surface-3:     #232030; /* overlays: menus, popovers, modals */
--border:        #2A2740; /* hairline dividers, control borders */
--border-strong: #3A3557; /* emphasized borders, focus outlines base */
```

### Text
```css
--text:      #E7E4F0; /* primary (≈14:1 on --bg) */
--text-muted:#A29DB8; /* secondary / descriptions (≈7:1) */
--text-dim:  #726C8C; /* labels, meta, placeholders — non-essential only */
--on-accent: #FFFFFF; /* text/icons on a solid accent surface (≈7.5:1) */
```

### Accent (violet / indigo)
```css
--accent:        #7B61FF; /* primary: solid buttons, active nav, selection */
--accent-hover:  #8F79FF;
--accent-active: #6A4DE8;
--accent-text:   #B7A6FF; /* accent as TEXT/links on dark (use this, not --accent, for text) */
--accent-subtle: #1A1530; /* solid tint fill: selected row, active chip bg */
--accent-ring:   rgba(123, 97, 255, 0.40); /* focus ring */
```
Rule: `--accent` is for **surfaces** (with `--on-accent` text); `--accent-text` is for **text/icons**
on dark. Never set small text in `--accent` (only ~2.5:1 on `--bg`).

### Status (semantic)
```css
--success: #3FB950; --success-text: #56D364; --success-bg: rgba(63,185,80,0.14);
--warning: #D29922; --warning-text: #E3B341; --warning-bg: rgba(210,153,34,0.14);
--danger:  #F85149; --danger-text:  #F0776F; --danger-bg:  rgba(248,81,73,0.14);
--info:    #58A6FF; --info-text:    #79C0FF; --info-bg:    rgba(88,166,255,0.14);
```

### Diff (the signature color language)
```css
--diff-add-bg:    rgba(63,185,80,0.14);  --diff-add-text:    #56D364; /* new / desired value */
--diff-remove-bg: rgba(248,81,73,0.12);  --diff-remove-text: #F0776F; /* current / replaced value (struck) */
```

Status → color mapping (used by pills, item rows, health):
| State | Color |
|---|---|
| ok / tagged / healthy | success |
| changed / tags written | accent |
| needs attention / drift | warning |
| error / unhealthy | danger |
| scanning / in progress | info |
| unmatched / disabled / skipped | text-dim on surface-2 |
| pinned (manual) | accent |

---

## Typography

Two faces from one superfamily, so UI and data feel cohesive and engineered:

- **IBM Plex Sans** — UI and body. Characterful humanist-grotesque, efficient small, not the default
  Inter. Headings use 600 with tight tracking.
- **IBM Plex Mono** — every identifier and tag value (MB IDs, ISRCs, paths, tag keys, numeric table
  cells). This is the character-carrying face and the signature.

Bundle both as subset **woff2**, self-hosted (no CDN — the SPA is embedded). Fallbacks:
```css
--font-sans: "IBM Plex Sans", system-ui, -apple-system, "Segoe UI", sans-serif;
--font-mono: "IBM Plex Mono", ui-monospace, "SF Mono", "Cascadia Code", monospace;
```

### Scale (compact base = 13px)
```css
--text-xs:  11px; /* badges, table meta, eyebrows (uppercase, letter-spacing .06em) */
--text-sm:  12px; /* secondary text, dense table cells */
--text-base:13px; /* DEFAULT UI/body */
--text-md:  14px; /* inputs, emphasized body */
--text-lg:  16px; /* card / section titles */
--text-xl:  20px; /* page titles */
--text-2xl: 28px; /* dashboard hero numbers (rare) */
```
Weights: 400 regular · 500 medium (UI emphasis, table headers) · 600 semibold (headings, buttons).
Line-height: 1.35 data/UI, 1.55 prose. Eyebrows/section labels: `--text-xs`, uppercase,
`letter-spacing: .06em`, `--text-dim`.

---

## Spacing, radius, elevation, motion

**An undeclared token does not fall back.** `gap: var(--space-4)` against a missing property is not
8px and not an error — it is `gap: normal`, i.e. zero; `padding: var(--space-6)` is no padding at
all. This scale was canonical here and absent from `theme.css` for a long time, so the three rules
that used it (`.entity-head`, `.pager`, `.runbar`) laid out flush against their own borders and read
as "cramped" with no rule to blame. Check a token exists in `:root` before reaching for it.

```css
/* 4px base scale */
--space-1:2px; --space-2:4px; --space-3:6px; --space-4:8px; --space-5:12px;
--space-6:16px; --space-7:20px; --space-8:24px; --space-9:32px; --space-10:40px; --space-11:48px;

/* radius — utilitarian: small, never pill-shaped cards */
--radius-sm:4px; --radius:6px; --radius-lg:10px; --radius-pill:999px;

/* elevation — on dark, prefer border + surface step over heavy shadow */
--shadow-1: 0 1px 2px rgba(0,0,0,.40);
--shadow-2: 0 8px 28px rgba(0,0,0,.55); /* menus, modals */

/* motion — quick and utilitarian; honor prefers-reduced-motion */
--dur-fast:120ms; --dur:160ms; --dur-slow:220ms; --ease:cubic-bezier(.2,.6,.2,1);
```

### Artwork sizes
```
26px  table row thumb (release rows)      --radius-sm
24px  collection row artist avatar        --radius-sm
96px  artist page header                  --radius
120px album page header                   --radius
```
Requested pixel size is separate from rendered size (a 26px thumb fetches the 250px cover), and the
fetched size is part of the cache key, so a thumbnail and a header image never evict each other.

### Density metrics
- Table row height: **34px** (compact). A future "comfortable" toggle uses 40px.
- Control height: **32px** default, **28px** small, **38px** large.
- Input padding: `6px 10px`. Button padding: `0 12px` (small `0 10px`).
- Page gutter: `--space-8` (24px). Card padding: `--space-6` (16px).
- Focus: `box-shadow: 0 0 0 3px var(--accent-ring)` + `border-color: var(--accent)`. Always visible.

---

## Components

Concise specs; the SPA implements each as one reusable component. States listed are required.

- **Button** — height/padding per density. Variants: **primary** (`--accent` bg, `--on-accent`
  text), **secondary** (`--surface-2` bg, `--border` border, `--text`), **ghost** (transparent →
  `--surface-2` on hover), **danger** (`--danger` bg). States: hover, active, focus-ring, disabled
  (50% opacity, no pointer), loading (spinner, keeps width). Semibold label, `--radius`.
  **Ghost + `--danger-text` label** is the destructive member of a row of ordinary ones — Remove in
  the Libraries table, Re-correlate beside the four verbs. A solid `btn-danger` in a row of ghosts
  reads as the row's primary action, which is the opposite of the point; the colour marks the
  consequence while the weight keeps it from being the thing the eye lands on first. Reserve it for
  actions that overwrite or discard, and pair it with a `ConfirmDialog` — the colour is a hint, not
  a confirmation.
- **Input / select / textarea** — `--surface-2` bg, `--border` border, `--text`, `--text-dim`
  placeholder, `--font-mono` when the value is an identifier/path. Focus ring. Invalid = `--danger`
  border + helper text in `--danger-text`.
- **Toggle / checkbox** — off = `--surface-2`/`--border`; on = `--accent`. 28px min target.
- **Table** — `--surface-1` body, `--surface-2` sticky header (`--text-muted`, medium, `--text-xs`
  uppercase). Rows separated by `--border` hairlines (no zebra). Hover = `--surface-2`. Selected =
  `--accent-subtle` fill + 2px `--accent` left border. Numeric/ID cells `--font-mono`. Right-align
  numbers. Cells carry `--space-4`/`--space-5` padding with the 34px row height as the *minimum*:
  `border-box` folds the padding into that height, so a single-line row is unchanged and only the
  cells that already wrapped — a stacked name/ID, a sentence — stop touching their hairlines. A
  sentence in a cell takes a measure (`max-width`) like any other prose; a table column is as wide
  as the widest thing in it, which is not a reason to set 11px text across 900px of it.
- **Status pill** — `--radius-pill`, `--text-xs`, medium, `10px` tall dot + label; color from the
  status map, on a `*-bg` tint with `*-text`. Disabled/unmatched uses `--surface-2`/`--text-dim`.
  **It is the width of its label, never of its container.** `inline-flex` is not enough inside a
  `.stack`: a flex column stretches its items, and a pill filling a table column stops reading as a
  status on the row and starts reading as a banner across it (`.stack > .pill` pins `align-self`).
- **ID chip** — `--font-mono`, `--text-xs`, `--surface-2` bg, `--border`, `--radius-sm`; click-to-copy
  MB IDs / ISRCs. Truncate with middle-ellipsis for UUIDs; full value on hover/copy.
- **Card / panel** — `--surface-1`, `1px --border`, `--radius-lg`, `--space-6` padding. Optional
  header row with title (`--text-lg`, 600) + actions.
- **Sidebar nav** — `--bg`, item height 34px, icon + label. Active = `--accent-subtle` fill,
  `--accent-text` label, 2px `--accent` left marker. Hover = `--surface-2`.
- **Tabs** — underline style; active tab `--text` + 2px `--accent` underline; others `--text-muted`.
- **Master/detail split** (`.rg-split`) — a two-pane grid for two-level data: the list of things
  on the left, the selected thing's contents on the right. Selection is an `--accent-subtle` fill plus
  a 2px `--accent` inset on the master row. Detail loads lazily on selection, never up front, when
  each item costs a rate-limited fetch. Stacks to one column below 900px.
  **Two jobs, two hit areas:** where a row is both selectable *and* has state, the checkbox owns the
  state and the row body owns the selection (`.edition-row` / `.edition-pick`). Ticking must never
  also re-select, or every state change triggers a fetch. The row body is a `<button>`, not a div, so
  the detail pane is reachable without a mouse.
- **Artwork** (`.artwork`, `Artwork.tsx`) — square, `object-fit: cover`, `--surface-2` behind it,
  `loading="lazy"`, fixed width/height always set. A missing image falls back to a **monogram tile**
  (`.artwork-fallback`): one or two initials, `--font-mono`, `--text-dim`, `--surface-2`, hairline
  border. The monogram is deliberately **neutral** — a per-artist hue would break the one-accent rule
  and turn a browsing aid into confetti. On a **header-sized artist tile** (≥64px) with no artist
  provider configured, the fallback carries a `title` saying so and where to fix it: artist images
  have one source and it needs a personal key, so a keyless install shows monograms forever with
  nothing on the page to explain them. It stays a hint on that one tile — never a notice, and never
  on the 24px row avatars, where the same sentence under fifty tiles is a nag and nobody is asking
  the question. Sources: Cover Art Archive for covers (no credential),
  fanart.tv for artist images (needs a key; absent means monograms, never an error state). Both are
  proxied and cached by the app, never hot-linked. Nothing waits on artwork.
- **Coverage meter** (`.coverage`, `CoverageBar.tsx`) — see *Signature* below.
- **Disk marker** (`.disk-marker`, `DiskMarker`) — `○` none, `◐` partial, `●` complete, coloured
  `--text-muted` / `--warning-text` / `--diff-add-text`. Always paired with a `title`; it is what
  keeps ownership from being conveyed by colour alone.
- **Entity header** (`.entity-head`) — how an artist or album page opens: artwork, an eyebrow of
  facts, the title, a coverage meter, a meta line, and the actions. It replaces a row of stat cards —
  five 28px hero numbers for one artist read as an admin dashboard, and the numbers are more useful
  as filter chips over the list they describe.
  **Backdrop exception** (`.entity-backdrop`): the artist header may carry a fanart.tv background
  image. This is the only ornamental image in the app, and it is fenced — `opacity` ≤ 0.18, a
  mandatory `::after` scrim so header text keeps its contrast whatever the image is, a
  `mask-image` fade so it never reads as a photo with a caption, and **nothing rendered at all** when
  there is no image (never an empty tinted band). Do not reuse this treatment elsewhere.
- **Run bar** (`.runbar`, `RunBar.tsx`) — the four verbs at whatever scope the surface has, plus the
  one status they share. A single serial job queue drains all four, so a single bar is their honest
  shape: one row of controls with one state, rather than four buttons that dim together for a reason
  stated in none of their labels. The state is the left half — the running job's **own title and
  stage**, in the words the Activity banner uses, with the progress meter (indeterminate outside the
  counted phase, per *Progress*); otherwise `Idle · 1,284 files indexed`, or `Nothing indexed yet`.
  It is a **link to Activity** while a job runs, since that is where the work is reported. Naming
  the job is the point: controls here dim for two different reasons — a job holds them, or nothing
  is indexed yet — and a disabled button looks identical either way. **Process is the only primary**; the other three are ghosts, in the
  documented cheapest-first order so the bar and the artist page's four read the same. Process keeps
  its label through a run (the bar says "Working", the button does not become it) — an action keeps
  its name through the flow.
  **What belongs beside it and what does not:** a verb that queues goes in the bar; an action that
  changes *what the surface holds* or only re-reads a manager, and queues nothing (Add artist, Sync
  from Lidarr) stays in the page head. That is a fact about the actions rather than about which
  manager a user happens to run, which is why it holds at both scopes.
  **Shared by the collection and one artist**, because the verbs are the same four at both scopes
  and the queue behind them is literally the same queue. Only `idle` differs — what there is to say
  when nothing is running is scope-specific (`1,284 files indexed` means nothing on one artist's
  page), so the caller owns that sentence and nothing else.
  On an artist, the bar also takes **Re-correlate** (after a `·`, ghost + `--danger-text`): it
  queues and rewrites tags, so it is a verb, and it stays *outside* the four because it is a repair.
  What is left in the header there is the artist's **state** — who manages it, whether it is
  followed, what following means — which is a different kind of thing from a verb and stopped being
  legible as one when eight controls shared a wrapping row.
  It replaced six buttons and a `·` in the collection head, with two competing primaries and every
  explanation in a `title`. A page-level toolbar earns a container when it has a state to state;
  without one, it is a row of buttons and belongs in the head.
- **Sortable header** (`.sortbtn`, `SortHeader`) — a `<button>` inside the `<th>`, with `aria-sort` on
  the `th` and a caret carrying direction visually (`•` when inactive). Clicking the active column
  flips direction; a new column starts at its own sensible default (year → descending). Numeric and
  date columns default descending, text ascending.
- **Table toolbar** (`.table-toolbar`, `TableToolbar`) — one free-text filter, the chips that narrow
  the same list, and a `first–last of total` count so "no matches" is distinguishable from "empty
  list". Sits above the table, outside its border.
  **It stays on screen when the filter matches nothing.** Hiding the controls with the rows is how
  someone gets stranded with no way to widen the filter that emptied the list; the empty state says
  "no match" rather than "nothing here", which are different facts.
  **A server-backed box debounces the fetch, not the input** (`useDebounced`, `hooks.ts`): the field
  stays immediate, and only the query waits, so typing does not become a request per keystroke and a
  list flickering through every intermediate answer. A client-side filter needs none of this.
  **Chips for a few options, a `<select>` for many.** Chips are the default because a count is read
  as a prelude to "show me which ones", but a dozen of them out-weigh the table they narrow — so a
  long list of mutually exclusive options is a select, with the counts on the option labels so the
  choice still states its own result. Only options that have happened are offered (plus the active
  one, even at zero, so a filter matching nothing can still be seen and undone).
- **Filter chip** (`.chip`, `FilterChip`) — a count that is also a control: label plus a mono number,
  `aria-pressed`, `--accent-subtle` fill when on (`--warning-bg` for drift-shaped filters). Disabled
  at zero unless already active. Counts on a browsing page should nearly always be chips — "3 partial"
  is only ever read as a prelude to "show me which three".
- **Grouped table sections** (`.group-section` / `.group-head`) — a collapsible section per real
  category, with its count and its own coverage meter in the header. Grouping must encode data (the
  MusicBrainz primary type), never visual chunking. Sections that are numerous and rarely the reason
  you opened the page start closed. One sort and one filter apply across every section, and each
  section **pages on its own** (`.group-foot`, see *Paging*) — a prolific artist has a handful of
  albums and three hundred singles, and it is only ever the one section that is long. The header's
  count stays the section's, never the page's: the pager below states what is on screen, and a count
  that followed the slice would disagree with the coverage meter beside it. The header's
  caret is `.twisty`; a *list* is never collapsed this way — the Activity feed tried it and the rows
  it hid were the ones worth reading (see *Run rail*).
- **Row disclosure** (`.detail-row` / `.detail-body`) — the paragraph a table row keeps folded away,
  opened underneath it as a `colSpan` row. It is *part of the row above*, not another row of the
  table, and has to look it: `--surface-2`, a 2px `--accent` inset gutter continuing down from the
  row it explains, `--space-5`/`--space-6` padding, and the hover fill pinned so the pointer never
  lights up two rows at once. Give it a class — inheriting the ordinary cell rule means
  `height: 34px` and no vertical padding, which opens a three-line explanation pressed against the
  hairlines on both sides. Prose measure is **68ch**, the same as page-head explanations; 80ch of
  12px text is a line the eye loses its place returning from. The toggle carries `aria-expanded`.
  It states what the row could not fit, never a longer version of the same sentence.
- **Browse state lives in the URL** (`useBrowse`) — query, sort key, direction, active filter, open
  sections, selected detail row, **page**. Written with `replace`, not `push`, so sorting a table is
  not a history entry. The reason is concrete: opening an album and coming back must not reset the
  list. A flag whose empty value is meaningful needs a sentinel (`closed=-` for "every section
  closed"), because an empty string reads as unset and springs the defaults back.
- **Paging** (`usePaging` + `Pager`, `browse.tsx`) — the page sits under the table, states
  `from–to of total`, and **renders nothing when everything fits on one page**: a control that
  cannot go anywhere is furniture, and the toolbar already says the count. Page one stays out of the
  URL, so a shared link never carries state meaning "nothing was changed".
  **Three zones, not a flex row** — `1fr auto 1fr`, each direction pinned to the edge it travels
  towards (with a `‹`/`›` arrow, `aria-hidden`, carrying that direction visually) and the position
  centred between them as one sentence. Flexing all of it left put both controls and a number within
  12px of each other under the table's bottom edge, and split one fact — the range and the page
  number — across opposite ends of the bar. No amount of gap fixes crowding while both controls
  share a corner. Below 560px the position drops to its own row and the two directions keep the
  edges.
  **Any narrowing resets to page one.** `useBrowse` drops `page` on every update except `setPage`,
  because filtering a list to twelve rows while on page four shows an empty table with no
  indication why — the rows are not missing, you are past the end of them.
  **Several lists on one surface page apart** (`usePaging(browse, total, size, key)`), keyed as
  `page-<key>` in the URL. The artist page's catalogue is four sections under one table header, and
  a single pager across them would put a page boundary in the middle of Singles while every group
  header's count and coverage meter went on describing a set that is not on screen. The reset rule
  applies to **every** list's page, not just the unkeyed one: one filter narrows all four sections
  at once, so a surviving `page-single=3` would strand one section past its own end. The in-section
  form is `compact` — same three zones, tighter, styled like the group header it closes, so a
  section reads as bracketed by its own chrome rather than as rows that ran out.
  **The hook is shared; the fetching deliberately is not.** A client-paged list (Collection: it
  holds every artist and sorts them in the browser) slices what it already has, because server
  paging would sort one page instead of the list. A server-paged one (Activity: it never holds the
  whole table) puts the same `offset` in the query. What is shared is the *position*, and `offset`
  is equally an array index and a query parameter.
- **Derived summary line** — where direct manipulation replaced a mode switch, one sentence states
  what the ticked boxes add up to, in the same vocabulary as the boxes ("Wanted: any edition, whole
  album" / "Wanted: 2 editions, 5 tracks"). The empty case says what the thing *is*, not what it is
  not ("Not wanted").
- **Visible defaults** — when the default state is "no rows stored", give it a real row with a real
  checkbox ("Any edition", "All tracks") rather than leaving it implied by absence. A default that
  has no control is a state the user can neither see nor return to. A master checkbox over a list is
  `indeterminate` for a subset — the honest third state — and unticking it drops the want rather than
  silently widening it.
- **Toggle button** — a control that *is* a state, not a command. **The label carries the state,
  not just the fill**: participle when on, verb when off — `Wanted`/`Want`, `Following`/`Follow`.
  On = `btn-primary` (accent fill); off = `btn-secondary`. Always set `aria-pressed`, and put the
  current state plus what a click does in the `title`. Two forms of *one* word is the point: it
  keeps the vocabulary rule below intact while making the state legible. (An earlier version fixed
  the label — a filled, accent-coloured "Follow" — and it was consistently read as an invitation to
  follow rather than as "you are following"; the fill alone cannot carry state.) Still avoid
  one-directional *action* phrases ("Add to wanted"): they describe a transition, not a state, and
  read as a different control once it flips. Controls that depend on a toggle being on stay
  **disabled, not hidden** — a vanishing control shifts the row and is harder to find than a dimmed
  one.
- **Derived state is never a toggle** — when a state is computed from something else (an album
  wanted because you follow the artist, or because Lidarr monitors it), render it as **state**: a
  `pill-off` pill naming the authority, plus a disabled toggle whose `title` says what governs it
  and where to change it. A toggle whose off direction silently does nothing is worse than a
  disabled one. If there is a way to take ownership of the state, give it its **own labelled
  action** ("Pin") rather than overloading the toggle — pressing an on-toggle and having it stay on
  while its meaning changes is not a state change anyone can predict. The control that explains a
  state must stay on the page **even when it cannot be used**: replacing it with a note hides the
  cause of the state it was explaining.
- **State must be readable without hover** — never leave it to the `title`. The toggle's own label
  does this job (see above); add a `pill-off` pill only for what the label cannot say, such as *why*
  a control is frozen (**Managed by Lidarr**). Do not do both for the same fact: a "Following" pill
  next to a "Following" button is one signal too many. One pill per *surface*, not per control —
  the artist header carries "Managed by Lidarr" once, and the Follow settings panel below it says
  the same thing in prose rather than repeating the pill.
- **Disabled means "not now"; absent means "not here."** A control the user could plausibly use in
  another state stays **disabled** with the reason in its `title` (Want on an album a manager owns —
  it is still the control that explains the state). A control that could *never* succeed on this
  surface is not rendered at all (**Pin** under a manager: pinning writes a want, and a manager-owned
  artist rejects every write, so a permanently dimmed button would be furniture). The test is whether
  the disabled state is explaining something; if it explains nothing, it is clutter.
- **A derived state still has a value, so show it** — a want with no stored rows behind it is not
  "nothing wanted"; it means *any release, whole album*, and the editor must open on that. Seed the
  editor from what the derived state means, not from the empty table behind it, or the page
  contradicts the row that linked to it.
- **Collection vocabulary** — one word per concept, used everywhere. **Wanted** is anything asked
  for but not owned; it is reached two ways: **Follow** an artist (auto-wants their studio albums
  and EPs, including future ones) or **Add to wanted** on a single album. An album wanted because
  of a follow is marked `auto`, so an automatic want never looks like a deliberate one. Editions
  read as a narrowing of an existing want ("any edition counts" -> "want this one"), never as a
  second, unrelated toggle. Never use "monitor" in the UI: it is the DB field name, and having two
  words for one idea is what made the earlier version unreadable. The default breadth is
  **"any edition"** — not "any release", which was the same idea in a second vocabulary; what
  MusicBrainz calls a *release* the UI calls an **edition**, everywhere.
- **Fielded search** (`.field-grid`) — a responsive grid of short, labelled inputs (eyebrow label
  above, `auto-fit` at 150px) that are read as **one** query, not a sequence of steps. Use it when
  free text cannot separate the results that matter: the common fields stay visible, the rest sit
  behind a **More fields** toggle, and paging shows `first–last of total` so "not found" is
  distinguishable from "not on this page". Always keep the free-text box too, and let it accept a
  pasted URL/ID — an external site will usually out-search an in-app form, and refusing the paste
  makes the user retype what they already found.
- **Review step** — any bulk action that writes to files renders every proposed change as an
  editable table *before* it commits, with per-row escape hatches (a **Skip** option) and the
  provenance of each guess shown (`№` = read from the filename, `order` = inferred). No
  "apply and see what happened": the review table **is** the confirmation, so there is no second
  dialog. Conflicts (two rows claiming one target) block the commit and highlight the rows with
  `--danger` rather than silently picking a winner.
- **Setting row** (`.setting-row`, `Settings.tsx`) — a settings page is a list of *definitions*, not
  a form filled top to bottom, so each row is two columns: name + config key + help on the left, the
  control on the right at a fixed width, so a column of inputs reads as a column. The config key is
  shown as a mono chip (`.setting-key`) because it is the identifier the docs, the environment
  variable and `config.json` all use — paraphrasing it would make the page unsearchable. A **tier
  badge** (`pill-off`: *On restart*, *Read-only*) sits with the label, not the control: it qualifies
  the setting, not the value, and is true whether or not you are editing. An edited row is marked
  with a 2px `--accent` bar on its leading edge rather than by recolouring the input, so "what have I
  changed" is answerable by scanning down the page. A read-only value renders as data (mono, on
  `--surface-2`), never as a disabled input pretending to be editable. Stacks to one column below
  760px.
- **Save bar** (`.savebar`) — for long editable pages: sticky to the bottom of the content area,
  rendered **only when something is unsaved**, stating the count (`3 unsaved changes`) beside
  Discard and Save. A Save button that scrolls away is one people stop trusting, and the count is
  what makes it unambiguous which page state is about to be written. Not for pages that save per row
  or per modal — those already confirm at the point of action.
- **Inline note** (`.note`, `.note-ok` / `.note-warn`) — the non-transient half of the toast/alert
  spec: a tinted panel with a 3px status bar on its leading edge, a bold one-word title and a line of
  detail. Used for outcomes that must stay on screen after the toast has gone — what a save applied
  now, and what is waiting for a restart.
- **Labelled divider** (`.or-divider`) — hairline `--border` rules flanking an eyebrow-styled label
  (11px, uppercase, `.06em`, `--text-dim`). Separates alternative paths that are equally valid, not
  a section boundary — currently password login vs. external identity providers.
- **Toast / inline alert** — `*-bg` tint + `*-text`, left accent bar in the status color, icon +
  message + optional action. Errors state what happened and how to fix it (see Voice).
- **Progress** (`ProgressBar.tsx`) — thin (4px) track `--surface-2`, fill `--accent` (or `--info`
  while scanning); paired with `n/total` in `--font-mono`. Used for run progress.
  **Indeterminate variant** (`.coverage-track.indeterminate`) — for a stage doing real work that
  the counters do not describe. A run counts *files*, and only its walk moves that number; its
  refresh, migration and collection stages count entities, so a proportional bar there sits frozen
  at 0% or 100% for minutes and is read as a hang. The indeterminate bar drops the numbers, drops
  `aria-valuenow` (which is what marks a progressbar indeterminate) and shows moving 45° stripes in
  `--border`. **Stripes, not a travelling sliver**, because `prefers-reduced-motion` suppresses the
  animation globally — a frozen sliver reads as a bar stalled at 15%, while static stripes still
  read as "working, unquantified". Which phases drive the bar is one shared rule
  (`phaseDrivesProgress`, `components/phases.ts`), because the Activity banner, the Activity feed
  rows, the Dashboard widget and the Artist page all draw the same counters.
- **Loading placeholder** (`.skel`, `Skeleton` in `ui.tsx`) — what a surface shows while its first
  fetch is in flight, when that fetch is slow enough to leave the page blank. It is **the surface's
  own geometry with the values missing**, not a spinner and not a centred word: the collection's
  loading state is the real table — one `<thead>` shared with the loaded state, real 34px rows, one
  placeholder per cell — so the rows arrive *into* a table rather than replacing a void with one. A
  shape that is already correct is what makes the arrival feel instant; a spinner says *wait* and
  nothing else.
  **Size each placeholder like the content it stands in for.** `table.data` is auto-layout, so a bar
  wider or narrower than the real value hands the difference to another column and the headers slide
  sideways as the rows land. Matching the *range* of the real content (a name is 75–155px, a pill is
  21px tall) settles that to a few pixels; it cannot be eliminated, since the widest name in a
  collection is not knowable before the fetch. Vertical position is exact, which is the half that
  matters — nothing below the table moves.
  **A placeholder is an empty slot, and this app already draws one** — `--surface-2` with an inset
  hairline, the same treatment as `.coverage-cell.none`. No shimmer sweep: that is a second visual
  idea for a fact the page states once, and `prefers-reduced-motion` deletes it globally anyway,
  leaving a design whose loading state is invisible to the people most likely to need it stated.
  The one moving part is the **indeterminate coverage meter** in the row's meter column (see
  *Progress*), which already means "real work, counted in a unit this bar does not have" — a
  coverage that is not known yet is exactly that, so a loading collection is drawn in the same
  language as a loading run.
  **First load only.** Show it for `loading && !data`; a reload has rows on screen that are still
  true, and replacing them with placeholders is a step backwards from the wait the pattern exists to
  soften. No artificial delay before it appears, either — the skeleton is not a different layout, so
  there is nothing to flash.
  **The controls that are already answerable stay live.** The filter box works through the wait
  (typing narrows the list the moment it arrives) and so do the sort headers, since a sort chosen
  during the wait is the order the rows arrive in. Counts do not: a chip reading `Mismatched 0` or a
  toolbar reading `0 of 0` states a fact nobody knows yet, so those are simply absent until they are
  known.
  The rows are placeholders, never a promise about **how many** there are — pick a count that fills
  the fold without implying a collection size. Mark the placeholder block `aria-hidden`, put
  `aria-busy` on the container, and state it once in words for the reader who cannot see any of it
  (`.sr-only` + `role="status"`).
- **Run rail** (`.rail`, `FeedRow` in `Activity.tsx`) — how a flat feed shows that several activities
  came from one run. The Activity feed is one row per thing that happened, ordered only by when it
  happened, so the relationship cannot be structural: a line in a 20px gutter joins the rows of one
  cascade, capped by a dot on the run itself — which sits at the *bottom* of its group, because it
  started first, so the rail reads as the cascade growing upward out of the thing that started it.
  Quiet by default (`--border-strong`, a divider's weight); **pointing at any row lights the rail of
  its whole cascade** (`--accent`), which is the answer to "which of these belong together" without
  giving each run a colour of its own. A per-run hue is the rule this deliberately avoids — a full
  page needs two hundred of them, and colour here belongs to status.
  **The rows themselves are not tinted with it.** Filling all seven rows made the one under the
  pointer indistinguishable from its relatives, so the row you were about to click stopped being
  obvious — the row keeps the table's ordinary `--surface-2` hover and the relation is drawn beside
  it, in the gutter. Two facts, two places.
  It replaced a disclosure that nested stages under their run. **Every row is the same row**: a stage
  of a run and a verb somebody pressed are the same work, so they render identically, with their own
  timestamp, duration, progress bar and modal. Nesting made a cascading activity look like a lesser
  kind of thing and put its detail two modals deep.
- **Run reference** (`.railref`) — the name of the run a row came from (`↳ Nightly process`), or the
  count of what a run spawned (`└ 5 activities`), sitting quiet under the title. Both narrow the feed
  to that one cascade, which is what carries the relationship when the rail cannot — a health check
  or a hand-pressed refresh between two stages breaks the run of adjacent rows. `--text-dim` at
  `--text-xs`: it is provenance, not the row's subject.
- **Activity detail** (`EventDetail` in `Activity.tsx`) — one modal, one activity: never a list of
  its siblings and never a modal that swaps itself for another. It opens with the **summary line the
  feed row states**, because every emitter writes one and the counters are what may be missing — a
  stage whose figures are all zero (a refresh with nothing due) drops every counter and would
  otherwise open onto a blank panel. Then the counters, then a one-line **note about the kind of
  activity** (`TYPE_NOTES`) where a reader would otherwise have to ask: three stages run *after* the
  tagging that looks like the point of the run, and "did that happen in the wrong order?" is a
  question the order itself provokes. The note is in the UI rather than in the emitter's summary
  because it is the same sentence every time — a Go package should not be writing English into the
  database — and a type with nothing to explain has no entry.
- **Event counters** (`StatRow` in `Activity.tsx`) — an event declares its own counters
  (`models.EventStat`), and the UI renders what it finds rather than knowing each type's keys.
  A counter naming an `EventItem` status becomes a **`FilterChip`** over the detail list below it,
  by the same rule as everywhere else: a count is read as a prelude to "show me which ones". One
  with no rows behind it stays a **`.statpill`** — a chip's size and rhythm, deliberately without
  its affordances (no hover, no pointer, no `aria-pressed`), because making a dead number look
  pressable is worse than leaving it alone. **One row, one size.** The static half used to be a
  22px hero figure, which put two scales of counter side by side: the one chip among them read as a
  stray control rather than as the one counter you can act on, and the row read as a dashboard
  header instead of facts about one activity. Same size means the difference between them is the
  affordance, which is the difference that is actually there.
  **Zero-valued counters are dropped**, because an emitter declares the same set
  every time so its events stay comparable, which means most events carry several that did not
  happen; "0 gone upstream · 0 re-linked · 0 failed" is noise in front of the two that did.
- **Identifier row** (`EntityItemRow` in `Activity.tsx`) — how a MusicBrainz ID is shown when it is
  the subject of a row rather than a field on one. A UUID is not a subject: a page of forty that
  returned 404 says something is wrong and nothing about what. So the ID chip is followed by **what
  kind of identifier it is, as the link to musicbrainz.org** (one element, one job — "open this
  release there"; the type has to be on the row because a release and a release-group are both
  UUIDs and the fix for a bad one differs), then the outcome pill, then a second line with **what
  the app calls it**, linked to its own page, and **how many files depend on it**. The file count is
  a control that expands the paths, fetched on demand — a count is read as a prelude to "show me
  which ones", and only the row being looked at is worth its paths.
  **MusicBrainz's vocabulary here, not the collection's** (*release*, not *edition*): the row
  reports what MusicBrainz said about a MusicBrainz identifier and the label opens that page, so
  translating the type would make the label disagree with its destination. This is the documented
  exception to *Collection vocabulary*, and it is fenced to rows whose subject is an upstream
  identifier.
- **Outcome legend** (`OutcomeLegend`) — one sentence per outcome present in a detail list, under
  it. For when a pill's label is honest but incomplete: "Changed upstream" reads as *a particular
  edit was made* when what it records is that a re-fetched payload no longer matches the cached one.
  Only the outcomes actually in the list, by the same rule that drops zero-valued counters — a
  glossary of five states in front of a list holding one of them is noise. Prefer fixing the label;
  reach for this only when the fix would make the label a sentence.
- **Empty state** — centered, muted icon, one-line explanation, and a primary action. An empty
  screen is an invitation to act ("No libraries yet — add your first music folder").
- **Modal** (`Modal`, `ui.tsx`) — `--surface-3`, `--shadow-2`, `--radius-lg`, backdrop
  `rgba(13,11,20,.7)`; focus-trapped.
  **Never taller than the screen.** The dialog is capped at the backdrop's content box and split into
  a pinned title and a scrolling `.modal-body`. A centred flex item that outgrows its container
  overflows *both* ends, and the backdrop is `position: fixed` with no overflow of its own — so a
  long modal's top edge was not merely off-screen, it was unreachable, with no scrollbar anywhere to
  bring it back. The title is what a long modal loses first, hence pinning it rather than letting the
  whole dialog scroll. (`min-height: 0` on the body is load-bearing: a flex item's default floor is
  its content, which is the overflow being prevented.)
  **An inner `.scroll` is for keeping something pinned above a list** — a search field while its
  results move, the file path over its tag diff. A list with nothing above it to keep in view does
  not get one: the body already scrolls, and two nested scrollbars for one list is worse than a long
  list. The Activity detail modal had two of them and was the case that broke.
- **Confirm dialog** (`ConfirmDialog`, `ui.tsx`) — a Modal with a required **body** and a confirm
  button that **restates the verb** ("Detach", "Ignore cache and refresh"), never "OK". The body is
  the whole component: a dialog that only asks *are you sure?* adds a click without adding
  information, and people learn to dismiss it unread. It carries what the button cannot — how long
  the work takes, what it overwrites, what it leaves alone. **Cancel comes first and is the plain
  one**: the escape from a dialog you opened by accident must not be the button styled to be pressed.
  Use `danger` for anything that overwrites or discards; expensive-but-safe work stays `primary`.
  Confirm the *consequential* variant only — putting a dialog on the cheap sibling of an expensive
  action trains people to click through the one that matters. When both variants are worth offering,
  that is a **Choice dialog**, not two confirms.
- **Choice dialog** (`ChoiceDialog`, `ui.tsx`) — for a verb with an ordinary reading and an expensive
  one. **The choice lives in the dialog, never as a control beside the button.** A checkbox that
  changes what the adjacent button does is a mode switch — two controls for one intention, and the
  weaker one is what people click first (see *Direct manipulation beats a mode switch*). It is worse
  on a settings surface, where a persistent tick box sits among controls that genuinely are stored
  preferences and reads as one.

  Moving the choice inside also **removes a rule rather than enforcing one**. The old shape needed
  "the box resets once a pass starts", or one considered decision silently became a setting and the
  next press — days later, by someone who did not tick it — cost hours. Nothing is left ticked, so
  there is nothing to reset.

  Two actions plus Cancel, ordered `Cancel · alternate · primary`. The primary is the routine reading
  (what the schedule would do); the alternate is `secondary`, so the expensive one is not what the eye
  lands on first — the same weighting the button spec uses for a destructive member of an ordinary
  row. Each label restates its own verb.

  **It differs from a confirm dialog in kind**: that one asks *are you sure*, this asks *which*. That
  is why reaching the ordinary action through it does not become the trained-through click the confirm
  rule warns about — nothing here is a speed bump, every press still resolves a real choice. Used by
  `RefreshMetadataDialog` and `RefreshArtworkDialog`; `SyncLidarrDialog` is the same principle in its
  other legitimate form, a single action with an in-dialog modifier, which suits a modifier that is
  orthogonal to the action rather than a different reading of it.

  One verb gets **one entry point**. Forcing a metadata refresh was reachable from two pages, which
  is how the same two words came to honour the cache everywhere except one page.
- **Panel footer** (`.panel-rule`) — an action that operates on a card's contents belongs *inside* the
  card, under a full-bleed hairline, not as a section beneath it. Given its own eyebrow and its own
  top-right button — the page-head convention — it reads as a second page heading. The rule is pulled
  out to the card's padding on both sides so it divides the panel rather than boxing the footer.
- **Brand mark** (`.brand .logo`, `Logo.tsx`) — a tag glyph on a 22px accent tile
  (`linear-gradient(135deg, var(--accent), #4c7dff)`, `5px` radius), followed by the wordmark. It
  appears exactly twice: the sidebar and the login card. **Drawn, never typed** — the mark was an
  emoji (`🏷`), which renders as a different picture on every platform and badly on Windows; a logo
  is the one element that may not vary by whose machine it is on. The glyph is inline SVG on
  `currentColor`, so the artwork exists once and is tinted by the tile rather than duplicated per
  colour. The **favicon** (`webui/public/favicon.svg`) is the same mark rebuilt standalone: a favicon
  inherits nothing from the document, so it states the gradient and the white glyph itself, with a
  wider stroke to survive 16px. Two files by necessity — keep the glyph and corner radius in step.

### Signature: the coverage meter
The browsing counterpart to the tag-diff row, and the same colour language. One cell per item —
filled `--diff-add-text` = on disk, `--warning` = partial, `--surface-2` + hairline = missing:

```
Albums   ████████████████░░░░░░   32 of 41 on disk
Tracks   ██████████▓▓░░           10/12 tracks on disk
```

- Same shape at every level — albums of an artist, tracks of an album, tracks of one edition, albums
  in a section header — so it is learned once and read everywhere.
- Below 30 items it is **segmented**, because a cell count answers "how many" as well as "how much":
  9 of 12 albums reads differently from 75%. Above 30 it collapses to a proportional bar
  (`.coverage-track`), since 200 two-pixel slivers are noise.
- **The cap is 30 *or* what the given width holds, whichever is smaller.** Cells are `flex: none`,
  so a count-only cap does not shrink or wrap when it runs out of room — it draws 118px of meter
  inside a 90px box and slides out from under the number beside it. `CoverageBar` budgets
  `floor((width + gap) / (cell + gap))` from a numeric `width` and collapses above it; a percentage
  or absent width keeps the plain count cap, since those call sites size to their content. The shape
  follows the count **and** the space.
- **A column of meters may force the proportional form** (`proportional`), whatever the count. One
  shape all the way down a page of artists is worth more than the cell count on the short rows,
  and the mono `8/12` beside it already answers "how many". Reserve this for repeated rows — a
  header showing one thing's coverage keeps the segmented form, which is where the cell count is
  actually read.
- Always paired with the numbers in `--font-mono` (`32 of 41`, `10/12`) and an `aria-label` saying
  what the items are. The bar is never the only carrier of the count.
- It replaces columns of bare counts. Four numeric columns were being read as one question — how much
  of this do I have — and the bar answers it directly.

### Signature: the tag-diff row
The recurring element that makes Autotaggerr recognizable. One row per tag on the file/scan-detail
view:

```
MUSICBRAINZ_ALBUMID   e28e29e0-…-a6beffd99aad   ← current (mono, --text-muted)
                      019fa765-…-c389e527ed21   ← new     (--diff-add-text on --diff-add-bg)
```
- Key: `--font-mono`, `--text-xs`, uppercase, `--text-dim`.
- Unchanged tag: single value, `--text-muted`, no highlight.
- Changed tag: current value on `--diff-remove-bg` with strike-through `--diff-remove-text`; new value
  on `--diff-add-bg` in `--diff-add-text`. This is also the color language for scan summaries
  ("N changed") and library-item status.
- Absent side of a diff — "(empty)", "(removed)" — is `.diffv.none`, `--text-dim`, italic. **Not
  `.empty`**: that is the empty-state component, declared later in `theme.css` at the same
  specificity, so it won and every absent value inherited a 48px-padded, centred empty state. A
  field a file did *not* have rendered eight times the height of one it did. A modifier class on a
  shared prefix is not namespaced by that prefix — check the whole file before naming one.

- **File group** (`.filegroup` / `.filehead`) — one file's diff, collapsed to its heading. The path,
  its outcome and its tag count stay visible; only the fields under them collapse, so this is a
  group disclosure and not the *list* collapse the Activity feed was told off for. Open by default
  only below `EXPANDED_FILE_LIMIT` (10) changed files, with an expand/collapse-all beside the
  section label once there is more than one: a handful of diffs is what the modal was opened to
  read, fifty is a wall to scroll past on the way to anything else. The threshold is the same kind
  of rule as the coverage meter's segmented-below-30 — the shape follows the count because the
  reading does.

---

## Voice & copy

- Sentence case everywhere. Active voice. A control says what it does: **Save changes**, not Submit;
  **Add library**, not Create. An action keeps its name through the flow (button "Scan" → toast
  "Scan started").
- Name things by what the user controls: **Libraries, Managers, Data sources, Tagger profiles, Scans**
  — never by internal type names.
- Errors explain what happened and how to fix it, in the interface's voice, never vague, no apology:
  "Lidarr didn't respond — check the base URL and API key."
- Empty states are invitations, not decoration.
- Identifiers are shown verbatim in mono; never paraphrase an MB ID or path.
- **A counter names its unit whenever the row it sits in mixes units.** A scan's stats are files
  *and* tags side by side, so they read **Files processed · Files unchanged · Files changed · Tags
  written** — bare "Changed 1 · Tags written 21" invites the reading that 21 things changed when one
  file did. Say the unit once per line where the clauses share it ("27 files processed · 1 changed"),
  and on every chip where they do not. Counters that carry it already — Releases checked, Entities
  checked, Files re-tagged — are the pattern, not the exception.

---

## Accessibility & quality floor

- Contrast: body text ≥ 4.5:1, large/UI text and meaningful graphics ≥ 3:1. `--text-dim` is for
  non-essential labels only.
- Every interactive element has a visible focus ring (`--accent-ring`) and a ≥ 28px target.
- Respect `prefers-reduced-motion`: no non-essential animation, instant state changes.
- Color is never the only signal — pair status color with an icon or label (pills carry text; the
  diff carries strike-through + position).
- Responsive down to mobile: the sidebar collapses; tables scroll horizontally inside their own
  container (the page body never scrolls sideways).
