/**
 * A link out to the MusicBrainz page for an entity. Checking an edition against the
 * source is a constant need when identifying files by hand, and MusicBrainz URLs are
 * entirely predictable from entity type + MBID, so this needs no lookup.
 */
export function MBLink({
  entity,
  mbid,
  label = "MB",
}: {
  entity: "release" | "release-group" | "artist" | "recording";
  mbid: string;
  label?: string;
}) {
  if (!mbid) return null;
  return (
    <a
      className="dim mono"
      style={{ fontSize: 11 }}
      href={`https://musicbrainz.org/${entity}/${mbid}`}
      target="_blank"
      rel="noreferrer noopener"
      title="Open on MusicBrainz"
      onClick={(e) => e.stopPropagation()}
    >
      {label} ↗
    </a>
  );
}
