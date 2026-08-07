import { useCallback, useEffect, useState } from "react";

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

  const reload = useCallback(() => {
    setLoading(true);
    loader()
      .then((d) => {
        setData(d);
        setErr(null);
      })
      .catch((e: Error) => setErr(e.message))
      .finally(() => setLoading(false));
  }, [loader]);

  useEffect(() => {
    reload();
  }, [reload]);

  return { data, err, loading, reload };
}
