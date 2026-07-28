import { useCallback, useEffect, useState } from "react";

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
