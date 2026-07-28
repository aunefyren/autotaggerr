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
  gradients-as-decoration, no ornament that doesn't encode meaning.

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
- **Toast / inline alert** — `*-bg` tint + `*-text`, left accent bar in the status color, icon +
  message + optional action. Errors state what happened and how to fix it (see Voice).
- **Progress** — thin (4px) track `--surface-2`, fill `--accent` (or `--info` while scanning);
  paired with `n/total` in `--font-mono`. Used for scan progress.
- **Empty state** — centered, muted icon, one-line explanation, and a primary action. An empty
  screen is an invitation to act ("No libraries yet — add your first music folder").
- **Modal** — `--surface-3`, `--shadow-2`, `--radius-lg`, backdrop `rgba(13,11,20,.7)`; focus-trapped.

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
