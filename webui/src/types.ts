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
}

export interface LibraryItem {
  id: string;
  library_id: string;
  path: string;
  mb_release_id: string;
  correlation_source: string;
  status: string;
  last_tagged_at: string | null;
}

export interface Health {
  name: string;
  version: string;
  counts: Record<string, number>;
}

export interface ScanStatus {
  running: boolean;
  started_at?: string;
  finished_at?: string;
  processed: number;
  unchanged: number;
  changed: number;
  tags_written: number;
  errors: number;
  last_error?: string;
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

export interface Event {
  id: string;
  type: string;
  status: string;
  started_at: string;
  finished_at: string | null;
  title: string;
  summary: string;
  details: Record<string, unknown> | null;
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
  monitored: boolean;
  managed_by: string;
  last_synced_at: string | null;
  owned_count?: number;
  complete_count?: number;
  partial_count?: number;
  missing_count?: number;
  mismatch_count?: number;
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
}

export interface ArtistDetail {
  artist: CollectionArtist;
  release_groups: CollectionReleaseGroup[];
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
