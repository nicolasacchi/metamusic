<script lang="ts">
  // Voice note recorder (C259, D-M5-23). Records with MediaRecorder, keeps the blob in memory, and uploads only when the
  // user presses Save (never on stop). The mic is released as soon as recording stops. No second audio element: the
  // recording is not previewed here; the saved note plays through the comment list like any other.
  import { onDestroy } from "svelte";
  import { api } from "../../api/client";
  import { VOICE_LIMITS } from "../voiceApi";
  import { fmt } from "../time";
  import { detectSupport, liveEnv, newIdempotencyKey, uploadVoice, voiceStart, voiceStep, type Support, type VoiceEvent, type VoiceState } from "../voiceRecord";

  interface Props {
    trackId: string;
    /** Seconds into the take the note is pinned to; null for none. */
    atSecond: number | null;
    /** Called with nothing after a note was saved, so the parent can reload its comments. */
    onsaved?: () => void;
  }
  let { trackId, atSecond, onsaved }: Props = $props();

  const support: Support = detectSupport(liveEnv());
  let st = $state<VoiceState>(voiceStart(support));
  let elapsed = $state(0);
  let kind = $state<"note" | "fix" | "keep">("note");

  let rec: MediaRecorder | null = null;
  let stream: MediaStream | null = null;
  let chunks: Blob[] = [];
  let blob: Blob | null = null;
  let key = "";
  let tick: ReturnType<typeof setInterval> | null = null;
  let started = 0;

  const send = (e: VoiceEvent) => (st = voiceStep(st, e));

  function release() {
    if (tick) clearInterval(tick);
    tick = null;
    stream?.getTracks().forEach((t) => t.stop());
    stream = null;
  }

  async function start() {
    if (!support.ok || st.s === "recording") return;
    try {
      stream = await navigator.mediaDevices.getUserMedia({ audio: true });
    } catch {
      st = { s: "error", message: "Microphone access was refused." };
      return;
    }
    chunks = [];
    blob = null;
    key = newIdempotencyKey(); // one key per recording, reused if Save is retried
    rec = new MediaRecorder(stream, support.mime ? { mimeType: support.mime } : undefined);
    rec.ondataavailable = (e) => {
      if (e.data.size > 0) chunks.push(e.data);
    };
    rec.onstop = () => {
      const seconds = (Date.now() - started) / 1000;
      release();
      blob = new Blob(chunks, { type: rec?.mimeType || support.mime || "audio/webm" });
      send({ e: "stop", seconds, bytes: blob.size });
    };
    send({ e: "start" });
    started = Date.now();
    elapsed = 0;
    rec.start();
    tick = setInterval(() => {
      elapsed = (Date.now() - started) / 1000;
      if (elapsed >= VOICE_LIMITS.maxSeconds) stop();
    }, 250);
  }

  function stop() {
    if (rec && rec.state !== "inactive") rec.stop();
  }

  async function save() {
    if (st.s !== "recorded" || !blob) return;
    const b = blob;
    const nextSt = voiceStep(st, { e: "save" });
    if (nextSt.s !== "uploading") return;
    st = nextSt;
    const o = await uploadVoice(api, trackId, key, b, { atSecond, kind });
    if (o.kind === "saved") {
      blob = null;
      send({ e: "saved" });
      onsaved?.();
    } else send({ e: "failed", outcome: o });
  }

  function discard() {
    blob = null;
    send({ e: "discard" });
  }

  onDestroy(() => {
    if (rec && rec.state !== "inactive") {
      rec.onstop = null;
      rec.stop();
    }
    release();
  });
</script>

<div class="voice" data-testid="voice-recorder" data-track={trackId} data-at={atSecond ?? ""} data-state={st.s}>
  {#if st.s === "unsupported"}
    <p class="muted" data-testid="voice-unsupported">Voice notes: {st.reason}</p>
  {:else if st.s === "unavailable"}
    <p class="muted" data-testid="voice-unavailable">Voice notes are not available on this server.</p>
  {:else if st.s === "recording"}
    <p role="status" data-testid="voice-timer">Recording {fmt(elapsed)} of {fmt(VOICE_LIMITS.maxSeconds)}</p>
    <button type="button" data-testid="voice-stop" onclick={stop}>Stop</button>
  {:else if st.s === "recorded"}
    <p data-testid="voice-recorded">Recorded {fmt(st.seconds)}{atSecond !== null ? ` at ${fmt(atSecond)}` : ""}.</p>
    {#if st.tooBig || st.tooLong}
      <p role="alert">{st.tooLong ? "Too long (2 minutes at most)." : "Too large (4 MiB at most)."} Discard it and record a shorter note.</p>
    {:else}
      <label>Kind
        <select bind:value={kind} data-testid="voice-kind">
          <option value="note">Note</option>
          <option value="fix">Fix</option>
          <option value="keep">Keep</option>
        </select>
      </label>
      <button type="button" data-testid="voice-save" onclick={() => void save()}>Save voice note</button>
    {/if}
    <button type="button" data-testid="voice-discard" onclick={discard}>Discard</button>
  {:else if st.s === "uploading"}
    <p role="status" aria-busy="true">Saving…</p>
  {:else}
    {#if st.s === "saved"}<p role="status" data-testid="voice-saved">Voice note saved.</p>{/if}
    {#if st.s === "error"}<p role="alert">{st.message}</p>{/if}
    <button type="button" data-testid="voice-record" onclick={() => void start()}>{st.s === "idle" ? "Record a voice note" : "Record another"}</button>
  {/if}
</div>

<style>
  .voice {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: var(--sp-2);
    min-width: 0;
  }
  .voice p {
    margin: 0;
    overflow-wrap: anywhere;
  }
  .voice button,
  .voice select {
    min-height: var(--hit);
    padding: 0 var(--sp-4);
  }
</style>
