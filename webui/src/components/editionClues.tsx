import { ReactNode, useEffect, useState } from "react";
import { api } from "../api";
import { ImportEditionClues, ImportNameClues, ReleaseSearchResult } from "../types";

/**
 * What a set of files says about which edition it is, and how each edition MusicBrainz
 * lists agrees with it. Shared by the import's Browse step, the release search, and
 * attach. The tags (`edition`) are the strong source; the folder or download name
 * (`name`) is weak — typed by whoever made the download, and shared by many editions.
 */
export interface EditionClues {
  edition?: ImportEditionClues | null;
  name?: ImportNameClues | null;
}

/**
 * What library files say about their edition, for attach: the files are indexed, so
 * no scan has read their tags, and the server reads them on request. Undefined until
 * it answers; a failure only means nothing is marked, so it is not reported.
 */
export function useEditionClues(itemIds: string[]): EditionClues | undefined {
  const [clues, setClues] = useState<EditionClues>();
  const key = itemIds.join(",");
  useEffect(() => {
    let live = true;
    api
      .post<EditionClues & { name_clues?: ImportNameClues | null }>("/attach/clues", { item_ids: itemIds })
      .then((res) => live && setClues({ edition: res.edition, name: res.name_clues }))
      .catch(() => undefined);
    return () => {
      live = false;
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [key]);
  return clues;
}

/** Loose equality for "the album the tags name": case, accents and punctuation aside. */
export function looseEq(a?: string, b?: string): boolean {
  const norm = (s?: string) =>
    (s ?? "").normalize("NFD").replace(/[\u0300-\u036f]/g, "").toLowerCase().replace(/[^\p{L}\p{N}]+/gu, "");
  return norm(a) !== "" && norm(a) === norm(b);
}

/** Digits with leading zeros dropped: 602478023293 (UPC-A), 0602478023293 (EAN-13)
 *  and 00602478023293 (GTIN-14) are one barcode written three ways. */
export function barcodeKey(s?: string): string {
  return (s ?? "").replace(/\D/g, "").replace(/^0+/, "");
}

/** Catalog numbers are written with and without spaces and dashes ("KAB 1", "KAB-1"). */
export function catalogKey(s?: string): string {
  return (s ?? "").toLowerCase().replace(/[^\p{L}\p{N}]+/gu, "");
}

/** A medium format agrees with a clue when it is the same format, or a kind of it:
 *  `12" Vinyl` is Vinyl, while `Hybrid SACD` is not CD. */
function formatMatches(format?: string, clue?: string): boolean {
  if (!format || !clue) return false;
  if (looseEq(format, clue)) return true;
  const words = (s: string) => s.toLowerCase().split(/[^\p{L}\p{N}]+/u).filter(Boolean);
  const have = words(format);
  return words(clue).every((w) => have.includes(w));
}

/** Which of an edition's facts agree with what the files say about theirs. */
export interface Agreement {
  barcode: boolean;
  catalog: boolean;
  label: boolean;
  date: boolean;
  country: boolean;
  format: boolean;
  /** One of the name's edition words ("Deluxe", "Remaster") is in the disambiguation. */
  disambiguation: boolean;
}

export function editionAgreement(e: ReleaseSearchResult, clues?: EditionClues): Agreement {
  const tags = clues?.edition ?? undefined;
  const name = clues?.name ?? undefined;
  const info = e["label-info"] ?? [];
  const sameDate = (tag?: string, mb?: string) => !!tag && !!mb && (tag === mb || (tag.length === 4 && mb.startsWith(tag)));
  const sameCountry = (clue?: string) => !!clue && clue.toUpperCase() === (e.country ?? "").toUpperCase();
  const disambiguation = (e.disambiguation ?? "").toLowerCase();
  return {
    barcode: !!tags?.barcode && barcodeKey(tags.barcode) !== "" && barcodeKey(tags.barcode) === barcodeKey(e.barcode),
    catalog:
      !!tags?.catalog_number && catalogKey(tags.catalog_number) !== "" && info.some((l) => catalogKey(l["catalog-number"]) === catalogKey(tags.catalog_number)),
    label: !!tags?.label && info.some((l) => looseEq(l.label?.name, tags.label)),
    date: sameDate(tags?.date, e.date),
    country: sameCountry(tags?.country) || sameCountry(name?.country),
    format: (e.media ?? []).some((m) => formatMatches(m.format, tags?.media) || formatMatches(m.format, name?.format)),
    disambiguation: disambiguation !== "" && (name?.words ?? []).some((w) => disambiguation.includes(w.toLowerCase())),
  };
}

/** A barcode or catalog number is printed on one pressing; everything else is shared
 *  by many. Only the first kind is worth leading the eye to — or picking by. */
export function strongAgreement(a: Agreement): boolean {
  return a.barcode || a.catalog;
}

export function editionTracks(e: ReleaseSearchResult): number {
  return (e.media ?? []).reduce((n, m) => n + (m["track-count"] ?? 0), 0);
}

/**
 * The edition the tags name, when they name exactly one: the one edition whose barcode
 * matches — or, if no edition's barcode does, whose catalog number does — and whose
 * track count equals the files. Anything less certain is marked, not picked. `why`
 * says which fact decided it, for the Selected line.
 */
export function editionTheTagsName(
  editions: ReleaseSearchResult[],
  clues: EditionClues | undefined,
  files: number,
): { edition: ReleaseSearchResult; why: "barcode" | "catalog number" } | null {
  const agreements = editions.map((e) => ({ e, a: editionAgreement(e, clues) }));
  const byBarcode = agreements.filter((x) => x.a.barcode);
  const pool = byBarcode.length > 0 ? byBarcode : agreements.filter((x) => x.a.catalog);
  if (pool.length !== 1 || editionTracks(pool[0].e) !== files) return null;
  return { edition: pool[0].e, why: byBarcode.length > 0 ? "barcode" : "catalog number" };
}

/** A value in the list's own colour, or in the diff's green where the files agree with it. */
export function Agrees({ on, children }: { on: boolean; children: ReactNode }) {
  return on ? (
    <span className="pick-match" style={{ fontSize: "inherit" }} title="Matches what the files say">
      {children}
    </span>
  ) : (
    <>{children}</>
  );
}

/** An edition's labels, catalog numbers and barcode on one line, each marked where the
 *  tags agree. Nothing when MusicBrainz lists none. */
export function EditionPrinting({ e, clues, a }: { e: ReleaseSearchResult; clues?: EditionClues; a: Agreement }) {
  const tags = clues?.edition ?? undefined;
  const labels = (e["label-info"] ?? []).filter((l) => l.label?.name || l["catalog-number"]);
  if (labels.length === 0 && !e.barcode) return null;
  return (
    <span className="pick-sub">
      {labels.map((l, i) => (
        <span key={i}>
          {i > 0 && " · "}
          {l.label?.name && <Agrees on={a.label && looseEq(l.label.name, tags?.label)}>{l.label.name}</Agrees>}
          {l.label?.name && l["catalog-number"] && " "}
          {l["catalog-number"] && (
            <span className="mono">
              <Agrees on={a.catalog && catalogKey(l["catalog-number"]) === catalogKey(tags?.catalog_number)}>{l["catalog-number"]}</Agrees>
            </span>
          )}
        </span>
      ))}
      {e.barcode && (
        <>
          {labels.length > 0 && " · "}
          <span className="mono">
            <Agrees on={a.barcode}>{e.barcode}</Agrees>
          </span>
        </>
      )}
    </span>
  );
}

/**
 * What the files say about their edition, as facts: the tags on one line, the name's
 * edition words on the next, so the green in a list below can be traced to its source.
 */
export function EditionFacts({ clues }: { clues?: EditionClues }) {
  const tags = clues?.edition;
  const name = clues?.name;
  return (
    <>
      {tags && (
        <div className="row" style={{ gap: 8, flexWrap: "wrap", fontSize: 12 }}>
          <span className="eyebrow">Edition tags</span>
          {tags.barcode && <span className="fact">Barcode <b className="mono">{tags.barcode}</b></span>}
          {tags.catalog_number && <span className="fact">Cat. no. <b className="mono">{tags.catalog_number}</b></span>}
          {tags.label && <span className="fact">Label <b>{tags.label}</b></span>}
          {tags.date && <span className="fact">Released <b className="mono">{tags.date}</b></span>}
          {tags.country && <span className="fact">Country <b className="mono">{tags.country}</b></span>}
          {tags.media && <span className="fact">Format <b>{tags.media}</b></span>}
        </div>
      )}
      {name && (
        <div className="row" style={{ gap: 8, flexWrap: "wrap", fontSize: 12 }}>
          <span className="eyebrow" title={name.name}>The name says</span>
          {(name.words ?? []).map((w) => <span key={w} className="fact"><b>{w}</b></span>)}
          {name.format && <span className="fact">Format <b>{name.format}</b></span>}
          {name.country && <span className="fact">Country <b className="mono">{name.country}</b></span>}
          {name.quality && (
            <span className="fact" title="Shown only — MusicBrainz lists no bit depth or sample rate to match">
              <b className="mono">{name.quality}</b> <span className="dim">· not on MusicBrainz</span>
            </span>
          )}
        </div>
      )}
    </>
  );
}
