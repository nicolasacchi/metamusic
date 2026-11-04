// Typed wrappers over Callback Listening (C255, C260). Scaffold by M5-F2; lane M5-W2 builds the Judge Callbacks view on them.
import type { createApi } from "../api/client";
import type { components, operations } from "../api/schema";
import { failure, type Res } from "./crateApi";

type Api = ReturnType<typeof createApi>;
type S = components["schemas"];
export type Callback = S["Callback"];
export type CallbackVerdict = S["CallbackVerdict"];
export type CallbackPage = S["CallbackPage"];
export type CallbackSubmitResult = S["CallbackSubmitResult"];
export type CallbackListQuery = NonNullable<operations["list_callbacks"]["parameters"]["query"]>;

/** The answers a callback kind accepts, in button order (fix: fixed / not yet / worse; keep: kept / lost). */
export const VERDICTS: Record<Callback["kind"], readonly CallbackVerdict["verdict"][]> = {
  fix: ["fixed", "not_yet", "worse"],
  keep: ["kept", "lost"],
};

/** list_callbacks: pending first (oldest first), then answered. */
export async function listCallbacks(api: Api, query: CallbackListQuery = {}): Promise<Res<CallbackPage>> {
  const r = await api.GET("/api/callbacks", { params: { query } });
  return r.data ? { ok: true, value: r.data } : failure(r.response.status, r.error, "Could not list the callbacks");
}

/** get_callback. */
export async function getCallback(api: Api, id: string): Promise<Res<Callback>> {
  const r = await api.GET("/api/callbacks/{id}", { params: { path: { id } } });
  return r.data ? { ok: true, value: r.data } : failure(r.response.status, r.error, "Could not load the callback");
}

/** submit_callback_verdict: naturally idempotent (no key); an identical repeat answers replayed. */
export async function submitVerdict(api: Api, id: string, verdict: CallbackVerdict["verdict"], note?: string, heardAtS?: number): Promise<Res<CallbackSubmitResult>> {
  const r = await api.POST("/api/callbacks/{id}/verdicts", {
    params: { path: { id } },
    body: { verdict, ...(note ? { note } : {}), ...(heardAtS !== undefined ? { heard_at_s: heardAtS } : {}) },
  });
  return r.data ? { ok: true, value: r.data } : failure(r.response.status, r.error, "Could not send the answer");
}
