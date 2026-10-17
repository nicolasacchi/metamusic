<script lang="ts">
  import { api } from "../api/client";
  import type { components } from "../api/schema";
  import { player } from "../lib/player.svelte";
  import { fmt } from "../lib/time";
  import PendingApprovals from "../lib/components/PendingApprovals.svelte";
  import PlayIcon from "../lib/components/icons/PlayIcon.svelte";
  import CoverArt from "../lib/components/CoverArt.svelte";
  import QueueButtons from "../lib/components/QueueButtons.svelte";

  type Track = components["schemas"]["TrackSummary"];

  // Placeholder landing (wave 2): the real inbox comes later. Pending approvals plus the newest takes.
  let takes = $state<Track[]>([]);
  let loading = $state(true);
  let error = $state("");

  $effect(() => {
    void (async () => {
      try {
        const r = await api.GET("/api/tracks", { params: { query: { sort: "recorded_desc", limit: 20 } } });
        if (r.data) takes = r.data.items;
        else error = r.error?.message ?? "Failed to load takes";
      } catch {
        error = "Failed to load takes";
      } finally {
        loading = false;
      }
    })();
  });

  function play(t: Track) {
    player.play(t.id, takes.map((x) => x.id), { title: t.title });
  }
  const day = (iso: string) => iso.slice(0, 10);
</script>

<h1>New</h1>
<p class="muted" data-testid="new-soon">Coming soon: everything new since you last looked, in one place.</p>

<PendingApprovals showEmpty />

<section aria-labelledby="recent-h" data-testid="new-recent">
  <h2 id="recent-h">Latest takes</h2>
  {#if error}<p role="alert">{error}</p>{/if}
  <ul aria-busy={loading}>
    {#each takes as t (t.id)}
      <li data-testid="new-take">
        <button type="button" class="play" aria-label="Play {t.title}" onclick={() => play(t)}><PlayIcon size={20} /></button>
        <CoverArt trackId={t.id} title="" size={40} />
        <a class="body" href="/tracks/{t.id}">
          <span class="title">{t.title}</span>
          <span class="meta muted">{day(t.recorded_at)} · {fmt(t.duration_s)}</span>
        </a>
        <QueueButtons id={t.id} title={t.title} />
      </li>
    {:else}
      {#if !loading && !error}<li class="muted">No takes yet.</li>{/if}
    {/each}
  </ul>
</section>

<style>
  ul {
    list-style: none;
    margin: 0;
    padding: 0;
  }
  li {
    display: flex;
    align-items: center;
    gap: var(--sp-3);
    min-height: 56px;
    border-bottom: 1px solid var(--line);
  }
  .play {
    display: inline-flex;
    align-items: center;
    justify-content: center;
    min-width: var(--hit);
    min-height: var(--hit);
    padding: 0;
  }
  .body {
    display: grid;
    min-width: 0;
    flex: 1;
    color: var(--text);
    text-decoration: none;
  }
  .title,
  .meta {
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
  }
  .meta {
    font-size: var(--fs-sm);
  }
</style>
