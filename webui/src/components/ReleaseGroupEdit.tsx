import { FormEvent, useState } from "react";
import { api, errMsg } from "../api";
import { LibraryItem } from "../types";
import { useToast } from "../toast";
import { Modal } from "./ui";

// The download's release group ("GRP" in Artist-Album-WEB-FLAC-2014-GRP) — what the
// {Release Group} naming token renders. Not MusicBrainz's release group, and not a tag:
// setting it changes where the next Organise names a file, nothing else.

const MAX_LENGTH = 64;

const HELP =
  "The group that put out the download, as in Artist-Album-WEB-FLAC-2014-GRP. File names use it through " +
  "{Release Group} the next time they are organised; no tag carries it. Empty records none.";

function saveReleaseGroup(ids: string[], value: string) {
  return api.put<{ updated: number; release_group: string }>("/item-release-group", { item_ids: ids, release_group: value });
}

/** How a recorded value reads: never recorded and recorded-as-none both name nothing, but say different things. */
function GroupValue({ value }: { value: string | null | undefined }) {
  if (value == null) return <span className="diffv none">not recorded</span>;
  if (value === "") return <span className="diffv none">none</span>;
  return <span className="diffv same">{value}</span>;
}

/**
 * One file's release group as a row of the item detail's path block, editable in place.
 * Beside the path because that is what it feeds. In a Lidarr library the row is a
 * readout: Lidarr records the group on every run, so an edit here would not hold.
 */
export function ReleaseGroupRow({ item, onSaved }: { item: LibraryItem; onSaved?: () => void }) {
  const [value, setValue] = useState(item.release_group);
  const [draft, setDraft] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const toast = useToast();

  const save = async (e: FormEvent) => {
    e.preventDefault();
    if (draft === null) return;
    setBusy(true);
    try {
      const res = await saveReleaseGroup([item.id], draft);
      setValue(res.release_group);
      setDraft(null);
      onSaved?.();
    } catch (err) {
      toast("err", errMsg(err));
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className="diffrow">
      <span className="diffkey">Release group</span>
      <div className="diffvals">
        {draft === null ? (
          <div className="row" style={{ gap: 8, justifyContent: "space-between" }}>
            <GroupValue value={value} />
            {item.release_group_editable && (
              <button type="button" className="btn btn-ghost btn-sm" onClick={() => setDraft(value ?? "")}>
                Edit
              </button>
            )}
          </div>
        ) : (
          <form className="row" style={{ gap: 6 }} onSubmit={save}>
            <input
              className="input mono"
              style={{ height: 28 }}
              aria-label="Release group"
              value={draft}
              maxLength={MAX_LENGTH}
              autoFocus
              placeholder="GRP"
              spellCheck={false}
              onChange={(e) => setDraft(e.target.value)}
              onKeyDown={(e) => {
                if (e.key === "Escape") {
                  e.stopPropagation();
                  setDraft(null);
                }
              }}
            />
            <button className="btn btn-primary btn-sm" disabled={busy}>Save</button>
            <button type="button" className="btn btn-ghost btn-sm" disabled={busy} onClick={() => setDraft(null)}>
              Cancel
            </button>
          </form>
        )}
        {draft !== null && <span className="dim" style={{ fontFamily: "var(--font-sans)", fontSize: 11 }}>{HELP}</span>}
        {!item.release_group_editable && (
          <span className="dim" style={{ fontFamily: "var(--font-sans)", fontSize: 11 }}>
            Recorded from Lidarr's track file — set it in Lidarr.
          </span>
        )}
      </div>
    </div>
  );
}

/** One release group for several files at once — an album usually came from one download. */
export function SetReleaseGroupModal({
  items,
  onClose,
  onSaved,
}: {
  items: LibraryItem[];
  onClose: () => void;
  onSaved: () => void;
}) {
  // Prefilled only when the files already agree; a mix has no value to start from.
  const shared = items.every((it) => (it.release_group ?? "") === (items[0].release_group ?? ""));
  const [value, setValue] = useState(shared ? items[0].release_group ?? "" : "");
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState("");
  const toast = useToast();
  const files = `${items.length} file${items.length === 1 ? "" : "s"}`;

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    setBusy(true);
    setErr("");
    try {
      const res = await saveReleaseGroup(items.map((it) => it.id), value);
      toast("info", res.release_group ? `Release group set to ${res.release_group} on ${files}` : `Release group cleared on ${files}`);
      onSaved();
    } catch (e) {
      setErr(errMsg(e));
    } finally {
      setBusy(false);
    }
  };

  return (
    <Modal title={`Set release group on ${files}`} onClose={onClose}>
      <form onSubmit={submit} className="stack">
        <div className="field">
          <label className="flabel" htmlFor="rg-value">Release group</label>
          <input
            id="rg-value"
            className={`input mono${err ? " invalid" : ""}`}
            value={value}
            maxLength={MAX_LENGTH}
            autoFocus
            placeholder={shared ? "GRP" : "these files differ"}
            spellCheck={false}
            onChange={(e) => setValue(e.target.value)}
          />
          {err ? <span className="help-err">{err}</span> : <span className="dim" style={{ fontSize: 11 }}>{HELP}</span>}
        </div>
        <div className="modal-actions">
          <button type="button" className="btn btn-ghost btn-sm" onClick={onClose}>Cancel</button>
          <button className="btn btn-primary btn-sm" disabled={busy}>
            {value.trim() ? `Set on ${files}` : `Clear on ${files}`}
          </button>
        </div>
      </form>
    </Modal>
  );
}
