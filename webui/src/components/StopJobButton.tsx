import { useState } from "react";
import { api, errMsg } from "../api";
import { useToast } from "../toast";

/**
 * Stops the job that is running. No confirm step: stopping loses nothing — every file
 * already written stays written, the files in flight finish, and the run records itself
 * as stopped early. What it does not do is clear the queue behind it.
 *
 * Ghost, not danger-coloured: it discards nothing, and it sits beside a status, not in a
 * row of verbs where it would compete with them.
 */
export function StopJobButton({ stopping, onStopped }: { stopping?: boolean; onStopped?: () => void }) {
  const toast = useToast();
  const [busy, setBusy] = useState(false);
  const stop = async () => {
    setBusy(true);
    try {
      await api.post("/process/cancel");
      toast("info", "Stopping after the files in flight — what is done stays done");
      onStopped?.();
    } catch (e) {
      toast("err", errMsg(e));
    } finally {
      setBusy(false);
    }
  };
  return (
    <button
      className="btn btn-ghost btn-sm"
      disabled={busy || stopping}
      title="Stop this job between files. Files already written stay written; jobs queued behind it still run."
      onClick={stop}
    >
      {stopping ? "Stopping…" : "Stop"}
    </button>
  );
}
