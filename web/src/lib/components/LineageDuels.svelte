<script lang="ts">
  import { api } from "../../api/client";
  import { newIdempotencyKey } from "../../api/idem";
  import type { components } from "../../api/schema";
  import { createDuel } from "../duelApi";
  import { blindRoundPairs, canBlindRound, cryptoRandom } from "../duelRound";
  import { runBlindRound, type RoundProgress } from "../duelRoundRun";
  import { ladderRows } from "../ladderView";

  interface Props {
    trackId: string;
    rootId: string;
    parentId: string | null;
    navigate: (to: string) => void;
  }
  let { trackId, rootId, parentId, navigate }: Props = $props();

  let ladder = $state<components["schemas"]["LadderPage"] | null>(null);
  let trackIds = $state<string[]>([]);
  let progress = $state<RoundProgress | null>(null);
  let busy = $state(false);
  let error = $state("");
  const rows = $derived(ladder ? ladderRows(ladder, 100).rows : []);

  $effect(() => {
    const root = rootId;
    ladder = null;
    trackIds = [];
    void (async () => {
      try {
        const [l, t] = await Promise.all([
          api.GET("/api/ladder", { params: { query: { scope: "lineage", root_track_id: root, limit: 50 } } }),
          api.GET("/api/tracks", { params: { query: { root_track_id: root, limit: 100 } } }),
        ]);
        if (root !== rootId) return;
        ladder = l.data ?? null;
        trackIds = t.data?.items.map((x) => x.id) ?? [];
      } catch {
        /* the block simply stays empty */
      }
    })();
  });

  async function blindRound() {
    if (busy || !canBlindRound(trackIds.length)) return;
    busy = true;
    error = "";
    const pairs = blindRoundPairs(trackIds, cryptoRandom);
    const r = await runBlindRound(pairs, {
      create: async (p, key) => ({ id: (await createDuel(api, p[0], p[1], key, { blind: true, matchLoudness: true })).id }),
      navigate,
      onProgress: (p) => (progress = p),
    });
    if (r.error) error = r.error;
    busy = false;
  }

  let abBusy = $state(false);
  async function abWithParent() {
    if (!parentId || abBusy) return;
    abBusy = true;
    error = "";
    try {
      const d = await createDuel(api, parentId, trackId, newIdempotencyKey(), { blind: false, matchLoudness: true });
      navigate(`/duels/${encodeURIComponent(d.id)}`);
    } catch (e) {
      error = e instanceof Error ? e.message : "Could not create the duel";
    } finally {
      abBusy = false;
    }
  }
</script>

<section class="card" aria-label="Lineage duels" data-testid="lineage-duels">
  <h2>Lineage duels</h2>
  {#if rows.length > 0}
    <ol>
      {#each rows as r (r.trackId)}
        <li><a class="tap" href={r.href}>{r.title}</a> <span class="muted">{r.u} · {r.record}</span></li>
      {/each}
    </ol>
  {:else}
    <p class="muted">No judged duels in this lineage yet.</p>
  {/if}
  <div class="actions">
    <button type="button" onclick={() => void blindRound()} disabled={busy || !canBlindRound(trackIds.length)} data-testid="blind-round">Blind round</button>
    {#if !canBlindRound(trackIds.length)}<span class="muted">Needs at least 2 tracks in the lineage.</span>{/if}
    {#if parentId}
      <button type="button" onclick={() => void abWithParent()} disabled={abBusy} data-testid="ab-parent">A/B with parent</button>
      <a class="tap" href="/compare?a={parentId}&b={trackId}">Compare with parent</a>
    {/if}
  </div>
  {#if progress && busy}
    <p role="status">{progress.waitingS > 0 ? `Waiting ${progress.waitingS} s (rate limit)…` : `Creating duels ${progress.done}/${progress.total}…`}</p>
  {/if}
  {#if error}<p role="alert">{error}</p>{/if}
</section>

<style>
  .card {
    border: 1px solid #3a322b;
    border-radius: 0.5rem;
    padding: 0.5rem 0.9rem;
  }
  h2 {
    margin: 0 0 0.4rem;
    font-size: 1rem;
  }
  ol {
    margin: 0;
    padding-left: 1.2rem;
  }
  .actions {
    display: flex;
    flex-wrap: wrap;
    gap: 0.5rem;
    align-items: center;
    margin-top: 0.5rem;
  }
</style>
