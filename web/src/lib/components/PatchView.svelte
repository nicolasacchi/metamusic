<script lang="ts">
  // The Song PR page body (M5-W1, C253, C262): what the patch addresses, its scope and bounds, the diff, the renders and the
  // callback tally. Rendering goes through the M3 confirm flow (/human/generate), unchanged.
  import { api } from "../../api/client";
  import { mayGenerate, startGeneration, type GenerationIntent, type Viewer } from "../genIntent";
  import { PATCH_BOUNDS, renderIntent, type SongPR } from "../songPrApi";
  import { callbackTally, loadPatch, renderCount, UNAVAILABLE_TEXT, type Load } from "../songPrView";
  import { fetchViewer } from "../viewer";

  interface Props {
    id: string;
    navigate: (to: string) => void;
    /** Pre-loaded patch (server-side rendering in unit tests; the page otherwise fetches it). */
    initial?: Load<SongPR>;
    /** The caller (unit tests pass it; the page otherwise asks /api/me once). */
    viewer?: Viewer | null;
  }
  let { id, navigate, initial, viewer = null }: Props = $props();

  // svelte-ignore state_referenced_locally
  let patch = $state<Load<SongPR>>(initial ?? { state: "loading" });
  // svelte-ignore state_referenced_locally
  let me = $state<Viewer | null>(viewer);
  let seeds = $state(2);

  $effect(() => {
    if (initial) return;
    const mine = id;
    patch = { state: "loading" };
    void loadPatch(api, mine).then((r) => {
      if (mine === id) patch = r;
    });
  });
  $effect(() => {
    if (viewer) return;
    void fetchViewer().then((v) => (me = v));
  });

  const renders = $derived(patch.state === "ready" ? renderCount(patch.value.renders) : 0);
  const atLimit = $derived(renders >= PATCH_BOUNDS.maxRenders);
  // The server sends style: null for a lyrics-only patch (songpr.Diff), although the generated type says object.
  const st = $derived(patch.state === "ready" ? (patch.value.diff.style as (typeof patch.value.diff)["style"] | null) : null);

  function render() {
    startGeneration(sessionStorage, renderIntent(id, seeds) as GenerationIntent, navigate);
  }
</script>

<section class="patch" data-testid="patch-view" data-patch={id} data-state={patch.state}>
  {#if patch.state === "loading"}
    <p class="muted">Loading…</p>
  {:else if patch.state === "unavailable"}
    <p class="muted" data-testid="patch-unavailable">{UNAVAILABLE_TEXT}</p>
  {:else if patch.state === "error"}
    <p role="alert" data-testid="patch-error">{patch.message}</p>
    <p><a class="tap" href="/library">Back to the library</a></p>
  {:else}
    {@const p = patch.value}
    <header>
      <h2 data-testid="patch-title">{p.title ?? "Song PR"}</h2>
      <p class="muted meta">
        {p.operator.replace("_", " ")} · {p.author.name} · {p.created_at.slice(0, 10)} · on
        <a class="tap" href="/tracks/{encodeURIComponent(p.base.track_id)}" data-testid="patch-base">{p.base.title}</a>
      </p>
      <p class="why" data-testid="patch-rationale">{p.rationale}</p>
    </header>

    <section class="card" data-testid="patch-addresses">
      <h3>Addresses</h3>
      <ul>
        {#each p.addresses as a (a.comment_id)}
          <li data-testid="patch-address"><span class="badge">{a.state}</span> <a class="tap" href="/tracks/{encodeURIComponent(a.track_id)}">{a.body_excerpt}</a></li>
        {/each}
      </ul>
    </section>

    <section class="card" data-testid="patch-scope">
      <h3>Scope</h3>
      <p class="muted" data-testid="patch-bounds">
        {p.bounds.bounds.lines_changed}/{p.bounds.bounds.max_lines} lines · {p.bounds.bounds.sections_scoped}/{p.bounds.bounds.max_sections} sections ·
        {p.bounds.bounds.style_terms_changed}/{p.bounds.bounds.max_style_terms} style terms · BPM Δ {p.bounds.bounds.bpm_delta}/±{p.bounds.bounds.max_bpm_delta}
      </p>
      {#if p.bounds.sections?.length}
        <ul class="chips">
          {#each p.bounds.sections as s (s.idx)}<li class="badge" data-testid="patch-scoped">{s.label}{s.extra ? " (extra)" : ""}</li>{/each}
        </ul>
      {/if}
    </section>

    <section class="card" data-testid="patch-diff">
      <h3>Diff</h3>
      {#each p.diff.lyrics ?? [] as l (`${l.section_idx}:${l.line_idx}`)}
        <div class="change" data-testid="patch-line">
          <p class="before"><span aria-hidden="true">−</span><span class="sr">Before: </span>{l.before}</p>
          <p class="after"><span aria-hidden="true">+</span><span class="sr">After: </span>{l.after}</p>
        </div>
      {/each}
      {#if st && st.before !== st.after}
        <div class="change" data-testid="patch-style">
          <p class="before"><span aria-hidden="true">−</span><span class="sr">Before: </span>{st.before}</p>
          <p class="after"><span aria-hidden="true">+</span><span class="sr">After: </span>{st.after}</p>
          <p class="muted meta">
            {#each st.added ?? [] as t (t)}<span class="badge add">+ {t}</span> {/each}
            {#each st.removed ?? [] as t (t)}<span class="badge del">− {t}</span> {/each}
          </p>
        </div>
      {/if}
    </section>

    <section class="card" data-testid="patch-renders">
      <h3>Renders</h3>
      {#if p.renders.length === 0}
        <p class="muted">Not rendered yet.</p>
      {:else}
        <ul>
          {#each p.renders as r (r.job_id)}
            <li data-testid="patch-render">
              seed {r.seed} · {r.state}
              {#if r.child_track_id}· <a class="tap" href="/tracks/{encodeURIComponent(r.child_track_id)}">listen</a>{/if}
              · <a class="tap" href="/jobs/{encodeURIComponent(r.job_id)}">job</a>
            </li>
          {/each}
        </ul>
      {/if}
      <p class="muted" data-testid="patch-tally">{callbackTally(p.callbacks)} · <a class="tap" href="/judge/callbacks">Callbacks</a></p>
      {#if mayGenerate(me)}
        <div class="render" data-testid="patch-render-form">
          <label>Seeds
            <select bind:value={seeds} data-testid="patch-seeds" disabled={atLimit}>
              {#each [1, 2, 3] as n (n)}<option value={n}>{n}</option>{/each}
            </select>
          </label>
          <button type="button" class="primary" onclick={render} disabled={atLimit} data-testid="patch-render-button">Render…</button>
        </div>
        {#if atLimit}<p class="muted" data-testid="patch-render-limit">This patch has {PATCH_BOUNDS.maxRenders} renders, the most one patch gets.</p>{/if}
      {/if}
    </section>
  {/if}
</section>

<style>
  .patch {
    display: grid;
    gap: var(--sp-3);
    min-width: 0;
    max-width: 100%;
  }
  h2,
  h3 {
    margin: 0 0 var(--sp-2);
  }
  h3 {
    font-size: var(--fs-md);
  }
  .meta {
    margin: 0;
    overflow-wrap: anywhere;
  }
  .why {
    margin: var(--sp-2) 0 0;
    overflow-wrap: anywhere;
  }
  .card {
    border: 1px solid #3a322b;
    border-radius: 0.5rem;
    padding: 0.5rem 0.9rem;
    min-width: 0;
  }
  ul {
    list-style: none;
    margin: 0;
    padding: 0;
    display: grid;
    gap: var(--sp-2);
    overflow-wrap: anywhere;
  }
  ul.chips {
    display: flex;
    flex-wrap: wrap;
  }
  .badge {
    background: #3a322b;
    border-radius: 0.6rem;
    padding: 0 0.45rem;
    font-size: var(--fs-xs);
  }
  .change p {
    margin: 0;
    padding: 0.1rem 0.4rem;
    overflow-wrap: anywhere;
    white-space: pre-wrap;
  }
  .before {
    background: color-mix(in srgb, var(--bad) 18%, transparent);
  }
  .after {
    background: color-mix(in srgb, var(--accent) 16%, transparent);
  }
  .change {
    margin-bottom: var(--sp-2);
  }
  .render {
    display: flex;
    flex-wrap: wrap;
    gap: var(--sp-3);
    align-items: center;
  }
  select,
  button {
    min-height: var(--hit);
    min-width: var(--hit);
  }
  .sr {
    position: absolute;
    width: 1px;
    height: 1px;
    overflow: hidden;
    clip-path: inset(50%);
  }
</style>
