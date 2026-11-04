<script lang="ts">
  // One callback (C255, C262): hear the new take at the spot, optionally the original at its spot, then answer with one of
  // the big buttons. Stays on the page (the shared player does the playing); 501/503 read as "not available".
  import { api } from "../../api/client";
  import { getCallback, listCallbacks, submitVerdict, type Callback } from "../callbacksApi";
  import { answerButtons, answeredText, isUnavailable, nextPending, question, roughSpot, spotText, UNAVAILABLE_TEXT, verdictLabel } from "../callbackView";
  import { player } from "../player.svelte";

  interface Props {
    id: string;
    navigate: (to: string) => void;
  }
  let { id, navigate }: Props = $props();

  let cb = $state<Callback | null>(null);
  let loading = $state(true);
  let unavailable = $state(false);
  let error = $state("");
  let busy = $state(false);
  let result = $state("");
  let next = $state<string | null>(null);
  let note = $state("");
  let seq = 0;

  async function load(want: string) {
    const mine = ++seq;
    loading = true;
    unavailable = false;
    error = "";
    result = "";
    note = "";
    cb = null;
    try {
      const r = await getCallback(api, want);
      if (mine !== seq) return;
      if (r.ok) cb = r.value;
      else if (isUnavailable(r)) unavailable = true;
      else error = r.message;
    } catch {
      if (mine === seq) error = "Could not load the callback";
    }
    if (mine === seq) loading = false;
  }
  $effect(() => {
    void load(id);
  });

  const buttons = $derived(cb ? answerButtons(cb.kind) : []);
  const mine = $derived(cb?.my_verdict ?? null);

  function hearNew() {
    if (!cb) return;
    player.play(cb.child.id, undefined, { startAt: Math.max(0, cb.spot.child_at_s), title: cb.child.title });
  }
  function hearOriginal() {
    if (!cb) return;
    const at = cb.spot.source_at_s ?? cb.comment.at_second;
    if (at === null) return;
    player.play(cb.comment.track_id, undefined, { startAt: Math.max(0, at) });
  }

  async function answer(v: (typeof buttons)[number]["verdict"]) {
    if (!cb || busy) return;
    busy = true;
    error = "";
    const heard = player.state.trackId === cb.child.id ? Math.round(player.state.current * 10) / 10 : undefined;
    try {
      const r = await submitVerdict(api, cb.id, v, note.trim() || undefined, heard);
      if (r.ok) {
        result = answeredText(r.value);
        const l = await listCallbacks(api, { state: ["pending"], limit: 50 });
        next = l.ok ? nextPending(l.value.items, cb.id) : null;
        const fresh = await getCallback(api, cb.id);
        if (fresh.ok) cb = fresh.value;
      } else if (isUnavailable(r)) unavailable = true;
      else error = r.message;
    } catch {
      error = "Could not send the answer";
    } finally {
      busy = false;
    }
  }
</script>

<section class="card" data-testid="callback-card" data-callback={id} aria-busy={loading}>
  {#if loading}
    <p class="muted">Loading…</p>
  {:else if unavailable}
    <p class="muted" data-testid="callbacks-unavailable">{UNAVAILABLE_TEXT}</p>
  {:else if error && !cb}
    <p role="alert">{error} <button type="button" onclick={() => void load(id)}>Retry</button></p>
  {:else if cb}
    <p class="kind">{cb.kind === "fix" ? "Fix check" : "Keep check"} · {cb.child.title} · seed {cb.child.seed}</p>
    <blockquote class="said" data-testid="callback-comment">
      {cb.comment.body}
      {#if cb.comment.voice}<span class="muted"> · voice note</span>{/if}
    </blockquote>
    <p class="spot" data-testid="callback-spot">
      {spotText(cb)}{#if roughSpot(cb)}<span class="muted"> · rough, listen around it</span>{/if}
    </p>
    <div class="row">
      <button type="button" class="hear" data-testid="callback-hear" onclick={hearNew}>Hear the new take at the spot</button>
      {#if cb.spot.source_at_s !== null || cb.comment.at_second !== null}
        <button type="button" class="hear" data-testid="callback-hear-original" onclick={hearOriginal}>Hear the original</button>
      {/if}
    </div>
    <h2 class="q">{question(cb)}</h2>
    {#if mine}
      <p data-testid="callback-answered">You answered: <strong>{verdictLabel(mine.verdict)}</strong></p>
    {:else}
      <div class="answers" role="group" aria-label="Your answer">
        {#each buttons as b (b.verdict)}
          <button type="button" class="ans" data-verdict={b.verdict} disabled={busy} onclick={() => void answer(b.verdict)}>{b.label}</button>
        {/each}
      </div>
      <label class="note">Note (optional) <input bind:value={note} maxlength="500" /></label>
    {/if}
    {#if cb.critic_verdicts.length > 0}
      <p class="muted" data-testid="callback-critics">Critic: {cb.critic_verdicts.map((v) => verdictLabel(v.verdict)).join(", ")}</p>
    {/if}
    {#if result}<p role="status" data-testid="callback-result">{result}</p>{/if}
    {#if error}<p role="alert">{error}</p>{/if}
    <div class="row">
      {#if next}<button type="button" data-testid="callback-next" onclick={() => navigate(`/judge/callbacks/${encodeURIComponent(next!)}`)}>Next callback</button>{/if}
      <a class="btn" href={cb.links.child}>Open the take</a>
      <a class="btn" href="/judge/callbacks">All callbacks</a>
    </div>
  {/if}
</section>

<style>
  .card {
    display: grid;
    gap: var(--sp-3);
    min-width: 0;
  }
  .kind,
  .spot {
    margin: 0;
    overflow-wrap: anywhere;
  }
  .said {
    margin: 0;
    padding: var(--sp-3);
    border-left: 3px solid var(--accent);
    background: var(--accent-soft);
    overflow-wrap: anywhere;
  }
  .row {
    display: flex;
    flex-wrap: wrap;
    gap: var(--sp-2);
  }
  .q {
    margin: 0;
  }
  .hear,
  .btn,
  .row button {
    min-height: var(--hit);
    padding: 0 var(--sp-4);
  }
  .answers {
    display: grid;
    grid-template-columns: repeat(auto-fit, minmax(96px, 1fr));
    gap: var(--sp-2);
  }
  .ans {
    min-height: var(--hit-lg);
    font-size: 1.1rem;
    font-weight: var(--fw-bold);
  }
  .note {
    display: grid;
    gap: var(--sp-1);
  }
  .note input {
    min-height: var(--hit);
    max-width: 100%;
    box-sizing: border-box;
  }
  .btn {
    display: inline-flex;
    align-items: center;
    border: 1px solid var(--line-strong);
    border-radius: var(--r-md);
    color: var(--text);
    text-decoration: none;
  }
</style>
