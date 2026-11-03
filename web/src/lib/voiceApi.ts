// Typed wrapper over create_voice_note (C259), the optional voice lane. Scaffold by M5-F2; lane M5-V1/W2 builds the recorder on it.
// The audio goes as base64 JSON through the generated client (the ESLint network ban holds).
import type { createApi } from "../api/client";
import type { components } from "../api/schema";
import { failure, type Res } from "./crateApi";

type Api = ReturnType<typeof createApi>;
export type Comment = components["schemas"]["Comment"];

/** The limits the server enforces on a voice note. */
export const VOICE_LIMITS = { maxSeconds: 120, maxBytes: 4 * 1024 * 1024 } as const;

export interface VoiceNoteInput {
  audioBase64: string;
  atSecond?: number | null;
  kind?: "note" | "fix" | "keep";
  body?: string;
}

/** create_voice_note: the caller owns the idempotency key (one per recording, reused on retries). */
export async function createVoiceNote(api: Api, trackId: string, key: string, i: VoiceNoteInput): Promise<Res<Comment>> {
  const r = await api.POST("/api/tracks/{id}/voice-notes", {
    params: { path: { id: trackId }, header: { "Idempotency-Key": key } },
    body: {
      audio_base64: i.audioBase64,
      ...(i.atSecond !== undefined ? { at_second: i.atSecond } : {}),
      ...(i.kind ? { kind: i.kind } : {}),
      ...(i.body ? { body: i.body } : {}),
    },
  });
  return r.data ? { ok: true, value: r.data } : failure(r.response.status, r.error, "Could not save the voice note");
}
