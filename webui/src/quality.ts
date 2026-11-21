import { api } from "./api";
import { useFetch } from "./hooks";
import { QualityDefinition, QualityID } from "./types";

/**
 * The quality ladder's display names, from the server (GET /quality) so the names
 * have one definition. Until they arrive, or for an ID the server does not know, a
 * quality reads as its ID.
 */
export function useQualityNames(): (id: QualityID | undefined) => string {
  const ladder = useFetch<{ qualities: QualityDefinition[] }>(() => api.get("/quality"));
  const names = new Map((ladder.data?.qualities ?? []).map((d) => [d.id, d.name]));
  return (id) => (id ? names.get(id) ?? id : "");
}
