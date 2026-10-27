export interface User {
  id: string;
  username: string;
  email: string;
  role: string;
}

/** One artist credited on an album (GET /artists/:mbid/release-groups/:rgid `credits`). */
export interface AlbumCredit {
  mb_id: string;
  name: string;
}

export interface DataSource {
  id: string;
  name: string;
  type: string;
  base_url: string;
  contact: string;
  rate_limit: number;
  enabled: boolean;
  health: string;
  /** The one MusicBrainz source the client reads — the oldest enabled. Set by the server. */
  in_use?: boolean;
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
  /** Where a browser reaches Lidarr, for links; empty means the base URL. */
  lidarr_web_url?: string;
  // Stored inverted so that a row written before the field existed means "allowed";
  // the form shows it the other way up.
  lidarr_skip_artist_refresh?: boolean;
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
  max_genres: number;
  mp3_multi_value_tags: boolean;
  /** Write only the first record label MusicBrainz lists — Plex shows one. */
  single_record_label: boolean;
  /** Stored the other way up from the form: false writes the Plex-shaped release type. */
  musicbrainz_release_type: boolean;
  /** Stored the other way up from the form: false types remix editions as remixes. */
  ignore_remix_editions: boolean;
  /** Write the album's original release date as DATE rather than the edition's. */
  original_date_as_date: boolean;
  /** Naming templates; empty means the default (see NamingInfo.defaults). */
  naming_artist_folder: string;
  naming_album_folder: string;
  naming_media_folder: string;
  naming_track_file: string;
}

/** The four naming templates, keyed as the Go NamingTemplates / NamingField. */
export interface NamingTemplates {
  artist_folder: string;
  album_folder: string;
  media_folder: string;
  track_file: string;
}

export type NamingField = keyof NamingTemplates;

export interface NamingToken {
  name: string;
  /** The first template this token may appear in. */
  field: NamingField;
  description: string;
  numeric: boolean;
}

export interface NamingInfo {
  defaults: NamingTemplates;
  tokens: NamingToken[];
}

export interface NamingPreview {
  check: {
    errors?: Partial<Record<NamingField, string>>;
    warnings?: Partial<Record<NamingField, string>>;
  };
  samples: { label: string; path?: string; error?: string }[];
}

/** Where a file is against where its profile's naming would put it, relative to the library root. */
export interface PathDiff {
  current: string;
  desired: string;
  changed: boolean;
  note?: string;
}

export type BPMWrite = "" | "fill_empty" | "overwrite";

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
  /** Per-library opt-in to tempo (BPM) analysis. Off by default. */
  analyse_tempo: boolean;
  /** How the stored tempo reaches the BPM tag. "" is off — the default. */
  bpm_write: BPMWrite;
  /** Confidence a tempo needs to be written; 0 or less means the server default (0.5). */
  bpm_min_confidence: number;
  /** The media servers that scan this library, each in one section ("" = every music section). */
  media_servers: LibraryMediaServer[];
}

export interface LibraryMediaServer {
  library_id?: string;
  media_server_id: string;
  section_key: string;
}

export type MediaServerType = "plex";

/** A media server told to refresh albums after their tags change. The token is write-only. */
export interface MediaServer {
  id: string;
  name: string;
  type: MediaServerType;
  enabled: boolean;
  base_url: string;
  /** Where a browser reaches the server, for links; "" links through app.plex.tv. */
  web_url: string;
  token_set: boolean;
  /** How many libraries it scans. */
  libraries: number;
}

/** GET /media-servers/:id/sections — one server's music sections, for the library form. */
export interface MediaServerSections {
  sections: { key: string; title: string; locations: string[] | null }[];
  error?: string;
}

export interface LibraryItem {
  id: string;
  library_id: string;
  path: string;
  mb_release_id: string;
  correlation_source: string;
  status: string;
  last_tagged_at: string | null;
  /** What the last attempt failed with; empty after a success or for unmatched. */
  error: string;
  /** When that failure happened — or, on an unmatched row, when the manager first missed it. */
  last_error_at: string | null;
  /**
   * The failure is expected to clear by itself (MusicBrainz down, throttled). On an
   * unmatched row: the manager missed a file it knew once, and the file stays in the
   * collection until the next attempt confirms it.
   */
  last_error_transient: boolean;
  /** A manual attach that automatic resolution must not override. */
  pinned: boolean;
  /**
   * A pin whose file's audio changed since it was attached: neither applied nor
   * dropped, and the file is not tagged until the pin is kept or released.
   */
  pin_held: boolean;
  /**
   * False when the file's library is managed by Lidarr: the release, the edition
   * and the track are Lidarr's to decide, so attaching by hand is rejected by the
   * API (409) and the controls are shown disabled rather than offered.
   */
  identity_editable: boolean;
  /**
   * Not editable as it stands, but the file has no identity yet: it may be attached to
   * a release of an artist detached from Lidarr. Set only while some artist is detached.
   */
  attach_if_detached?: boolean;
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
  /** What the pressing is printed with — the facts a download's tags most often share. */
  barcode?: string;
  "label-info"?: { "catalog-number"?: string; label?: { name: string } | null }[] | null;
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
  /**
   * A video rather than a piece of audio (a bonus DVD, an enhanced CD's last
   * track). Automatic mapping never proposes one and it is not counted among the
   * tracks you are missing, but it stays pickable by hand for audio ripped off one.
   */
  video: boolean;
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

/** One album's worth of loose files found under an import source. */
export interface ImportGroup {
  folder: string;
  files: string[];
  artist?: string;
  album?: string;
  year?: number;
  /** Where each hint came from: the files' own tags, or the folder name. */
  artist_from?: "tags" | "folder";
  album_from?: "tags" | "folder";
  year_from?: "tags" | "folder";
  /** How many disc folders the files span; 1 for a flat album folder. */
  discs: number;
  /** The release every tagged file agrees on, when they carry one. */
  release_id?: string;
  release_ids_disagree?: boolean;
  /** What the tags say about which pressing the files are; absent when they say nothing. */
  edition?: ImportEditionClues;
  /** What the album folder's (or download's) name says about the edition. */
  name_clues?: ImportNameClues;
  /** The download's release group, parsed from its name; the review prefills it. */
  release_group?: string;
}

/** Edition words read from a folder or download name: weak clues, marked but never picked by. */
export interface ImportNameClues {
  name: string;
  words?: string[];
  /** A MusicBrainz medium format, e.g. "Digital Media". */
  format?: string;
  country?: string;
  /** Bit depth and sample rate ("24-96"); shown only. */
  quality?: string;
}

/** The tags that tell one edition from another, each the value most files agree on. */
export interface ImportEditionClues {
  barcode?: string;
  catalog_number?: string;
  label?: string;
  country?: string;
  media?: string;
  /** The release's own date, not the original's. */
  date?: string;
}

export interface ImportScan {
  root: string;
  groups: ImportGroup[];
  max_files: number;
}

/** A remote path prefix as a download client reports it, and the same folder on this host. */
export interface PathMapping {
  remote: string;
  local: string;
}

export interface DownloadClientCategory {
  id?: string;
  category: string;
  library_id: string;
  /** Import this category's downloads without a review when the match is certain. */
  auto_import: boolean;
}

/** A configured download client. The password is never returned, only whether one is set. */
export type DownloadClientType = "qbittorrent" | "transmission" | "sabnzbd";

/** Display names, in the order the form offers them. */
export const DOWNLOAD_CLIENT_LABEL: Record<DownloadClientType, string> = {
  qbittorrent: "qBittorrent",
  transmission: "Transmission",
  sabnzbd: "SABnzbd",
};

/** Whether a client type holds a download's files after it finishes (mirrors models.DownloadClientSeeds). */
export const downloadClientSeeds = (type: DownloadClientType) => type !== "sabnzbd";

export interface DownloadClient {
  id: string;
  name: string;
  type: DownloadClientType;
  enabled: boolean;
  base_url: string;
  username: string;
  password_set: boolean;
  path_mappings: PathMapping[];
  categories: DownloadClientCategory[];
  last_poll_at: string | null;
  last_poll_error?: string;
  /** Torrent clients: the category an imported download is moved to; "" leaves it. */
  imported_category: string;
  /** Torrent clients: remove an imported download, files and all, once its share limit stopped it. */
  remove_after_seeding: boolean;
}

export type DownloadState = "ready" | "unavailable" | "imported" | "dismissed";

/** A finished download in a watched category, offered on the Import page. */
export interface DownloadItem {
  id: string;
  client_id: string;
  client_name: string;
  remote_id: string;
  name: string;
  category: string;
  library_id: string;
  /** The download's folder (or file) on this host; remote_path as the client reported it. */
  path: string;
  remote_path: string;
  files: string[];
  size: number;
  state: DownloadState;
  reason?: string;
  /** What happened to each album folder: "queued" | "imported" | "failed". */
  groups: Record<string, string>;
  /** Why auto-import left an album for review, by album folder. */
  notes: Record<string, string>;
  seeding: boolean;
  completed_at: string | null;
  imported_at: string | null;
  albums: number;
  albums_imported: number;
}

export interface DownloadsList {
  clients: {
    id: string;
    name: string;
    enabled: boolean;
    categories: number;
    last_poll_at: string | null;
    last_poll_error?: string;
  }[];
  downloads: DownloadItem[];
}

/** A download's albums, in a folder scan's shape, plus what happened to each. */
export interface DownloadScan extends ImportScan {
  states: Record<string, string>;
  notes: Record<string, string> | null;
  seeding: boolean;
}

export type ImportConflict = "" | "exists" | "duplicate" | "clash" | "other_edition" | "unidentified";

/** One file of an import, as planned by the server. An empty track ID is a skip. */
export interface ImportRow {
  source: string;
  mb_release_track_id: string;
  track_number?: string;
  track_title?: string;
  medium?: number;
  how?: "number" | "order" | "";
  target?: string;
  target_rel?: string;
  conflict?: ImportConflict;
  /** For other_edition: which edition the album folder already holds; for unidentified, how many files. */
  conflict_detail?: string;
  /** Every file importing this row would remove. */
  replaces?: string[];
  replace?: boolean;
  error?: string;
}

export interface ImportPreview {
  release: ReleaseTracks["release"];
  tracks: ReleaseTrack[];
  rows: ImportRow[];
  /** Why the import cannot run as it stands; empty when it can. */
  problem: string;
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
  /** Files waiting on something, by what they wait on; each is one Items filter. */
  attention?: {
    errors: number;
    retrying: number;
    pins_held: number;
    unmatched_wait: number;
  };
  /** What a fresh install still lacks, as counts (see setup.tsx). */
  setup?: {
    managers: number;
    libraries: number;
    indexed_files: number;
    importable_libraries: number;
    download_clients: number;
  };
}

/** One queued or running background job. */
export interface JobView {
  kind: string;
  title: string;
}

export interface ScanStatus {
  running: boolean;
  /** The running job was asked to stop and is finishing the files already in flight. */
  stopping?: boolean;
  /** The job executing right now (a processing run, re-tag, or metadata refresh); absent when idle. */
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
   *  artist folder being worked on. Absent/zero when no run is in flight. */
  total?: number;
  done?: number;
  phase?: string;
  current?: string;
  /** Files in the index right now — the precondition Scan and Tag files share. Zero
   *  means nothing has walked a library yet, which is what those buttons say instead
   *  of running and reporting an honest zero. */
  indexed: number;
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

/**
 * The *Refresh artwork* verb's status. Deliberately not folded into MirrorStatus:
 * images come from the artwork providers rather than MusicBrainz, so none of these
 * numbers is comparable with the metadata ones.
 */
export interface ArtworkStatus {
  running: boolean;
  title?: string;
  started_at?: string;
  finished_at?: string;
  total: number;
  done: number;
  fetched: number;
  fresh: number;
  /** Asked and answered: the provider has no image. The ordinary case, not a failure. */
  missing: number;
  errors: number;
  last_error?: string;
  /** What the cache holds right now. `images` is what a forced pass re-downloads. */
  images: number;
  missing_cached: number;
  /**
   * Whether anything can be fetched at all. Without these the page cannot tell
   * "nothing warmed because it is all current" from "nothing warmed because no
   * provider is configured" — opposite situations that look identical in zeroes.
   */
  covers_enabled: boolean;
  artist_enabled: boolean;
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
  /** Absent when the naming could not render this file's release; path_error says why. */
  path?: PathDiff;
  path_error?: string;
}

/** One field's before/after from a tag write. */
export interface TagChange {
  field: string;
  old: string;
  new: string;
}

/** One file's outcome inside an event: what happened, and exactly what changed. */
/**
 * One counter an event declares about itself. The emitter says which of its numbers
 * matter and what each selects; the UI decides how that looks.
 *
 * `kind` is semantic emphasis, never a colour — an emitter naming a CSS variable would
 * put the design system in the Go package least able to know about it.
 * `filter` names the EventItem.status this counter selects, which is what turns the
 * number into a control over the list below it.
 */
export interface EventStat {
  label: string;
  value: number;
  kind?: "muted" | "notable" | "bad";
  filter?: string;
}

/**
 * What Autotaggerr knows about the MBID on an entity row — the local side of an
 * identifier the event only knows as a UUID.
 *
 * Resolved per request, never stored: the row records what MusicBrainz said at the
 * time, this says what the collection holds now.
 */
export interface EntityRef {
  /** "artist" | "release-group" | "release" — also the path segment on musicbrainz.org. */
  kind: string;
  name?: string;
  artist?: string;
  artist_mb_id?: string;
  group_mb_id?: string;
  /** How many indexed files point at this identifier. */
  files: number;
}

export interface EventItem {
  id: string;
  event_id: string;
  /** A file path, the MBID of an entity, or an album title — see `kind`. */
  path: string;
  /** "" (a file) | "entity" (an MBID) | "album" (a Plex refresh target). */
  kind?: string;
  /** "changed" | "error" | "refreshed" | "gone" | "relinked" | "unknown" | "queued" | "skipped" */
  status: string;
  /** Which stage of the event produced this row; groups release rows apart from files. */
  phase?: string;
  tags_written: number;
  error?: string;
  /** Where a file was before the activity placed it at `path` (a file import). */
  from?: string;
  changes?: TagChange[];
  /** Present on an entity row the collection can name. */
  related?: EntityRef;
}

/** One indexed file behind a MusicBrainz identifier (GET /mb/:mbid/files). */
export interface MBFile {
  path: string;
  library: string;
  status: string;
  error?: string;
  mb_release_id: string;
}

export interface MBFilesPage {
  mb_id: string;
  total: number;
  files: MBFile[];
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
  /** The counters this event wants shown, declared by whatever emitted it. */
  stats?: EventStat[];
  /** Per-file detail. Only the single-event endpoint returns it, never the feed. */
  items?: EventItem[];
  /**
   * Set on an activity a run spawned: the run that spawned it. The feed is flat — every
   * activity is its own row — so this is what relates the rows rather than nesting them.
   */
  parent_id?: string;
  /**
   * The activities this run spawned, oldest first. Only the single-event endpoint
   * returns them; the feed lists them as rows of their own.
   */
  children?: Event[];
  /** How many activities this run spawned, so a row can offer to narrow the feed to them. */
  child_count?: number;
  /** The run this activity came from, set on every row that has one. */
  parent_title?: string;
}

export interface EventsPage {
  total: number;
  limit: number;
  offset: number;
  events: Event[];
  /**
   * How many events each filter option would return. Each facet is computed with the
   * *other* filters applied but not its own, so the type list stays a list of what you
   * could switch to rather than collapsing to the one already chosen.
   */
  facets?: {
    type?: Record<string, number>;
    status?: Record<string, number>;
  };
}

export interface CollectionArtist {
  mb_id: string;
  name: string;
  /** Set when a manager sync found the manager no longer lists this artist. */
  manager_missing_at?: string | null;
  /** Following is *stored*; see follow_governs for whether it currently decides. */
  monitored: boolean;
  /**
   * Whether the native follow settings can decide anything in this view. False in
   * Lidarr's library, or where Lidarr governs the artist and no single native library
   * could take a follow — the flag is kept, and the controls are shown frozen.
   */
  follow_governs: boolean;
  /**
   * Whether wants and editions may be edited in this view, for edit_library_id.
   * False in Lidarr's library, where every write is a 409, so the controls are
   * disabled and edit_blocked says why.
   */
  identity_editable: boolean;
  /**
   * Whether a manager currently governs this artist, so taking that authority back
   * is a meaningful thing to offer. Derived server-side alongside the two flags
   * above, so the button and the endpoint cannot disagree about when it applies.
   */
  detachable: boolean;
  /**
   * Whether the user has already taken authority back from the manager. Stored, not
   * derived: managed_by is recomputed from the library's manager on every scan, so
   * this is what keeps a detach from being undone by the next one.
   */
  manager_detached: boolean;
  managed_by: string;
  /** "library" = materialised from files on disk; "manual" = added by hand. */
  origin: string;
  /** Comma-separated primary types that following auto-wants. Empty = Album,EP. */
  follow_types: string;
  /** Whether live albums, compilations and remixes count when following. */
  follow_secondary: boolean;
  /**
   * Earliest release year a follow wants; 0 means no cutoff (the whole back
   * catalogue). A year rather than a date because that is the precision MusicBrainz
   * release dates have. Releases MusicBrainz has no date for are excluded once a
   * cutoff is set.
   */
  follow_from_year: number;
  /**
   * The native libraries following wants into, with the one set of settings; empty
   * means every library Autotaggerr decides for. Prefer `following_in`, which applies
   * that rule.
   */
  follow_library_ids: string[];
  /** The native libraries following actually wants into, as the server reads the rule. */
  following_in?: string[];
  /**
   * Whether following decides what is wanted in this view: stored, governing, and
   * — in one library's view — pointed at that library. Prefer it to `monitored`.
   */
  following: boolean;
  /** The library this view reads, or null for every library. */
  view_library_id: string | null;
  /** The library an edit made in this view is for; null when tied to none. */
  edit_library_id: string | null;
  /** Why nothing is editable here, when it is not. */
  edit_blocked?: "lidarr" | "unknown" | "choose_library" | "no_native_library";
  /** The libraries the artist's pages can be read in. Only the artist's own pages fill it. */
  libraries?: ArtistLibrary[];
  last_synced_at: string | null;
  /**
   * How many release-groups of the MusicBrainz discography this artist's follow shape
   * takes in, followed or not — the denominator of "3 of 11 on disk". Null read time:
   * the discography has not been read yet.
   */
  catalogue_count: number;
  catalogue_read_at: string | null;
  owned_count?: number;
  complete_count?: number;
  partial_count?: number;
  missing_count?: number;
  mismatch_count?: number;
  picked_count?: number;
  /** Libraries holding any of this artist's albums (list view only). */
  library_ids?: string[];
}

/**
 * What one library holds of an edition or an album. For an album, owned_tracks is
 * that library's best edition — the same rule as the album's own summary.
 */
/** Who decides for an artist's files in one library. */
export type Authority = "lidarr" | "autotaggerr" | "unknown";

/** One library as an artist's pages see it. */
export interface ArtistLibrary {
  library_id: string;
  name: string;
  authority: Authority;
  /** Whether any of the artist's albums has files here. */
  holds: boolean;
}

export interface LibraryCopy {
  library_id: string;
  name: string;
  owned_tracks: number;
  files: number;
}

/** How the disk view and the manager's catalog view disagree, if at all. */
export type Discrepancy = "" | "unmapped" | "stale_catalog" | "not_indexed" | "no_edition";

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
   * hand and editable here; "auto" = derived from following the artist, or from the
   * rebuild narrowing a want to the edition you own; "manager" = the library's
   * manager (Lidarr) monitors this album, or selected this edition. Only an explicit
   * want is a live toggle on the row; the derived ones are shown as frozen state.
   *
   * This, not "are there desire rows", is what says a want is the user's: a derived
   * want has rows too (the edition it narrowed to), and reading their presence as
   * authorship offered controls that the API rejects.
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
  /**
   * False when this view's authority is Lidarr (or no single native library can take
   * an edit): the want and edition controls on the row then render as state.
   */
  identity_editable: boolean;
  /** Where the album lives: one entry per library holding files of it. */
  libraries: LibraryCopy[];
  /** The managers holding the album, as links into each. Set by the discography only. */
  links?: ExternalLink[];
}

/** One MusicBrainz edition of a release-group, plus what is owned of *that* edition. */
export interface Edition extends ReleaseSearchResult {
  owned: boolean;
  owned_tracks: number;
  owned_total_tracks: number;
  complete: boolean;
  /** Files resolving to this edition, across every library. More than owned_tracks = copies. */
  files: number;
  /** Which libraries hold this edition, and how much of it. */
  libraries: LibraryCopy[];
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

/** A link to where an entity lives outside Autotaggerr — a Lidarr, a Plex library. */
export interface ExternalLink {
  kind: string;
  /** The manager or library, to tell two of one kind apart. */
  name: string;
  url: string;
}

export interface ArtistDetail {
  artist: CollectionArtist;
  release_groups: CollectionReleaseGroup[];
  desires: CollectionDesire[];
  /** The managers holding this artist, as links into each one's UI. */
  links?: ExternalLink[];
  /**
   * Present only when some of the artist's albums carry a MusicBrainz ID that resolves
   * nowhere while a manager still lists them — what a manager refresh repairs.
   */
  repair?: {
    albums: string[];
    /** Managers whose artist-refresh setting means nobody can be asked. */
    held_by?: string[];
    /** A refresh for this artist is already queued or running. */
    queued: boolean;
  };
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

/** A want. Empty release_mb_id = any release of the group will do. */
export interface CollectionDesire {
  id: string;
  artist_mb_id: string;
  release_group_mb_id: string;
  release_mb_id: string;
  recording_mb_ids: string[] | null;
  /** The library this want is for; the nil UUID for a want tied to no library. */
  library_id: string;
  /**
   * Who authored it, and therefore who may rewrite it: "manual" is the user's and
   * is never recomputed; "auto" was narrowed from an any-edition want to the edition
   * whose files landed; "manager" mirrors Lidarr's monitored release. Prefer the
   * release-group's wanted_source for what to render — this says which single row
   * came from where.
   */
  source: "manual" | "auto" | "manager";
}

/**
 * An identity change reported by a metadata source: an entity merged into another
 * upstream, or one whose ID resolves nowhere. Open rows (pending, and failed ones
 * being re-tried) are the queue; everything else is history.
 *
 * Most of this is computed at read time by the server rather than stored, because it
 * describes state that moves independently of the change — files arrive, a manager
 * stops listing an album, a repair is attempted. See migration.Review.
 */
export interface MusicbrainzMigration {
  id: string;
  entity_type: string;
  old_mb_id: string;
  new_mb_id: string;
  kind: string;
  status: string;
  name: string;
  /** The reporting source, written the way it spells itself ("MusicBrainz"). */
  source_label: string;
  affected_files: number;
  affected_desires: number;
  touches_pinned: boolean;
  detected_at: string;
  applied_at: string | null;
  error?: string;

  /** How it left the queue, and — when the word is not enough — why. */
  resolution?: "approved" | "automatic" | "dismissed" | "external";
  resolution_detail?: string;
  resolved_at?: string | null;

  /** Set while a manager refresh queued for this row's artist is in flight. */
  repair_queued_at?: string | null;
  /** Set once the manager has been asked, whatever the answer. */
  repair_attempted_at?: string | null;

  artist_mb_id?: string;
  artist_name?: string;
  /** Open rows waiting on the same artist, this one included. One refresh settles all. */
  artist_open?: number;
  /** Where the surviving entity of a merge or re-key sits in the collection, when it does. */
  target_artist_mb_id?: string;
  target_release_group_mb_id?: string;

  /** Files under this entity — what you own, not what approving would rewrite. */
  files_on_disk: number;
  editions?: number;
  owned: boolean;
  in_catalog: boolean;
  /** Why approving would not complete right now, read before the press. */
  blocker?: string;
  /** The one blocker a refresh can clear: approve asks the manager instead of applying. */
  needs_manager_refresh: boolean;
  /** Ready-to-render sentences: what is wrong, and what approving does about it. */
  problem: string;
  effect: string;
}

export interface MigrationList {
  migrations: MusicbrainzMigration[];
  /** Rows matching the filter, not rows in this page — the pager needs both. */
  total: number;
  limit: number;
  offset: number;
  /** Pending across the whole table, for the badge. */
  pending: number;
}

/** What approve answers with when it queued a manager refresh instead of applying. */
export interface MigrationRepairQueued {
  queued?: boolean;
  artist_mbid?: string;
  artist_name?: string;
  message?: string;
}

/** Which categories of migration wait for approval instead of applying themselves. */
export interface MigrationPolicy {
  review_releases: boolean;
  review_artists: boolean;
  review_pinned: boolean;
  review_deletions: boolean;
  entity_types: string[];
}

/**
 * One setting, as the server describes it. The server owns the whole description —
 * label, help, type, and whether an edit takes effect now or at the next start — so
 * the page renders from it rather than restating the field list in TypeScript, where
 * it would drift the first time a key is added.
 *
 * `value` is absent for secrets: they are fetched one at a time, on request.
 */
export interface SettingsField {
  key: string;
  label: string;
  help?: string;
  /** string | int | bool | select | cron | secret | list */
  type: string;
  /** live = applied on save · restart = takes effect at next start · readonly */
  tier: string;
  options?: string[];
  placeholder?: string;
  value?: string | number | boolean | string[];
  secret_set?: boolean;
  editable: boolean;
}

export interface SettingsSection {
  id: string;
  title: string;
  description?: string;
  fields: SettingsField[];
}

export interface SettingsView {
  sections: SettingsSection[];
}

/** What a save did: what moved, what took effect now, what waits for a restart. */
export interface SettingsSaveResult {
  changed: string[];
  applied: string[];
  restart_required: string[];
}

/** GET /tempo/status — the *Analyse tempo* verb. See docs/tempo.md. */
export interface TempoStatus {
  running: boolean;
  /** Waiting between files while a file-writing job runs. */
  paused: boolean;
  started_at?: string;
  finished_at?: string;
  total: number;
  done: number;
  analysed: number;
  fresh: number;
  restamped: number;
  carried: number;
  no_tempo: number;
  failed: number;
  last_error?: string;
  /**
   * Whether a pass could do anything: the detector installed, and how many enabled
   * libraries opted in. Without these, "nothing to do" and "cannot do it" look alike.
   */
  detector_available: boolean;
  libraries: number;
  /** Files with a tempo on record, between passes too. */
  stored: number;
}

/** One file's stored analysis (models.TrackTempo). */
export interface TrackTempo {
  path: string;
  bpm: number;
  confidence: number;
  beats: number;
  detector: string;
  detector_version: string;
  error?: string;
  analysed_at: string;
}

/** GET /library-items/:id/tempo — the stored result plus what will happen to it. */
export interface ItemTempo {
  tempo: TrackTempo | null;
  library_analyses: boolean;
  /** Whether the library's tagger profile writes tags at all. */
  writes_tags: boolean;
  /** What the library does with a tempo; "" when it writes none. */
  bpm_write: BPMWrite;
  /** The effective floor, default already applied. */
  min_confidence: number;
}

/** What the collection does by itself: whether an artist found in your files is followed. */
export interface CollectionPolicy {
  new_artist_follow: "off" | "new" | "all";
  follow_types: string;
  follow_secondary: boolean;
}

/** What an organise covers: exactly one of the three. */
export interface OrganiseScope {
  library_id?: string;
  artist_mb_id?: string;
  release_group_mb_id?: string;
}

/** One file of an organise: a proposed move, or one that cannot be made (reason). */
export interface OrganiseRow {
  item_id: string;
  library_id: string;
  from: string;
  to?: string;
  from_rel: string;
  to_rel?: string;
  conflict?: "" | "clash" | "exists";
  reason?: string;
  skip?: boolean;
}

/** POST /organise/preview — the dry run. */
export interface OrganisePlan {
  moves: OrganiseRow[];
  skipped: OrganiseRow[];
  in_place: number;
  libraries: string[];
  /** Covers, cue sheets and logs that move with a whole album. */
  extras: { from: string; to: string; from_rel: string; to_rel: string; exists?: boolean }[];
}
