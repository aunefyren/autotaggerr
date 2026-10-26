import { useCallback, useEffect, useRef, useState } from "react";

/**
 * A value that settles rather than tracking every change.
 *
 * For a filter box whose results come from the server: the input stays immediate, and
 * only the fetch waits, so typing does not turn into a request per keystroke and a list
 * that flickers through every intermediate answer. A client-side filter needs none of
 * this — it is already free.
 */
export function useDebounced<T>(value: T, ms = 250): T {
  const [settled, setSettled] = useState(value);
  useEffect(() => {
    const t = setTimeout(() => setSettled(value), ms);
    return () => clearTimeout(t);
  }, [value, ms]);
  return settled;
}

// useFetch runs an async loader and exposes {data, err, loading, reload}.
export function useFetch<T>(fn: () => Promise<T>, deps: unknown[] = []) {
  const [data, setData] = useState<T | null>(null);
  const [err, setErr] = useState<string | null>(null);
  const [loading, setLoading] = useState(true);

  // eslint-disable-next-line react-hooks/exhaustive-deps
  const loader = useCallback(fn, deps);
  // Which call is the latest. Only its answer is applied: when the deps change while
  // a request is in flight (a page switching library), the older response can land
  // last, and applying it would show the previous view under the new one's controls.
  const latest = useRef(0);

  const reload = useCallback(() => {
    const call = ++latest.current;
    setLoading(true);
    loader()
      .then((d) => {
        if (call !== latest.current) return;
        setData(d);
        setErr(null);
      })
      .catch((e: Error) => {
        if (call === latest.current) setErr(e.message);
      })
      .finally(() => {
        if (call === latest.current) setLoading(false);
      });
  }, [loader]);

  useEffect(() => {
    reload();
  }, [reload]);

  return { data, err, loading, reload };
}
