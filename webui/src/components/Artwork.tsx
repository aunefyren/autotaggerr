import { createContext, ReactNode, useContext, useState } from "react";
import { api } from "../api";
import { useFetch } from "../hooks";

/**
 * Album covers and artist portraits, served by the app's own /artwork proxy.
 *
 * Artwork is structure here, not decoration: it is what makes a row of releases
 * scannable at a glance instead of a wall of titles. So it has to behave like
 * structure — never shift the layout, never block a render, and always occupy its
 * square whether or not an image exists. A missing cover falls back to a monogram
 * tile of the same size.
 *
 * The proxy is unauthenticated by necessity (an <img> tag cannot send a bearer
 * token), which is why these URLs carry no credentials of any kind.
 */
export type ArtworkEntity = "release-group" | "release" | "artist";

/**
 * Which artwork kinds a request could actually return. When a provider is disabled
 * (or, for fanart, keyless) every request for its kind comes back empty, so we render
 * the monogram directly and never fire the <img> — a page of hundreds of artists is
 * the difference between zero requests and hundreds of pointless ones. Defaults match
 * the server's seed (covers on, artist art opt-in) so the common install shows covers
 * immediately and never attempts artist art it does not have; the fetched value
 * corrects both a beat later.
 *
 * This covers the whole-provider case only. With fanart configured, most individual
 * artists still have no image there — the server answers those 204, and the <img>
 * error event below is what falls back.
 */
export interface ArtworkCapabilities {
  cover: boolean;
  artist: boolean;
}

const DEFAULT_CAPABILITIES: ArtworkCapabilities = { cover: true, artist: false };

const ArtworkCapabilitiesContext = createContext<ArtworkCapabilities>(DEFAULT_CAPABILITIES);

/** Fetches artwork capabilities once and shares them with every Artwork below. */
export function ArtworkCapabilitiesProvider({ children }: { children: ReactNode }) {
  const { data } = useFetch<ArtworkCapabilities>(() => api.get("/artwork-capabilities"));
  return (
    <ArtworkCapabilitiesContext.Provider value={data ?? DEFAULT_CAPABILITIES}>
      {children}
    </ArtworkCapabilitiesContext.Provider>
  );
}

export function artworkUrl(entity: ArtworkEntity, mbid: string, size: number, kind?: string): string {
  const params = new URLSearchParams({ size: String(size) });
  if (kind) params.set("kind", kind);
  return `/api/v1/artwork/${entity}/${mbid}?${params.toString()}`;
}

/**
 * Initials for the monogram fallback. Two letters from two words, else two from
 * one — enough to tell rows apart without pretending to be an image.
 */
function monogram(name: string): string {
  const words = name.trim().split(/\s+/).filter(Boolean);
  if (words.length === 0) return "♫";
  if (words.length === 1) return words[0].slice(0, 2).toUpperCase();
  return (words[0][0] + words[1][0]).toUpperCase();
}

export function Artwork({
  entity,
  mbid,
  name,
  px,
  kind,
  size,
  className = "",
}: {
  entity: ArtworkEntity;
  mbid: string;
  /** Used for the alt text and the monogram fallback. */
  name: string;
  /** Rendered edge length. The fetched size is requested separately, so a small
   *  thumbnail does not pull a 500px image. */
  px: number;
  kind?: string;
  size?: number;
  className?: string;
}) {
  const caps = useContext(ArtworkCapabilitiesContext);
  const [failed, setFailed] = useState(false);
  // Keyed by mbid so navigating between artists re-attempts rather than inheriting
  // the previous one's failure.
  const key = `${entity}:${mbid}:${kind ?? ""}`;

  // The provider for this entity's kind may be off, in which case the request can
  // only come back empty — fall straight to the monogram and never touch the network.
  const supported = entity === "artist" ? caps.artist : caps.cover;

  if (!mbid || failed || !supported) {
    return (
      <span
        className={`artwork artwork-fallback ${className}`}
        style={{ width: px, height: px, fontSize: Math.max(10, Math.round(px * 0.36)) }}
        aria-hidden="true"
      >
        {monogram(name)}
      </span>
    );
  }

  return (
    <img
      key={key}
      className={`artwork ${className}`}
      style={{ width: px, height: px }}
      src={artworkUrl(entity, mbid, size ?? Math.min(1200, Math.max(250, px * 2)), kind)}
      alt={`${name} artwork`}
      // Lazy by default: a collection of several hundred artists must not fire
      // several hundred requests to paint the first screenful.
      loading="lazy"
      decoding="async"
      onError={() => setFailed(true)}
    />
  );
}

/**
 * The artist backdrop. The one place in the app where an image is allowed to be
 * atmosphere rather than information — see the style guide's named exception. It
 * renders nothing at all when there is no image, so the header never shows an
 * empty tinted band.
 */
export function ArtistBackdrop({ mbid }: { mbid: string }) {
  const caps = useContext(ArtworkCapabilitiesContext);
  const [failed, setFailed] = useState(false);
  if (!mbid || failed || !caps.artist) return null;
  return (
    <img
      className="entity-backdrop"
      src={artworkUrl("artist", mbid, 1200, "background")}
      alt=""
      aria-hidden="true"
      loading="lazy"
      decoding="async"
      onError={() => setFailed(true)}
    />
  );
}
