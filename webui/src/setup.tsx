import { createContext, ReactNode, useCallback, useContext, useEffect, useMemo, useState } from "react";
import { useLocation } from "react-router-dom";
import { api } from "./api";
import { Health } from "./types";

/**
 * Setup: what a fresh install still lacks, in the order it has to be added. One source
 * for the sidebar's setup steps, the overview's Get started card and the Libraries
 * page's manager hint, so the three can never disagree about what comes next.
 */

export interface SetupStep {
  key: "manager" | "library" | "process" | "download";
  /** A verb: the step is something to do, and the page is where it is done. */
  label: string;
  to: string;
  done: boolean;
  optional?: boolean;
}

interface SetupValue {
  health: Health | null;
  steps: SetupStep[];
  /** Every required step is done. */
  complete: boolean;
  /** The first required step not done yet, if any. */
  next: SetupStep | null;
  reload: () => void;
}

const SetupContext = createContext<SetupValue>({ health: null, steps: [], complete: true, next: null, reload: () => {} });

/** While setup is unfinished the figures are read again this often, so a step done on
 *  the page in front of you — a run finishing, say — is ticked without navigating. */
const POLL_MS = 10_000;

export function SetupProvider({ children }: { children: ReactNode }) {
  const loc = useLocation();
  const [health, setHealth] = useState<Health | null>(null);

  const reload = useCallback(() => {
    api.get<Health>("/health").then(setHealth).catch(() => {
      /* The sidebar keeps its last answer; the pages report their own errors. */
    });
  }, []);

  // Read on every navigation: most steps are finished on one page and noticed on the next.
  useEffect(reload, [reload, loc.pathname]);

  const value = useMemo<SetupValue>(() => {
    const s = health?.setup;
    if (!s) return { health, steps: [], complete: true, next: null, reload };
    const steps: SetupStep[] = [
      { key: "manager", label: "Choose a manager", to: "/managers", done: s.managers > 0 },
      { key: "library", label: "Add a library", to: "/libraries", done: s.libraries > 0 },
      { key: "process", label: "Process it", to: "/libraries", done: s.indexed_files > 0 },
    ];
    // Import refuses Lidarr-managed libraries, so a setup with only those has no use
    // for a download client — the step is not offered rather than offered and refused.
    if (s.libraries === 0 || s.importable_libraries > 0) {
      steps.push({ key: "download", label: "Connect a download client", to: "/download-clients", done: s.download_clients > 0, optional: true });
    }
    const next = steps.find((st) => !st.optional && !st.done) ?? null;
    return { health, steps, complete: next === null, next, reload };
  }, [health, reload]);

  useEffect(() => {
    if (value.complete) return;
    const t = setInterval(reload, POLL_MS);
    return () => clearInterval(t);
  }, [value.complete, reload]);

  return <SetupContext.Provider value={value}>{children}</SetupContext.Provider>;
}

export function useSetup(): SetupValue {
  return useContext(SetupContext);
}
