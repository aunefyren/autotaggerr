import { FormEvent, useState } from "react";
import { api, errMsg } from "../api";
import { ArtistSearchResult } from "../types";
import { Modal } from "./ui";
import { MBLink } from "./MBLink";
import { useToast } from "../toast";

/**
 * Adds an artist to the collection before any of their files are owned — the thing
 * a rebuild-from-disk can never do. Monitoring the artist afterwards is what pulls
 * in their discography.
 */
export function AddArtistModal({ onClose, onAdded }: { onClose: () => void; onAdded: () => void }) {
  const toast = useToast();
  const [query, setQuery] = useState("");
  const [results, setResults] = useState<ArtistSearchResult[] | null>(null);
  const [searching, setSearching] = useState(false);
  const [busy, setBusy] = useState(false);

  const search = async (e: FormEvent) => {
    e.preventDefault();
    setSearching(true);
    try {
      setResults(await api.get<ArtistSearchResult[]>(`/search/artists?q=${encodeURIComponent(query)}`));
    } catch (e) {
      toast("err", errMsg(e));
    } finally {
      setSearching(false);
    }
  };

  const add = async (r: ArtistSearchResult) => {
    setBusy(true);
    try {
      await api.post("/artists", { mb_id: r.id, name: r.name });
      toast("ok", `Added ${r.name}`);
      onAdded();
    } catch (e) {
      toast("err", errMsg(e));
      setBusy(false);
    }
  };

  return (
    <Modal title="Add artist" onClose={onClose} wide>
      <div className="stack">
        <form onSubmit={search} className="row" style={{ gap: 8 }}>
          <input
            className="input"
            style={{ flex: 1 }}
            value={query}
            onChange={(e) => setQuery(e.target.value)}
            placeholder="Artist name…"
            autoFocus
          />
          <button className="btn btn-primary btn-sm" disabled={searching || !query.trim()}>
            {searching ? "Searching…" : "Search"}
          </button>
        </form>

        {results && results.length === 0 && (
          <div className="dim" style={{ fontSize: 12 }}>No artists matched.</div>
        )}

        {results && results.length > 0 && (
          <div className="scroll">
            <div style={{ display: "flex", flexDirection: "column", gap: 2 }}>
              {results.map((r) => (
                <div
                  key={r.id}
                  className="row"
                  style={{ justifyContent: "space-between", padding: "6px 0", borderBottom: "1px solid var(--border)" }}
                >
                  <div>
                    <div style={{ color: "var(--text)" }}>
                      {r.name}
                      {r.disambiguation && <span className="dim"> ({r.disambiguation})</span>}
                    </div>
                    <div className="dim" style={{ fontSize: 11 }}>
                      {[r.type, r.country].filter(Boolean).join(" · ")}
                    </div>
                  </div>
                  <div className="row" style={{ gap: 8 }}>
                    <MBLink entity="artist" mbid={r.id} />
                    <button className="btn btn-secondary btn-sm" disabled={busy} onClick={() => add(r)}>Add</button>
                  </div>
                </div>
              ))}
            </div>
          </div>
        )}
      </div>
    </Modal>
  );
}
