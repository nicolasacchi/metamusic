<script lang="ts">
  import { untrack } from "svelte";
  import { api as defaultApi } from "../../api/client";
  import type { components } from "../../api/schema";
  import { exportTape, getTape, type Tape } from "../curationApi";
  import type { Viewer } from "../recordView";
  import { lossyBanner, nextPollMs, planRows, progressText, tapeErrorText } from "../tapeView";
  import { player } from "../player.svelte";
  import { playTape, seekToChapter, tapeItem } from "../recordPlayer";
  import ChapterList from "./ChapterList.svelte";

  type Brief = components["schemas"]["TapeBrief"];
  interface Props {
    recordId: string;
    revision: number;
    status: "draft" | "proposed" | "published";
    briefs: readonly Brief[];
    /** slot id -> Take title (for the plan table and the chapters). */
    titles: ReadonlyMap<string, string>;
    /** Warnings the Record carries now (to word the review step). */
    warningCount: number;
    viewer: Viewer | null;
    /** The Record changed under us (409 STALE_REVISION). */
    onstale?: () => void;
    api?: typeof defaultApi;
    /** The Record's title: the queue label and mini-player title of a played tape. */
    recordTitle?: string;
    /** Test seam: a Tape to show without any request. */
    initialTape?: Tape | null;
  }
  let { recordId, revision, status, briefs, titles, warningCount, viewer, onstale, recordTitle = "", api = defaultApi, initialTape = null }: Props = $props();

  // svelte-ignore state_referenced_locally
  let tape = $state<Tape | null>(initialTape);
  let message = $state("");
  let needsReview = $state(false);
  let busy = $state(false);
  let unavailable = $state(false);
  let gaveUp = $state(false);
  let startedAt = 0;

  const human = $derived(viewer?.kind === "human");
  const canExport = $derived(viewer !== null && viewer.scopes.includes("write") && status === "published");

  // Show the newest tape the Record already has.
  $effect(() => {
    if (untrack(() => tape)) return;
    const b = briefs[0];
    if (!b) return;
    void getTape(api, b.id).then((r) => {
      if (r.kind === "ok") tape = r.value;
      else if (r.kind === "unavailable") unavailable = true;
    });
  });

  // Poll while queued or running (1 s, 15 min at most).
  $effect(() => {
    const t = tape;
    if (!t) return;
    const wait = nextPollMs(t.state, startedAt === 0 ? 0 : Date.now() - startedAt);
    if (wait === null) {
      if (t.state === "queued" || t.state === "running") gaveUp = true;
      return;
    }
    if (startedAt === 0) startedAt = Date.now();
    const h = setTimeout(async () => {
      const r = await getTape(api, t.id);
      if (r.kind === "ok") tape = r.value;
      else if (r.kind === "unavailable") unavailable = true;
    }, wait);
    return () => clearTimeout(h);
  });

  async function start(accept: boolean) {
    if (busy) return;
    busy = true;
    message = "";
    gaveUp = false;
    startedAt = 0;
    const r = await exportTape(api, recordId, { revision, ...(accept ? { accept_warnings: true } : {}) })();
    busy = false;
    if (r.kind === "ok") {
      tape = r.value;
      needsReview = false;
    } else if (r.kind === "unavailable") unavailable = true;
    else if (r.kind === "stale") onstale?.();
    else if (r.kind === "failed") {
      if (r.code === "TAPE_NEEDS_REVIEW" && human) needsReview = true;
      else message = tapeErrorText(r.status, r.code, r.message);
    } else message = "Could not reach the server.";
  }
  const banner = $derived(tape ? lossyBanner(tape) : null);

  // Play as tape / chapter seeking (W3): a ready tape cut from this revision plays at once, inside the click.
  const playing = $derived(!!tape && player.state.trackId === tapeItem(tape.id));
  const names = $derived(Object.fromEntries(titles));
  function playNow(startAtMs = 0) {
    if (!tape) return;
    playTape(tape, { recordId, title: recordTitle, names, startAtMs });
  }
  function pick(index: number) {
    if (!tape) return;
    if (playing) seekToChapter(tape, index);
    else playNow(tape.chapters?.find((c) => c.index === index)?.start_ms ?? 0);
  }
</script>

<section aria-label="Tapes" data-testid="tape-panel">
  {#if unavailable}
    <p class="muted" role="status" data-testid="tape-unavailable">Tape export is not available yet.</p>
  {:else}
    {#if status !== "published"}
      <p class="muted">Publish this record to export it as one gapless tape.</p>
    {/if}
    {#if canExport}
      <button type="button" class="primary" disabled={busy || tape?.state === "queued" || tape?.state === "running"} onclick={() => start(false)} data-testid="tape-export">Export tape</button>
    {/if}
    {#if needsReview}
      <div class="review" role="alertdialog" aria-label="Review warnings" data-testid="tape-review">
        <p>This record has {warningCount} {warningCount === 1 ? "warning" : "warnings"}. Export anyway?</p>
        <button type="button" class="primary" disabled={busy} onclick={() => start(true)} data-testid="tape-accept">Accept {warningCount} {warningCount === 1 ? "warning" : "warnings"}</button>
        <button type="button" onclick={() => (needsReview = false)}>Cancel</button>
      </div>
    {/if}
    {#if message}<p role="alert" data-testid="tape-error">{message}</p>{/if}

    {#if tape}
      <p role="status" data-testid="tape-progress" data-state={tape.state}>
        {progressText(tape)}{#if tape.reused} · reused an identical export{/if}
      </p>
      {#if gaveUp}<p class="muted">Still working. Reload this page to check again.</p>{/if}
      {#if banner}<p class="banner" role="note" data-testid="tape-lossy">{banner}</p>{/if}
      {#if tape.state === "ready" && tape.download}
        <p>
          <a class="tap" href={tape.download.url} download={tape.download.filename} data-testid="tape-download">Download {tape.download.filename}</a>
          {#if tape.download.cue_url}<a class="tap" href={tape.download.cue_url}>Cue sheet</a>{/if}
          {#if tape.record_revision === revision}
            <button type="button" class="primary" onclick={() => playNow()} data-testid="tape-play">{playing ? "Restart tape" : "Play as tape"}</button>
          {/if}
        </p>
      {/if}
      {#if tape.items.length > 0}
        <h3>Plan</h3>
        <div class="scroll">
          <table data-testid="tape-plan">
            <thead><tr><th>#</th><th>Track</th><th>Gain</th><th>Level</th><th>Result</th></tr></thead>
            <tbody>
              {#each planRows(tape, titles) as r (r.slotId)}
                <tr>
                  <td>{r.position}</td><td>{r.title}{#each r.flags as f (f)} <span class="chip">{f}</span>{/each}</td><td class="num">{r.gain}</td><td class="num">{r.predicted}</td><td>{r.status}</td>
                </tr>
              {/each}
            </tbody>
          </table>
        </div>
      {/if}
      {#if tape.chapters && tape.chapters.length > 0}
        <h3>Chapters</h3>
        <div data-testid="tape-chapters">
          <ChapterList chapters={tape.chapters} {names} positionMs={playing ? Math.round(player.state.current * 1000) : null} onselect={pick} />
        </div>
      {/if}
    {:else if briefs.length === 0 && canExport}
      <p class="muted">No tape yet.</p>
    {/if}
  {/if}
</section>

<style>
  section {
    display: grid;
    gap: var(--sp-2);
  }
  .review {
    border: 1px solid var(--warn);
    background: var(--warn-soft);
    border-radius: var(--r-md);
    padding: var(--sp-3);
    display: flex;
    flex-wrap: wrap;
    gap: var(--sp-2);
    align-items: center;
  }
  .review p {
    flex-basis: 100%;
    margin: 0;
  }
  .banner {
    border: 1px solid var(--warn);
    background: var(--warn-soft);
    border-radius: var(--r-md);
    padding: var(--sp-2) var(--sp-3);
    margin: 0;
  }
  .scroll {
    overflow-x: auto;
  }
  table {
    border-collapse: collapse;
    width: 100%;
    font-size: var(--fs-sm);
  }
  th,
  td {
    text-align: left;
    padding: var(--sp-1) var(--sp-2);
    border-bottom: 1px solid var(--line);
  }
  h3 {
    margin: var(--sp-3) 0 0;
    font-size: var(--fs-md);
  }
</style>
