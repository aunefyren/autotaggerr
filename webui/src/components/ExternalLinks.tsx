import { ExternalLink } from "../types";

const KIND_LABEL: Record<string, string> = { lidarr: "Lidarr", plex: "Plex" };

/**
 * Links out to where an artist or album also lives — the Lidarr that holds it, the
 * Plex library it is in — set beside the MusicBrainz link and styled the same, since
 * they are the same kind of thing: an identifier's home elsewhere. The name is shown
 * only when two links of one kind would otherwise read identically (two Lidarrs, two
 * Plex libraries).
 */
export function ExternalLinks({ links }: { links: ExternalLink[] | null | undefined }) {
  if (!links || links.length === 0) return null;
  const counts = links.reduce<Record<string, number>>((n, l) => ({ ...n, [l.kind]: (n[l.kind] ?? 0) + 1 }), {});
  return (
    <>
      {links.map((l) => {
        const label = KIND_LABEL[l.kind] ?? l.kind;
        return (
          <span key={l.url} className="row" style={{ gap: 6 }}>
            <span className="sep">·</span>
            <a
              className="dim mono"
              style={{ fontSize: 11 }}
              href={l.url}
              target="_blank"
              rel="noreferrer noopener"
              title={`Open in ${label}${l.name ? ` (${l.name})` : ""}`}
              onClick={(e) => e.stopPropagation()}
            >
              {counts[l.kind] > 1 ? `${label}: ${l.name}` : label} ↗
            </a>
          </span>
        );
      })}
    </>
  );
}
