<script lang="ts">
  // The Song PR panel of the Track page (M5-W1, C253, C262): open fix comments with their spots, the "Propose a patch" form
  // (the bounds are shown next to what the draft uses), and the patches of this take with renders and callback tallies.
  import { api } from "../../api/client";
  import { idempotentAction } from "../../api/idem";
  import type { components } from "../../api/schema";
  import { jumpTo } from "../jump";
  import { PATCH_BOUNDS, proposePatch } from "../songPrApi";
  import {
    addressable,
    boundsUsage,
    buildOps,
    callbackTally,
    draftProblem,
    guarded,
    isUnavailable,
    loadPatchList,
    renderCount,
    spotText,
    UNAVAILABLE_TEXT,
    type Load,
    type PatchDraft,
  } from "../songPrView";

  type S = components["schemas"];
  interface Props {
    trackId: string;
    /** The comments the Track page already loaded (shared with its Comments panel). */
    comments?: S["Comment"][];
    /** The base lyrics sections (SectionOut.idx / lines are what replace_line addresses). */
    sections?: readonly S["SectionOut"][];
    /** The base recipe style, the starting point of the style field. */
    baseStyle?: string;
    /** Proposing is for the operator; agents use propose_patch directly. */
    canPropose?: boolean;
    navigate?: (to: string) => void;
    /** Pre-loaded list (server-side rendering in unit tests; the panel otherwise fetches it). */
    initial?: Load<S["PatchPage"]>;
  }
  let { trackId, comments = [], sections = [], baseStyle = "", canPropose = false, navigate, initial }: Props = $props();

  // svelte-ignore state_referenced_locally
  let patches = $state<Load<S["PatchPage"]>>(initial ?? { state: "loading" });
  $effect(() => {
    if (initial) return;
    const mine = trackId;
    patches = { state: "loading" };
    void loadPatchList(api, mine).then((r) => {
      if (mine === trackId) patches = r;
    });
  });

  const fixes = $derived(addressable(comments));

  let open = $state(false);
  let picked = $state<string[]>([]);
  let rationale = $state("");
  let title = $state("");
  // svelte-ignore state_referenced_locally
  let style = $state(baseStyle);
  let editing = $state<number[]>([]);
  let texts = $state<Record<string, string>>({});
  let busy = $state(false);
  let message = $state("");
  let unavailable = $state(false);
  let action: (() => Promise<Awaited<ReturnType<typeof proposePatch>>>) | null = null;
  let actionSig = "";

  const sectionList = $derived(editing.flatMap((i) => sections.filter((s) => s.idx === i)));
  const draft = $derived<PatchDraft>({
    comments: picked,
    rationale,
    title,
    style,
    baseStyle,
    lines: sectionList.flatMap((s) => s.lines.map((before, line_idx) => ({ section_idx: s.idx, line_idx, before, text: texts[`${s.idx}:${line_idx}`] ?? before }))),
  });
  const usage = $derived(boundsUsage(draft));
  const problem = $derived(draftProblem(draft));
  const addable = $derived(sections.filter((s) => s.lines.length > 0 && !editing.includes(s.idx)));

  function toggle(id: string, on: boolean) {
    picked = on ? [...picked, id] : picked.filter((x) => x !== id);
    // Suggest the sections the picked comments sit in, once, until the user chooses their own.
    if (on && editing.length === 0) {
      const c = fixes.find((x) => x.id === id);
      const hit = c?.section ? sections.find((s) => s.label === c.section?.label && s.lines.length > 0) : undefined;
      if (hit) editing = [hit.idx];
    }
  }
  function addSection(e: Event) {
    const el = e.currentTarget as HTMLSelectElement;
    const n = Number(el.value);
    el.value = "";
    if (Number.isInteger(n) && !editing.includes(n)) editing = [...editing, n];
  }
  function go(to: string) {
    if (navigate) navigate(to);
    else {
      history.pushState(null, "", to);
      window.dispatchEvent(new PopStateEvent("popstate"));
    }
  }

  async function submit(e: Event) {
    e.preventDefault();
    if (busy) return;
    message = "";
    const bad = draftProblem(draft);
    if (bad) {
      message = bad;
      return;
    }
    const input = {
      baseTrackId: trackId,
      addressesCommentIds: picked,
      rationale: rationale.trim(),
      ...(title.trim() ? { title: title.trim() } : {}),
      ops: buildOps(draft),
    };
    // One key per user action: a retry of the same content reuses it; edited content is a new action.
    const sig = JSON.stringify(input);
    if (!action || sig !== actionSig) {
      actionSig = sig;
      action = idempotentAction((key) => proposePatch(api, key, input));
    }
    busy = true;
    try {
      const r = await guarded("Could not propose the patch", () => action!());
      if (r.ok) {
        action = null;
        go(`/patches/${encodeURIComponent(r.value.id)}`);
      } else if (isUnavailable(r)) unavailable = true;
      else message = r.message;
    } finally {
      busy = false;
    }
  }
</script>

<section class="card" data-testid="song-pr-panel" data-track={trackId}>
  <h2>Song PR</h2>

  {#if unavailable || patches.state === "unavailable"}
    <p class="muted" data-testid="song-pr-unavailable">{UNAVAILABLE_TEXT}</p>
  {/if}

  {#if fixes.length === 0}
    <p class="muted" data-testid="song-pr-no-fixes">No open fix comments. A fix comment on a moment you want changed starts a patch.</p>
  {:else}
    <ul class="fixes" data-testid="song-pr-fixes">
      {#each fixes as c (c.id)}
        <li data-testid="song-pr-fix" data-comment-id={c.id}>
          {#if open}
            <label class="pick">
              <input type="checkbox" checked={picked.includes(c.id)} onchange={(e) => toggle(c.id, e.currentTarget.checked)} data-testid="song-pr-pick" />
              <span class="what"><span class="spot">{spotText(c)}</span> <span class="body">{c.body}</span></span>
            </label>
          {:else}
            <span class="what">
              {#if c.at_second !== null}
                <button type="button" class="spotbtn" onclick={() => jumpTo(trackId, c.at_second ?? 0)} title="Jump to this moment">{spotText(c)}</button>
              {:else}
                <span class="spot">{spotText(c)}</span>
              {/if}
              <span class="body">{c.body}</span>
              {#if c.state === "regressed"}<span class="badge">regressed</span>{/if}
            </span>
          {/if}
        </li>
      {/each}
    </ul>
  {/if}

  {#if canPropose && !unavailable && patches.state !== "unavailable" && fixes.length > 0}
    {#if !open}
      <button type="button" class="primary" data-testid="song-pr-open" onclick={() => (open = true)}>Propose a patch</button>
    {:else}
      <form class="form" onsubmit={submit} data-testid="song-pr-form">
        <p class="bounds muted" data-testid="song-pr-bounds">
          Bounds: up to {PATCH_BOUNDS.maxLines} lines, {PATCH_BOUNDS.maxSections} sections, {PATCH_BOUNDS.maxStyleTerms} style terms, BPM ±{PATCH_BOUNDS.maxBPM}. No bar edits.
          <span class="use" class:over={usage.over.length > 0} data-testid="song-pr-usage">
            This patch: {usage.lines}/{usage.maxLines} lines · {usage.sections}/{usage.maxSections} sections · {usage.styleTerms}/{usage.maxStyleTerms} style terms
          </span>
        </p>
        <p class="hint muted">Tick the fix comments this patch addresses.</p>

        <label class="field">Why
          <textarea bind:value={rationale} rows="2" maxlength="2000" placeholder="What this patch fixes" data-testid="song-pr-rationale"></textarea>
        </label>
        <label class="field">Title (optional)
          <input type="text" bind:value={title} maxlength="120" data-testid="song-pr-title" />
        </label>
        <label class="field">Style
          <textarea bind:value={style} rows="2" maxlength="1000" data-testid="song-pr-style"></textarea>
        </label>

        {#each sectionList as s (s.idx)}
          <fieldset class="sec" data-testid="song-pr-section" data-idx={s.idx}>
            <legend>{s.label}</legend>
            {#each s.lines as before, i (i)}
              <input
                type="text"
                class="line"
                value={texts[`${s.idx}:${i}`] ?? before}
                oninput={(e) => (texts[`${s.idx}:${i}`] = e.currentTarget.value)}
                maxlength="400"
                aria-label="{s.label} line {i + 1}"
                data-testid="song-pr-line"
              />
            {/each}
          </fieldset>
        {/each}
        {#if addable.length > 0}
          <label class="field">Edit lyrics of a section
            <select onchange={addSection} data-testid="song-pr-add-section">
              <option value="">Add a section…</option>
              {#each addable as s (s.idx)}<option value={s.idx}>{s.label}</option>{/each}
            </select>
          </label>
        {/if}

        {#if message}<p role="alert" class="err" data-testid="song-pr-error">{message}</p>{/if}
        <div class="acts">
          <button type="submit" class="primary" disabled={busy || problem !== null} data-testid="song-pr-submit">{busy ? "Proposing…" : "Propose"}</button>
          <button type="button" onclick={() => (open = false)} disabled={busy}>Cancel</button>
        </div>
        {#if problem && (rationale || picked.length)}<p class="muted hint" data-testid="song-pr-problem">{problem}</p>{/if}
      </form>
    {/if}
  {/if}

  {#if patches.state === "loading"}
    <p class="muted">Loading patches…</p>
  {:else if patches.state === "error"}
    <p class="muted" role="alert" data-testid="song-pr-list-error">{patches.message}</p>
  {:else if patches.state === "ready" && patches.value.items.length > 0}
    <h3>Patches</h3>
    <ul class="patches" data-testid="song-pr-patches">
      {#each patches.value.items as p (p.id)}
        <li data-testid="song-pr-patch" data-patch-id={p.id}>
          <a class="tap name" href="/patches/{encodeURIComponent(p.id)}">{p.title ?? p.rationale}</a>
          <span class="muted meta">{p.operator.replace("_", " ")} · {p.author.name} · {p.created_at.slice(0, 10)}</span>
          <span class="muted meta" data-testid="song-pr-renders">{renderCount(p.renders)} render{renderCount(p.renders) === 1 ? "" : "s"} · {callbackTally(p.callbacks)}</span>
        </li>
      {/each}
    </ul>
  {/if}
</section>

<style>
  section {
    min-width: 0;
  }
  ul {
    list-style: none;
    padding: 0;
    margin: 0 0 var(--sp-3);
    display: grid;
    gap: var(--sp-2);
  }
  h3 {
    margin: var(--sp-3) 0 var(--sp-2);
    font-size: var(--fs-md);
  }
  .what {
    display: flex;
    flex-wrap: wrap;
    gap: 0.2rem 0.5rem;
    align-items: center;
    min-width: 0;
    overflow-wrap: anywhere;
  }
  .spot,
  .spotbtn {
    font-variant-numeric: tabular-nums;
    font-weight: 700;
  }
  .spotbtn {
    min-height: var(--hit);
    max-width: 100%;
  }
  .badge {
    background: #3a322b;
    border-radius: 0.6rem;
    padding: 0 0.45rem;
    font-size: var(--fs-xs);
  }
  .pick {
    display: flex;
    gap: var(--sp-3);
    align-items: center;
    min-height: var(--hit);
  }
  .pick input {
    flex: none;
    width: 1.4rem;
    height: 1.4rem;
  }
  .form {
    display: grid;
    gap: var(--sp-3);
    margin: var(--sp-3) 0;
    min-width: 0;
  }
  .bounds,
  .hint {
    margin: 0;
    font-size: var(--fs-sm);
  }
  .use {
    display: block;
    color: var(--text);
  }
  .use.over {
    color: var(--bad);
  }
  .field {
    display: grid;
    gap: var(--sp-1);
    font-size: var(--fs-sm);
    min-width: 0;
  }
  textarea,
  input[type="text"],
  select {
    box-sizing: border-box;
    width: 100%;
    min-width: 0;
    background: var(--bg-sunken);
    border: 1px solid var(--line-strong);
    border-radius: var(--r-sm);
    padding: var(--sp-2);
  }
  input[type="text"],
  select {
    min-height: var(--hit);
  }
  .sec {
    border: 1px solid var(--line-strong);
    border-radius: var(--r-sm);
    padding: var(--sp-2);
    display: grid;
    gap: var(--sp-2);
    min-width: 0;
    margin: 0;
  }
  .acts {
    display: flex;
    flex-wrap: wrap;
    gap: var(--sp-2);
  }
  button {
    min-height: var(--hit);
    min-width: var(--hit);
  }
  .err {
    color: var(--bad);
    margin: 0;
  }
  .patches li {
    display: grid;
    gap: 0.1rem;
    min-width: 0;
  }
  .name {
    overflow-wrap: anywhere;
  }
  .meta {
    font-size: var(--fs-sm);
    overflow-wrap: anywhere;
  }
</style>
