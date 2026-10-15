<script lang="ts">
  import { api } from "../../api/client";
  import type { components } from "../../api/schema";
  import { eventText } from "../songView";

  type Ev = components["schemas"]["PickEvent"];
  interface Props {
    songId: string;
    /** The newest events already in get_song. */
    events: Ev[];
  }
  let { songId, events }: Props = $props();

  let all = $state<Ev[] | null>(null);
  let busy = $state(false);
  let error = $state("");
  const shown = $derived(all ?? events);

  async function loadAll() {
    busy = true;
    error = "";
    try {
      const out: Ev[] = [];
      let after: number | undefined;
      for (let i = 0; i < 20; i++) {
        const r = await api.GET("/api/pick-events", { params: { query: { song_id: songId, limit: 50, ...(after !== undefined ? { after } : {}) } } });
        if (!r.data) {
          error = r.error?.message ?? "Failed to load the history";
          return;
        }
        out.push(...r.data.items);
        if (r.data.next_cursor === null) break;
        after = r.data.next_cursor;
      }
      all = out;
    } catch {
      error = "Failed to load the history";
    } finally {
      busy = false;
    }
  }
</script>

<section data-testid="pick-history" aria-labelledby="pick-history-h">
  <h2 id="pick-history-h">Pick history</h2>
  {#if shown.length === 0}
    <p class="muted">No Pick yet.</p>
  {:else}
    <ol>
      {#each shown as e (e.id)}
        <li data-testid="pick-event"><time datetime={e.at}>{e.at.slice(0, 16).replace("T", " ")}</time> · {eventText(e)}</li>
      {/each}
    </ol>
  {/if}
  {#if all === null}
    <button type="button" onclick={loadAll} disabled={busy} data-testid="pick-history-all">Full history</button>
  {/if}
  {#if error}<p role="alert">{error}</p>{/if}
</section>

<style>
  ol {
    padding-left: 1.2rem;
    margin: 0.3rem 0;
  }
</style>
