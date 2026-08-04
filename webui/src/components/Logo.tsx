/**
 * The Autotaggerr mark: a tag glyph on the accent tile.
 *
 * Inline SVG rather than an imported asset or an emoji. The emoji it replaces
 * rendered differently on every platform (and poorly on Windows), which is the
 * one thing a logo may not do. Inline also means it inherits `currentColor`, so
 * the glyph is white on the tile without a second copy of the artwork, and it
 * costs no request — the sidebar paints with the first byte of the bundle.
 *
 * The favicon (`public/favicon.svg`) is deliberately a separate file: it has no
 * document to inherit colour from, so it carries the tile and the brand colours
 * itself. Keep the two in step — same glyph, same corner radius.
 */
export function Logo() {
  return (
    <span className="logo" aria-hidden="true">
      <svg
        viewBox="0 0 24 24"
        fill="none"
        stroke="currentColor"
        strokeWidth="2"
        strokeLinecap="round"
        strokeLinejoin="round"
      >
        <path d="M20.59 13.41l-7.17 7.17a2 2 0 0 1-2.83 0L2 12V2h10l8.59 8.59a2 2 0 0 1 0 2.82z" />
        <line x1="7" y1="7" x2="7.01" y2="7" />
      </svg>
    </span>
  );
}
