import { useEffect, useState } from "react";
import { api, errMsg } from "../api";
import { IdentifyAvailability, IdentifyMatch, LibraryItem, ReleaseTracks } from "../types";
import { Modal } from "./ui";
import { MBLink } from "./MBLink";
import { ReleaseSearch, guessFields } from "./ReleaseSearch";
import { useToast } from "../toast";

export function AttachModal({
  item,
  onClose,
  onAttached,
}: {
  item: LibraryItem;
  onClose: () => void;
  onAttached: () => void;
}) {
  const toast = useToast();
  const [release, setRelease] = useState<ReleaseTracks | null>(null);
  const [loadingTracks, setLoadingTracks] = useState(false);
  const [busy, setBusy] = useState(false);

  // Fingerprint identification, if it is set up at all. Probed once so the button
  // can carry its own explanation rather than being an action that always fails.
  const [fingerprinting, setFingerprinting] = useState<IdentifyAvailability | null>(null);
  const [matches, setMatches] = useState<IdentifyMatch[] | null>(null);
  const [identifying, setIdentifying] = useState(false);
  const [suggestedRecording, setSuggestedRecording] = useState("");

  useEffect(() => {
    api.get<IdentifyAvailability>("/identify").then(setFingerprinting).catch(() => setFingerprinting(null));
  }, []);

  const pickRelease = async (mbid: string, recordingId = "") => {
    setLoadingTracks(true);
    setSuggestedRecording(recordingId);
    try {
      setRelease(await api.get<ReleaseTracks>(`/releases/${mbid}/tracks`));
    } catch (e) {
      toast("err", errMsg(e));
    } finally {
      setLoadingTracks(false);
    }
  };

  /**
   * Asks AcoustID what this file is. The answer is autofill, never an action: a
   * recording appears on many releases, so the suggestion opens the normal picker
   * with the likely track highlighted and a human still confirms it.
   */
  const identify = async () => {
    setIdentifying(true);
    try {
      const res = await api.post<{ matches: IdentifyMatch[] | null }>(`/library-items/${item.id}/identify`, {});
      setMatches(res.matches ?? []);
    } catch (e) {
      toast("err", errMsg(e));
    } finally {
      setIdentifying(false);
    }
  };

  const attach = async (trackId: string) => {
    if (!release) return;
    setBusy(true);
    try {
      const res = await api.post<{ tags_written: number; warning?: string }>(
        `/library-items/${item.id}/attach`,
        { mb_release_id: release.release.mb_id, mb_release_track_id: trackId }
      );
      if (res.warning) toast("info", res.warning);
      else toast("ok", `Attached — ${res.tags_written} tag${res.tags_written === 1 ? "" : "s"} written`);
      onAttached();
    } catch (e) {
      toast("err", errMsg(e));
      setBusy(false);
    }
  };

  return (
    <Modal title="Attach to a release" onClose={onClose} wide>
      <div className="stack">
        <div>
          <div className="eyebrow" style={{ marginBottom: 4 }}>File</div>
          <span className="path">{item.path}</span>
        </div>

        {!release && fingerprinting && (
          <div className="row" style={{ justifyContent: "space-between", gap: 8, flexWrap: "wrap" }}>
            <span className="dim" style={{ fontSize: 11 }}>
              {fingerprinting.available
                ? "Not sure what this is? Identify it from the audio itself."
                : fingerprinting.reason}
            </span>
            <button
              className="btn btn-secondary btn-sm"
              disabled={!fingerprinting.available || identifying}
              title={fingerprinting.available ? "Fingerprint the audio and suggest matches" : fingerprinting.reason}
              onClick={identify}
            >
              {identifying ? "Listening…" : "Identify by audio"}
            </button>
          </div>
        )}

        {!release && matches !== null && matches.length === 0 && (
          <div className="dim" style={{ fontSize: 11 }}>
            {/* Failing closed is the design: no confident answer is reported as no
                answer, never as a best guess. */}
            No confident match. AcoustID either does not know this recording, or every
            candidate was too uncertain to offer. Search for the release instead.
          </div>
        )}

        {!release && matches !== null && matches.length > 0 && (
          <div className="stack" style={{ gap: 6 }}>
            <div className="eyebrow">Suggestions · {matches.length}</div>
            {matches.slice(0, 5).map((m) => (
              <div
                key={m.recording_mb_id + m.release_mb_id}
                className="row"
                style={{
                  justifyContent: "space-between", gap: 8, padding: "6px 0",
                  borderBottom: "1px solid var(--border)",
                  cursor: m.release_mb_id ? "pointer" : "default",
                  opacity: m.release_mb_id ? 1 : 0.6,
                }}
                onClick={() => m.release_mb_id && pickRelease(m.release_mb_id, m.recording_mb_id)}
              >
                <div>
                  <div style={{ color: "var(--text)" }}>
                    {m.title}
                    {m.artist && <span className="dim"> — {m.artist}</span>}
                  </div>
                  <div className="dim" style={{ fontSize: 11 }}>
                    {[m.release_title || "no release", m.release_year || "", (m.reasons ?? []).join(", ")]
                      .filter(Boolean)
                      .join(" · ")}
                  </div>
                </div>
                <span className="dim mono" style={{ fontSize: 11 }} title="Fingerprint and folder agreement">
                  {Math.round(m.confidence * 100)}%
                </span>
              </div>
            ))}
            <div className="dim" style={{ fontSize: 11 }}>
              Picking one opens its tracklist with the suggested track marked. Nothing is written
              until you attach.
            </div>
          </div>
        )}

        {!release && (
          <ReleaseSearch initialFields={guessFields(item.path)} onPick={pickRelease} picking={loadingTracks} />
        )}

        {loadingTracks && <div className="muted">Loading tracklist…</div>}

        {release && (
          <div className="stack">
            <div className="row" style={{ justifyContent: "space-between" }}>
              <div>
                <div style={{ color: "var(--text)" }}>{release.release.title}</div>
                <div className="dim" style={{ fontSize: 11 }}>
                  {[release.release.date?.slice(0, 4), release.release.country, release.release.disambiguation]
                    .filter(Boolean)
                    .join(" · ")}
                </div>
              </div>
              <div className="row" style={{ gap: 8 }}>
                <MBLink entity="release" mbid={release.release.mb_id} />
                <button className="btn btn-ghost btn-sm" onClick={() => setRelease(null)}>Back to results</button>
              </div>
            </div>

            <div className="dim" style={{ fontSize: 11 }}>
              Pick the track this file is. Tags are written immediately.
              {suggestedRecording && " The suggested track is marked, but it is still your call."}
            </div>

            <div className="scroll">
              {release.tracks.map((t) => {
                // Matched on the *recording*, which is what a fingerprint identifies;
                // the release-scoped track ID would not survive picking another edition.
                const suggested = !!suggestedRecording && t.recording_id === suggestedRecording;
                return (
                  <div
                    key={t.track_id}
                    className="row"
                    style={{
                      justifyContent: "space-between", padding: "4px 0",
                      borderBottom: "1px solid var(--border)",
                      background: suggested ? "var(--accent-subtle)" : undefined,
                      cursor: busy ? "default" : "pointer", opacity: busy ? 0.5 : 1,
                    }}
                    onClick={() => !busy && attach(t.track_id)}
                  >
                    <div className="row" style={{ gap: 8 }}>
                      <span className="dim mono" style={{ fontSize: 11, minWidth: 32, textAlign: "right" }}>
                        {release.tracks.some((x) => x.medium !== t.medium) ? `${t.medium}-${t.number}` : t.number}
                      </span>
                      <span style={{ color: "var(--text)" }}>{t.title}</span>
                      {suggested && (
                        <span className="dim mono" style={{ fontSize: 11, color: "var(--accent-text)" }}>
                          suggested
                        </span>
                      )}
                    </div>
                    <button className={suggested ? "btn btn-primary btn-sm" : "btn btn-secondary btn-sm"} disabled={busy}>
                      Attach
                    </button>
                  </div>
                );
              })}
            </div>
          </div>
        )}
      </div>
    </Modal>
  );
}
