// The app's cover provider: get_media_links online, the cached 512 cover offline.
import { api } from "../api/client";
import { createCovers } from "./coverLinks";
import { offline } from "./offline.svelte";

export const covers = createCovers({
  links: async (id) => {
    const r = await api.GET("/api/tracks/{id}/media", { params: { path: { id } } });
    return r.data ?? null;
  },
  local: async (id) => (await offline.localSource(id))?.coverSrc ?? null,
  online: () => typeof navigator === "undefined" || navigator.onLine !== false,
  now: () => Date.now(),
});
