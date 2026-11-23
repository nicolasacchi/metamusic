<script lang="ts">
  // Listen to a plan before paying for audio (DESK-W2, m5b-desk C363.3). "Audition" asks the server for a free CPU render of the
  // plan's score (create_plan_audition: fluidsynth and a soundfont, cached by content), then plays the MP3 on the shared
  // AudioContext, the TrackStems pattern: bytes through fetchClip, an audio-focus lease that pauses the main player, never a
  // second audio element. The bar that is sounding is reported to the notation through onbar. With no synthesizer or soundfont
  // on the server the panel says so plainly and offers no button, so nothing fails after a click.
  import { onDestroy, untrack } from "svelte";
  import { api as defaultApi } from "../../api/client";
  import { fetchClip } from "../../api/media";
  import { getAudioContext } from "../audioContext";
  import type { Lease } from "../audioFocus";
  import { barAt, barStarts, auditionBody, auditionLine, auditionRefusalText, clock, defaultAuditionForm, playOutcome, type AuditionForm, type BarStart } from "../auditionPlayer";
  import { createAudition, getAudition, type DeskFailure, type Plan, type PlanAudition } from "../deskApi";
  import { voiceIds } from "../deskView";
  import { player } from "../player.svelte";
  import { getCapabilities, type Capabilities } from "../transformApi";
  import { scoreAvailability } from "../transformScore";
  import type { BarRef } from "../abcRender";

  interface Props {
    plan: Pick<Plan, "id" | "auditions" | "bar_spans" | "score">;
    client?: typeof defaultApi;
    /** Pre-loaded capabilities (unit tests). */
    initialCaps?: Capabilities | null;
    /** The bar sounding now, or null when nothing plays. */
    onbar?: (b: BarRef | null) => void;
  }
  let { plan, client = defaultApi, initialCaps = null, onbar }: Props = $props();

  // svelte-ignore state_referenced_locally
  let caps = $state<Capabilities | null>(initialCaps);
  // svelte-ignore state_referenced_locally
  let capsLoaded = $state(initialCaps !== null);
  // svelte-ignore state_referenced_locally
  let stored = $state<PlanAudition[]>([...(plan.auditions ?? [])]);
  let form = $state<AuditionForm>({ ...defaultAuditionForm });
  let making = $state(false);
  let failure = $state<DeskFailure | null>(null);
  let error = $state("");
  let notice = $state("");
  let playing = $state<string | null>(null);
  let loading = $state<string | null>(null);
  let pos = $state(0);
  let total = $state(0);

  const avail = $derived(scoreAvailability(caps));
  const starts = $derived<BarStart[]>(barStarts(plan.bar_spans, voiceIds(plan)));

  $effect(() => {
    void plan.id;
    stored = [...untrack(() => plan.auditions ?? [])];
    untrack(stop);
  });
  $effect(() => {
    if (initialCaps !== null) return;
    let live = true;
    void getCapabilities(client).then((r) => {
      if (!live) return;
      caps = r.ok ? r.value : null;
      capsLoaded = true;
    });
    return () => {
      live = false;
    };
  });

  const buffers: Record<string, AudioBuffer | undefined> = {};
  let source: AudioBufferSourceNode | null = null;
  let lease: Lease | null = null;
  let token = 0;
  let startedAt = 0;
  let timer: ReturnType<typeof setInterval> | null = null;

  function stop() {
    token++;
    const src = source;
    source = null;
    if (src) {
      src.onended = null;
      try {
        src.stop();
      } catch {
        // already stopped
      }
      src.disconnect();
    }
    if (timer !== null) clearInterval(timer);
    timer = null;
    playing = null;
    loading = null;
    pos = 0;
    onbar?.(null);
    lease?.release();
    lease = null;
  }
  onDestroy(stop);

  async function play(a: Pick<PlanAudition, "id" | "url">, attempt = 0): Promise<void> {
    if (playing === a.id || loading === a.id) return stop();
    if (attempt === 0) stop();
    const my = token;
    let ctx: AudioContext;
    try {
      ctx = getAudioContext();
    } catch (e) {
      error = e instanceof Error ? e.message : "Playback is not available here.";
      return;
    }
    void ctx.resume().catch(() => {}); // inside the click, before any await (iOS)
    if (attempt === 0) {
      lease = player.acquireFocus("audition", { handlers: { play: () => void play(a), pause: () => stop(), playing: () => playing !== null }, onEnd: () => void ctx.suspend().catch(() => {}) });
    }
    loading = a.id;
    error = "";
    try {
      let buf = buffers[a.id];
      if (!buf) {
        buf = await ctx.decodeAudioData(await fetchClip(a.url));
        buffers[a.id] = buf;
      }
      if (my !== token) return;
      const src = ctx.createBufferSource();
      src.buffer = buf;
      src.connect(ctx.destination);
      src.onended = () => {
        if (my === token) stop();
      };
      src.start();
      startedAt = ctx.currentTime;
      total = buf.duration;
      source = src;
      playing = a.id;
      loading = null;
      lease?.changed();
      timer = setInterval(() => {
        pos = Math.max(0, ctx.currentTime - startedAt);
        onbar?.(barAt(starts, pos));
      }, 100);
    } catch (e) {
      if (my !== token) return;
      if (playOutcome(e, attempt) === "retry") {
        // the signed link expired: ask for a fresh one and try once more
        const fresh = await getAudition(client, plan.id, a.id);
        if (my !== token) return;
        loading = null;
        if (fresh.ok) return play({ id: a.id, url: fresh.value.url }, attempt + 1);
        failure = fresh;
        error = auditionRefusalText(fresh);
      } else error = e instanceof Error ? e.message : "Could not play the audition.";
      stop();
    }
  }

  /** Play a stored audition: its listed link may be old, so the server is asked for a fresh one first. */
  async function playStored(a: PlanAudition) {
    if (playing === a.id || loading === a.id) return stop();
    stop();
    const my = token;
    void getAudioContext().resume().catch(() => {});
    loading = a.id;
    const fresh = await getAudition(client, plan.id, a.id);
    if (my !== token) return;
    loading = null;
    if (!fresh.ok) {
      failure = fresh;
      error = auditionRefusalText(fresh);
      return;
    }
    await play({ id: a.id, url: fresh.value.url });
  }

  async function audition() {
    if (making) return;
    making = true;
    failure = null;
    error = "";
    notice = "";
    stop();
    void getAudioContext().resume().catch(() => {}); // the click is the gesture; the render takes a while
    const r = await createAudition(client, plan.id, auditionBody(form))();
    making = false;
    if (!r.ok) {
      failure = r;
      error = auditionRefusalText(r);
      return;
    }
    const d = r.value.audition;
    if (!stored.some((x) => x.id === d.id)) stored = [{ id: d.id, created_at: d.created_at, url: d.url, soundfont: d.soundfont, chords: d.chords, seconds: d.seconds }, ...stored];
    notice = r.value.created ? "" : "Already made: playing the saved one.";
    await play({ id: d.id, url: d.url });
  }
</script>

<section class="card" aria-labelledby="audition-h" data-testid="audition-panel">
  <h2 id="audition-h">Listen</h2>
  {#if !capsLoaded}
    <p class="muted" data-testid="audition-loading">Loading…</p>
  {:else if !avail.available}
    <p class="muted" role="status" data-testid="audition-unavailable">{avail.reason}</p>
  {:else}
    <p class="muted">A free preview of this score through a soundfont. It is not a take and costs nothing.</p>
    <div class="row">
      <button type="button" class="primary" disabled={making} onclick={() => void audition()} data-testid="audition-make">{making ? "Rendering…" : "Audition"}</button>
      <label>Soundfont
        <select value={form.soundfont} onchange={(e) => (form.soundfont = e.currentTarget.value)} data-testid="audition-soundfont">
          <option value="">First configured</option>
          {#each avail.soundfonts as n (n)}<option value={n}>{n}</option>{/each}
        </select>
      </label>
      <label>Chords
        <select value={form.chords} onchange={(e) => (form.chords = e.currentTarget.value as AuditionForm["chords"])} data-testid="audition-chords">
          <option value="">Play chord symbols</option>
          <option value="none">Notes only</option>
        </select>
      </label>
    </div>
    <div aria-live="polite">
      {#if making}<p class="muted" aria-busy="true">Rendering the score…</p>{/if}
      {#if notice}<p class="muted" role="status">{notice}</p>{/if}
      {#if error}<p class="err" role="alert" data-testid="audition-error" data-code={failure?.code ?? ""}>{error}</p>{/if}
    </div>
    {#if stored.length > 0}
      <ul class="list" data-testid="audition-list">
        {#each stored as a (a.id)}
          <li data-testid="audition-item">
            <button type="button" class="play" aria-pressed={playing === a.id} disabled={loading === a.id} onclick={() => void playStored(a)} data-testid="audition-play">
              {playing === a.id ? "Stop" : loading === a.id ? "Loading…" : "Play"}
            </button>
            <span>{auditionLine(a)}</span>
            {#if playing === a.id}<span class="muted" data-testid="audition-clock">{clock(pos)} / {clock(total)}</span>{/if}
          </li>
        {/each}
      </ul>
    {/if}
  {/if}
</section>

<style>
  .row {
    display: flex;
    flex-wrap: wrap;
    gap: var(--sp-3);
    align-items: end;
  }
  .row label {
    display: flex;
    flex-direction: column;
    gap: var(--sp-1);
    font-size: var(--fs-sm);
  }
  button,
  select {
    min-height: var(--hit);
  }
  .primary {
    background: var(--accent);
    color: var(--accent-ink);
    border: none;
    border-radius: var(--r-md);
    padding: 0 var(--sp-4);
    font-weight: var(--fw-bold);
  }
  .list {
    list-style: none;
    padding: 0;
    margin: var(--sp-3) 0 0;
    display: grid;
    gap: var(--sp-2);
  }
  .list li {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: var(--sp-3);
  }
  .play {
    min-width: var(--hit);
  }
  .err {
    color: var(--bad);
    overflow-wrap: anywhere;
  }
</style>
