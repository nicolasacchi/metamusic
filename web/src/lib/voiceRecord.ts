// Voice note recording (C259, D-M5-23): feature detection, container choice, base64 and the upload outcome. Pure, so the
// recorder component stays thin. Nothing here uploads by itself: `save` is only reachable from "recorded" (a user action).
import type { createApi } from "../api/client";
import { newIdempotencyKey } from "../api/idem";
import type { Failure } from "./crateApi";
import { createVoiceNote, VOICE_LIMITS, type Comment, type VoiceNoteInput } from "./voiceApi";

type Api = ReturnType<typeof createApi>;

/** Containers the server accepts, best first: WebM/Opus (Chrome, Firefox), Ogg/Opus, MP4 (iOS Safari records AAC in MP4). */
export const MIME_PREFERENCE = ["audio/webm;codecs=opus", "audio/ogg;codecs=opus", "audio/mp4;codecs=mp4a.40.2", "audio/mp4"] as const;

export interface RecorderEnv {
  MediaRecorder?: { isTypeSupported?(mime: string): boolean };
  mediaDevices?: { getUserMedia?: unknown };
  secure?: boolean;
}

export type Support = { ok: true; mime: string } | { ok: false; reason: string };

/** Feature check (iOS Safari: MediaRecorder from 14.3, a secure context and a gesture are required). */
export function detectSupport(env: RecorderEnv): Support {
  if (env.secure === false) return { ok: false, reason: "Recording needs a secure (https) page." };
  if (!env.MediaRecorder) return { ok: false, reason: "This browser cannot record audio." };
  if (typeof env.mediaDevices?.getUserMedia !== "function") return { ok: false, reason: "No microphone access in this browser." };
  const can = env.MediaRecorder.isTypeSupported?.bind(env.MediaRecorder);
  // A recorder without isTypeSupported (very old WebKit) is left to choose its own container.
  if (!can) return { ok: true, mime: "" };
  const mime = MIME_PREFERENCE.find((m) => can(m));
  return mime ? { ok: true, mime } : { ok: false, reason: "This browser records a format the server does not accept." };
}

/** The live environment, read at call time so the component can feature-check on mount. */
export function liveEnv(): RecorderEnv {
  const g = globalThis as unknown as { MediaRecorder?: RecorderEnv["MediaRecorder"]; navigator?: { mediaDevices?: RecorderEnv["mediaDevices"] }; isSecureContext?: boolean };
  return { MediaRecorder: g.MediaRecorder, mediaDevices: g.navigator?.mediaDevices, secure: g.isSecureContext };
}

/** Base64 of a blob without building one giant string of char codes. */
export async function blobToBase64(blob: Blob): Promise<string> {
  const bytes = new Uint8Array(await blob.arrayBuffer());
  let bin = "";
  for (let i = 0; i < bytes.length; i += 0x8000) bin += String.fromCharCode(...bytes.subarray(i, i + 0x8000));
  return btoa(bin);
}

export type VoiceState =
  | { s: "unsupported"; reason: string }
  | { s: "idle" }
  | { s: "recording" }
  | { s: "recorded"; seconds: number; bytes: number; tooBig: boolean; tooLong: boolean }
  | { s: "uploading" }
  | { s: "saved" }
  | { s: "unavailable" }
  | { s: "error"; message: string };

export type VoiceEvent =
  | { e: "start" }
  | { e: "stop"; seconds: number; bytes: number }
  | { e: "save" }
  | { e: "saved" }
  | { e: "failed"; outcome: Exclude<UploadOutcome, { kind: "saved" }> }
  | { e: "discard" };

export const voiceStart = (sup: Support): VoiceState => (sup.ok ? { s: "idle" } : { s: "unsupported", reason: sup.reason });

/** The recorder's transitions. `uploading` is reachable only from `recorded` through an explicit `save`. */
export function voiceStep(st: VoiceState, ev: VoiceEvent): VoiceState {
  switch (ev.e) {
    case "start":
      return st.s === "idle" || st.s === "saved" || st.s === "error" || st.s === "recorded" ? { s: "recording" } : st;
    case "stop":
      return st.s === "recording"
        ? { s: "recorded", seconds: ev.seconds, bytes: ev.bytes, tooBig: ev.bytes > VOICE_LIMITS.maxBytes, tooLong: ev.seconds > VOICE_LIMITS.maxSeconds }
        : st;
    case "save":
      return st.s === "recorded" && !st.tooBig && !st.tooLong ? { s: "uploading" } : st;
    case "saved":
      return st.s === "uploading" ? { s: "saved" } : st;
    case "failed":
      if (st.s !== "uploading") return st;
      return ev.outcome.kind === "unavailable" ? { s: "unavailable" } : { s: "error", message: ev.outcome.message };
    case "discard":
      return st.s === "recorded" || st.s === "error" ? { s: "idle" } : st;
  }
}

export type UploadOutcome = { kind: "saved"; comment: Comment } | { kind: "unavailable" } | { kind: "error"; message: string; status: number };

const REASONS: Record<string, string> = {
  encoding: "The recording could not be read.",
  size: "The recording is too large (4 MiB at most).",
  container: "This browser's recording format is not accepted.",
  decode: "The server could not decode the recording.",
  too_long: "The recording is too long (2 minutes at most).",
};

/** Maps a failed create_voice_note: 501/503 is "not available"; HUMAN_ONLY and VOICE_INVALID get their own words. */
export function uploadFailure(f: Failure): Exclude<UploadOutcome, { kind: "saved" }> {
  if (f.status === 501 || f.status === 503) return { kind: "unavailable" };
  if (f.code === "HUMAN_ONLY" || f.status === 403) return { kind: "error", status: f.status, message: "Only you, signed in as yourself, can record voice notes." };
  if (f.code === "VOICE_INVALID") {
    const m = /reason[^a-z_]*([a-z_]+)/i.exec(f.message)?.[1] ?? "";
    return { kind: "error", status: f.status, message: REASONS[m] ?? "The server did not accept the recording." };
  }
  return { kind: "error", status: f.status, message: f.message };
}

/**
 * Uploads one recording. The key is the caller's, one per recording, reused when the same recording is retried.
 * Only the explicit Save button reaches this.
 */
export async function uploadVoice(api: Api, trackId: string, key: string, blob: Blob, opts: Omit<VoiceNoteInput, "audioBase64">): Promise<UploadOutcome> {
  try {
    const r = await createVoiceNote(api, trackId, key, { ...opts, audioBase64: await blobToBase64(blob) });
    return r.ok ? { kind: "saved", comment: r.value } : uploadFailure(r);
  } catch {
    return { kind: "error", status: 0, message: "Network error: the voice note was not saved. Try again." };
  }
}

export { newIdempotencyKey };
