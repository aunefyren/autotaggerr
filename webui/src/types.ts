export interface User {
  id: string;
  username: string;
  email: string;
  role: string;
}

export interface DataSource {
  id: string;
  name: string;
  type: string;
  base_url: string;
  rate_limit: number;
  enabled: boolean;
  health: string;
}

/**
 * What role each data source type can play. The four types share one table because
 * they share every field, but they are not interchangeable: only a metadata provider
 * can be a library's data source. Mirrors `models.DataSourceCategory` in Go — the API
 * enforces this, so keep the two definitions in step.
 */
export const DATA_SOURCE_CATEGORY: Record<string, string> = {
  musicbrainz: "metadata",
  acoustid: "fingerprint",
  coverartarchive: "artwork",
  fanart: "artwork",
};

export function dataSourceCategory(type: string): string {
  return DATA_SOURCE_CATEGORY[type] ?? "";
}

/** Human names for the provider types, so each one is spelled the same everywhere. */
export const DATA_SOURCE_LABEL: Record<string, string> = {
  musicbrainz: "MusicBrainz",
  acoustid: "AcoustID",
  coverartarchive: "Cover Art Archive",
  fanart: "fanart.tv",
};

export interface Manager {
  id: string;
  name: string;
  type: string;
  enabled: boolean;
  lidarr_base_url?: string;
  health: string;
}

export interface TaggerProfile {
  id: string;
  name: string;
  write_tags: boolean;
  remove_values: boolean;
  use_current_artist_name: boolean;
  use_custom_artist_delimiter: boolean;
  custom_artist_delimiter: string;
}

export interface Library {
  id: string;
  name: string;
  path: string;
  manager_id: string | null;
  data_source_id: string | null;
  tagger_profile_id: string | null;
  enabled: boolean;
  cron: string;
  last_scan: string | null;
  /** Per-library opt-in to audio fingerprint identification. Off by default. */
  use_acoustid: boolean;
}

export interface LibraryItem {
  id: string;
  library_id: string;
  path: string;
  mb_release_id: string;
  correlation_source: string;
  status: string;
  last_tagged_at: string | null;
  /** A manual attach that automatic resolution must not override. */
  pinned: boolean;
}

/** One hit from the MusicBrainz release search used by manual attach. */
export interface ReleaseSearchResult {
  id: string;
  title: string;
  status: string;
  date: string;
  country: string;
  disambiguation: string;
  "artist-credit": { name: string; joinphrase: string }[];
  media: { format: string; "track-count": number }[];
}

/** One page of release search hits. count is the total, not the page size. */
export interface ReleaseSearchPage {
  count: number;
  offset: number;
  releases: ReleaseSearchResult[] | null;
}

/** A release's flattened tracklist, as offered by the attach picker. */
export interface ReleaseTracks {
  release: {
    mb_id: string;
    title: string;
    date: string;
    country: string;
    disambiguation: string;
  };
  tracks: ReleaseTrack[];
}

export interface ReleaseTrack {
  track_id: string;
  recording_id: string;
  title: string;
  position: number;
  number: string;
  medium: number;
  medium_title: string;
  length: number;
}

/**
 * One proposed file → track pairing in a bulk attach. An empty
 * mb_release_track_id means "skip this file"; `how` says why the pairing was
 * proposed, so the review step can flag the weaker signal.
 */
export interface BulkMapping {
  item_id: string;
  path: string;
  mb_release_track_id: string;
  track_number?: string;
  track_title?: string;
  medium?: number;
  /** "number" = matched the filename's track number; "order" = sorted and zipped. */
  how?: "number" | "order" | "";
}

export interface BulkPreview {
  release: ReleaseTracks["release"];
  tracks: ReleaseTrack[];
  mappings: BulkMapping[];
}

/** Whether fingerprint identification can run at all, and if not, why. */
export interface IdentifyAvailability {
  available: boolean;
  reason?: string;
}

/**
 * One AcoustID suggestion for a file. `score` is the fingerprint's own confidence
 * in the *recording*; `confidence` additionally weighs how well the release agrees
 * with the file's folder, which is what orders the list.
 */
export interface IdentifyMatch {
  score: number;
  confidence: number;
  reasons: string[] | null;
  recording_mb_id: string;
  title: string;
  artist: string;
  release_mb_id: string;
  release_title: string;
  release_year: number;
  track_count: number;
}

export interface Health {
  name: string;
  version: string;
  counts: Record<string, number>;
}

/** One queued or running background job. */
export interface JobView {
  kind: string;
  title: string;
}

export interface ScanStatus {
  running: boolean;
  /** The job executing right now (a scan, re-tag, or metadata refresh); absent when idle. */
  current_job?: JobView;
  /** Jobs waiting behind the current one, in the order they will run. */
  queue?: JobView[];
  started_at?: string;
  finished_at?: string;
  processed: number;
  unchanged: number;
  changed: number;
  tags_written: number;
  errors: number;
  last_error?: string;
  /** Live progress while running: files to visit, files done, current stage, and the
   *  artist folder being worked on. Absent/zero when no scan is in flight. */
  total?: number;
  done?: number;
  phase?: string;
  current?: string;
}

/**
 * One mirror pass, plus how much of the collection is cached right now.
 *
 * `fetched` and `fresh` are the pair worth reading: `fetched` is what actually
 * cost a MusicBrainz rate-limit slot, `fresh` is what the local mirror already
 * had. `cached` is keyed by entity kind and is meaningful between passes too.
 */
export interface MirrorStatus {
  running: boolean;
  phase?: string;
  started_at?: string;
  finished_at?: string;
  total: number;
  done: number;
  fetched: number;
  fresh: number;
  errors: number;
  /** Releases whose metadata differs from the cached copy. Reported, not acted on. */
  changed_releases: number;
  gone_releases: number;
  relinked: number;
  last_error?: string;
  cached?: Record<string, number>;
}

export interface ItemsPage {
  total: number;
  limit: number;
  offset: number;
  items: LibraryItem[];
}

export interface TagDiffEntry {
  key: string;
  current: string;
  desired: string;
  changed: boolean;
}

export interface ItemTags {
  item: LibraryItem;
  tags: TagDiffEntry[];
}

/** One field's before/after from a tag write. */
export interface TagChange {
  field: string;
  old: string;
  new: string;
}

/** One file's outcome inside an event: what happened, and exactly what changed. */
export interface EventItem {
  id: string;
  event_id: string;
  path: string;
  /** "changed" | "error" */
  status: string;
  tags_written: number;
  error?: string;
  changes?: TagChange[];
}

export interface Event {
  id: string;
  type: string;
  status: string;
  started_at: string;
  finished_at: string | null;
  title: string;
  summary: string;
  details: Record<string, unknown> | null;
  /** Live progress, written on a throttled ticker while the event runs and left on
   *  the finished row. total/done drive a bar; phase names the stage; current is what
   *  is being worked on. Absent/zero when the job reported no countable progress. */
  total?: number;
  done?: number;
  phase?: string;
  current?: string;
  /** Per-file detail. Only the single-event endpoint returns it, never the feed. */
  items?: EventItem[];
}

export interface EventsPage {
  total: number;
  limit: number;
  offset: number;
  events: Event[];
}

export interface CollectionArtist {
  mb_id: string;
  name: string;
  /** Following is *stored*; see follow_governs for whether it currently decides. */
  monitored: boolean;
  /**
   * Whether the native follow settings actually govern this artist. False when a
   * manager (Lidarr) owns it — the flag is kept, but the manager decides what is
   * wanted, so the follow controls are shown frozen rather than as live toggles.
   */
  follow_governs: boolean;
  managed_by: string;
  /** "library" = materialised from files on disk; "manual" = added by hand. */
  origin: string;
  /** Comma-separated primary types that following auto-wants. Empty = Album,EP. */
  follow_types: string;
  /** Whether live albums, compilations and remixes count when following. */
  follow_secondary: boolean;
  last_synced_at: string | null;
  owned_count?: number;
  complete_count?: number;
  partial_count?: number;
  missing_count?: number;
  mismatch_count?: number;
  picked_count?: number;
}

/** How the disk view and the manager's catalog view disagree, if at all. */
export type Discrepancy = "" | "unmapped" | "stale_catalog" | "not_indexed";

export interface CollectionReleaseGroup {
  mb_id: string;
  artist_mb_id: string;
  title: string;
  primary_type: string;
  secondary_types: string;
  first_release_date: string;
  /** Disk view — what Autotaggerr walked. */
  owned: boolean;
  owned_tracks: number;
  total_tracks: number;
  /** Catalog view — what the manager says should exist. 0 total = unknown. */
  in_catalog: boolean;
  catalog_owned_tracks: number;
  catalog_total_tracks: number;
  catalog_monitored: boolean;
  /** Derived server-side so the rules have one definition. */
  complete: boolean;
  discrepancy: Discrepancy;
  /**
   * Wanted, and why — which is also what could change it. "explicit" = picked by
   * hand and editable here; "auto" = derived from following the artist;
   * "manager" = the library's manager (Lidarr) monitors it. Only an explicit want
   * is a live toggle on the row; the derived ones are shown as frozen state.
   */
  wanted: boolean;
  wanted_source: "" | "explicit" | "auto" | "manager";
  /** True when any edition of the group satisfies the want (the default shape). */
  wanted_any_edition: boolean;
  /** Specific editions asked for. */
  desired_releases: string[];
  /** Specific songs asked for (recording MBIDs). Empty = the whole thing. */
  desired_recordings: string[];
  /**
   * How many distinct editions files are owned of. owned_tracks/total_tracks
   * describe only the best-owned edition, so this is what says two pressings are
   * involved.
   */
  owned_editions: number;
}

/** One MusicBrainz edition of a release-group, plus what is owned of *that* edition. */
export interface Edition extends ReleaseSearchResult {
  owned: boolean;
  owned_tracks: number;
  owned_total_tracks: number;
  complete: boolean;
}

/**
 * Who an artist is, from MusicBrainz — a live read, never stored, and never
 * required: the artist page renders without it and simply says less.
 */
export interface ArtistInfo {
  /** MusicBrainz's own vocabulary: "Person", "Group", "Orchestra", … */
  type: string;
  disambiguation: string;
  country: string;
  /** Where they are from — begin area if MusicBrainz has one, else the country's area. */
  area: string;
  begin: string;
  end: string;
  ended: boolean;
  genres: string[] | null;
}

export interface ArtistDetail {
  artist: CollectionArtist;
  release_groups: CollectionReleaseGroup[];
  desires: CollectionDesire[];
}

/** An enabled external login option, as shown on the login page. */
export interface LoginProvider {
  id: string;
  name: string;
  type: string;
}

/** An OIDC login provider, as administered on the Login providers page. */
export interface AuthProvider {
  id: string;
  name: string;
  type: string;
  enabled: boolean;
  issuer: string;
  client_id: string;
  scopes: string;
  redirect_url: string;
  allow_signup: boolean;
  default_role: string;
}

/** A MusicBrainz artist search hit, for adding an artist you own nothing of. */
export interface ArtistSearchResult {
  id: string;
  name: string;
  "sort-name": string;
  disambiguation: string;
  type: string;
  country: string;
}

/** An explicit want. Empty release_mb_id = any release of the group will do. */
export interface CollectionDesire {
  id: string;
  artist_mb_id: string;
  release_group_mb_id: string;
  release_mb_id: string;
  recording_mb_ids: string[] | null;
}

/**
 * A MusicBrainz identity change: an entity that was merged into another upstream,
 * or deleted outright. Pending rows are waiting for approval because their category
 * is held for review; everything else is history.
 */
export interface MusicbrainzMigration {
  id: string;
  entity_type: string;
  old_mb_id: string;
  new_mb_id: string;
  kind: string;
  status: string;
  name: string;
  affected_files: number;
  affected_desires: number;
  touches_pinned: boolean;
  detected_at: string;
  applied_at: string | null;
  error?: string;
}

export interface MigrationList {
  migrations: MusicbrainzMigration[];
  pending: number;
}

/** Which categories of migration wait for approval instead of applying themselves. */
export interface MigrationPolicy {
  review_releases: boolean;
  review_artists: boolean;
  review_pinned: boolean;
  review_deletions: boolean;
  entity_types: string[];
}
