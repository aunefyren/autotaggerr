import { ReleaseSearchResult } from "../types";

/**
 * "2×CD · 34 tracks" — what actually tells two editions of an album apart.
 * Formats are grouped rather than listed, so a 3-disc set reads "3×CD" and not
 * "CD + CD + CD".
 */
export function mediaSummary(media: ReleaseSearchResult["media"]): string {
  if (!media?.length) return "";
  const byFormat = new Map<string, number>();
  for (const m of media) {
    const format = m.format || "Unknown";
    byFormat.set(format, (byFormat.get(format) ?? 0) + 1);
  }
  const formats = [...byFormat].map(([f, n]) => (n > 1 ? `${n}×${f}` : f)).join(" + ");
  const tracks = media.reduce((n, m) => n + (m["track-count"] ?? 0), 0);
  return [formats, tracks ? `${tracks} tracks` : ""].filter(Boolean).join(" · ");
}
