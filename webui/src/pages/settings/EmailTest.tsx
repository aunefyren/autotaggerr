import { useState } from "react";
import { api, errMsg } from "../../api";
import { useToast } from "../../toast";

/**
 * The Email section's action. Sending one message is the only way to find out whether
 * SMTP settings work, and until now there was nothing to find out with: the settings
 * shipped before any code sent mail.
 *
 * It sends through the *stored* configuration, so pending edits are called out rather
 * than quietly tested — typing a new host, pressing Test, and being told it works
 * because the old one does is the failure this line exists to prevent.
 */
export function EmailTest({
  pendingEdits,
  testEnvironment,
}: {
  pendingEdits: boolean;
  testEnvironment: boolean;
}) {
  const [busy, setBusy] = useState(false);
  const toast = useToast();

  const sendTest = async () => {
    setBusy(true);
    try {
      const result = await api.post<{ sent_to: string }>("/settings/email/test");
      toast("ok", `Test message sent to ${result.sent_to}`);
    } catch (e) {
      // The SMTP server's own words: "535 authentication failed" is the answer the
      // admin came for, and a friendlier summary would throw it away.
      toast("err", errMsg(e));
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className="stack" style={{ gap: 8 }}>
      <div className="row" style={{ gap: 10, alignItems: "center", flexWrap: "wrap" }}>
        <button className="btn btn-secondary btn-sm" onClick={sendTest} disabled={busy}>
          {busy ? "Sending…" : "Send test message"}
        </button>
        <span className="muted" style={{ fontSize: 12 }}>
          {pendingEdits
            ? "Unsaved changes above — the test uses the stored settings, so save first."
            : "Sends one message to the test recipient, using the stored settings."}
        </span>
      </div>
      {testEnvironment && (
        <p className="muted" style={{ margin: 0, fontSize: 12, maxWidth: "76ch" }}>
          This instance is in the <strong>test</strong> environment, so <em>every</em> message it
          sends goes to the test recipient — never to the address it was addressed to. Nothing here
          can override that.
        </p>
      )}
    </div>
  );
}
