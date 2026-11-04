// Typed wrapper over get_listen_heat (C256). Scaffold by M5-F2; lane M5-W1 draws the heat strip from it.
import type { createApi } from "../api/client";
import type { components } from "../api/schema";
import { failure, type Res } from "./crateApi";

type Api = ReturnType<typeof createApi>;
export type ListenHeat = components["schemas"]["ListenHeat"];
export type ListenHotspot = components["schemas"]["ListenHotspot"];

/** get_listen_heat: 120 bins of how the operator listened to a take, plus up to three hotspots. */
export async function getListenHeat(api: Api, id: string): Promise<Res<ListenHeat>> {
  const r = await api.GET("/api/tracks/{id}/heat", { params: { path: { id } } });
  return r.data ? { ok: true, value: r.data } : failure(r.response.status, r.error, "Could not load the listening heat");
}
