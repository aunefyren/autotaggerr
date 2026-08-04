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
- **Input / select / textarea** — `--surface-2` bg, `--border` border, `--text`, `--text-dim`
  placeholder, `--font-mono` when the value is an identifier/path. Focus ring. Invalid = `--danger`
  border + helper text in `--danger-text`.
- **Toggle / checkbox** — off = `--surface-2`/`--border`; on = `--accent`. 28px min target.
- **Table** — `--surface-1` body, `--surface-2` sticky header (`--text-muted`, medium, `--text-xs`
  uppercase). Rows separated by `--border` hairlines (no zebra). Hover = `--surface-2`. Selected =
  `--accent-subtle` fill + 2px `--accent` left border. Numeric/ID cells `--font-mono`. Right-align
  numbers.
- **Status pill** — `--radius-pill`, `--text-xs`, medium, `10px` tall dot + label; color from the
  status map, on a `*-bg` tint with `*-text`. Disabled/unmatched uses `--surface-2`/`--text-dim`.
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
  and turn a browsing aid into confetti. Sources: Cover Art Archive for covers (no credential),
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
- **Sortable header** (`.sortbtn`, `SortHeader`) — a `<button>` inside the `<th>`, with `aria-sort` on
  the `th` and a caret carrying direction visually (`•` when inactive). Clicking the active column
  flips direction; a new column starts at its own sensible default (year → descending). Numeric and
  date columns default descending, text ascending.
- **Table toolbar** (`.table-toolbar`, `TableToolbar`) — one free-text filter, the chips that narrow
  the same list, and a `first–last of total` count so "no matches" is distinguishable from "empty
  list". Sits above the table, outside its border.
- **Filter chip** (`.chip`, `FilterChip`) — a count that is also a control: label plus a mono number,
  `aria-pressed`, `--accent-subtle` fill when on (`--warning-bg` for drift-shaped filters). Disabled
  at zero unless already active. Counts on a browsing page should nearly always be chips — "3 partial"
  is only ever read as a prelude to "show me which three".
- **Grouped table sections** (`.group-section` / `.group-head`) — a collapsible section per real
  category, with its count and its own coverage meter in the header. Grouping must encode data (the
  MusicBrainz primary type), never visual chunking. Sections that are numerous and rarely the reason
  you opened the page start closed. One sort and one filter apply across every section.
- **Browse state lives in the URL** (`useBrowse`) — query, sort key, direction, active filter, open
  sections, selected detail row. Written with `replace`, not `push`, so sorting a table is not a
  history entry. The reason is concrete: opening an album and coming back must not reset the list.
  A flag whose empty value is meaningful needs a sentinel (`closed=-` for "every section closed"),
  because an empty string reads as unset and springs the defaults back.
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
- **Labelled divider** (`.or-divider`) — hairline `--border` rules flanking an eyebrow-styled label
  (11px, uppercase, `.06em`, `--text-dim`). Separates alternative paths that are equally valid, not
  a section boundary — currently password login vs. external identity providers.
- **Toast / inline alert** — `*-bg` tint + `*-text`, left accent bar in the status color, icon +
  message + optional action. Errors state what happened and how to fix it (see Voice).
- **Progress** — thin (4px) track `--surface-2`, fill `--accent` (or `--info` while scanning);
  paired with `n/total` in `--font-mono`. Used for scan progress.
- **Empty state** — centered, muted icon, one-line explanation, and a primary action. An empty
  screen is an invitation to act ("No libraries yet — add your first music folder").
- **Modal** — `--surface-3`, `--shadow-2`, `--radius-lg`, backdrop `rgba(13,11,20,.7)`; focus-trapped.
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
