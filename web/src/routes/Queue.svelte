<script lang="ts">
  import { player } from "../lib/player.svelte";
  import { loadTitle } from "../lib/trackTitles";
  import { fmt } from "../lib/time";
  import CoverArt from "../lib/components/CoverArt.svelte";
  import PlayIcon from "../lib/components/icons/PlayIcon.svelte";
  import PauseIcon from "../lib/components/icons/PauseIcon.svelte";

  const SHOWN = 50;

  const s = $derived(player.state);
  const upcoming = $derived(s.index >= 0 ? s.queue.slice(s.index + 1) : []);
  const shown = $derived(upcoming.slice(0, SHOWN));
  let titles = $state<Record<string, string>>({});

  $effect(() => {
    for (const id of shown) {
      if (titles[id] !== undefined) continue;
      void loadTitle(id).then((t) => {
        if (t !== null) titles = { ...titles, [id]: t };
      });
    }
  });

  // Play from here keeps the queue and its source: only the position moves.
  const playFrom = (id: string) => player.play(id);
</script>

<h1>Play queue</h1>

{#if !s.trackId}
  <p class="muted" data-testid="queue-empty">Nothing is queued. Play a take from the Library to start.</p>
{:else}
  <section aria-labelledby="np-h">
    <h2 id="np-h">Now playing</h2>
    <div class="row now" data-testid="queue-now">
      <CoverArt trackId={s.trackId} title="" size={48} />
      <a class="body" href="/tracks/{s.trackId}">
        <span class="title">{player.title}</span>
        <span class="muted sub">{fmt(s.current)} / {fmt(s.duration)}</span>
      </a>
      <button type="button" class="ico" aria-label={s.playing ? "Pause" : "Play"} onclick={() => player.toggle()}>
        {#if s.playing}<PauseIcon size={20} />{:else}<PlayIcon size={20} />{/if}
      </button>
    </div>
  </section>

  <section aria-labelledby="up-h">
    <h2 id="up-h">Up next{#if upcoming.length > 0} <span class="muted">({upcoming.length})</span>{/if}</h2>
    {#if upcoming.length === 0}
      <p class="muted" data-testid="queue-end">{s.endOfQueue ? "End of queue." : "Nothing after this take."}</p>
    {:else}
      <ol data-testid="queue-list">
        {#each shown as id (id)}
          <li class="row" data-testid="queue-item">
            <CoverArt trackId={id} title="" size={40} />
            <a class="body" href="/tracks/{id}"><span class="title">{titles[id] ?? "Loading…"}</span></a>
            <button type="button" class="ico" aria-label="Play {titles[id] ?? 'this take'} from here" onclick={() => playFrom(id)}><PlayIcon size={20} /></button>
          </li>
        {/each}
      </ol>
      {#if upcoming.length > shown.length}<p class="muted">and {upcoming.length - shown.length} more</p>{/if}
    {/if}
  </section>
{/if}

<style>
  ol {
    list-style: none;
    margin: 0;
    padding: 0;
  }
  .row {
    display: flex;
    align-items: center;
    gap: var(--sp-3);
    min-height: 56px;
    border-bottom: 1px solid var(--line);
  }
  .now {
    border: 1px solid var(--accent);
    border-radius: var(--r-md);
    padding: var(--sp-2) var(--sp-3);
    background: var(--accent-soft);
  }
  .body {
    display: grid;
    flex: 1;
    min-width: 0;
    color: var(--text);
    text-decoration: none;
  }
  .title,
  .sub {
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
  }
  .ico {
    display: inline-flex;
    align-items: center;
    justify-content: center;
    min-width: var(--hit);
    min-height: var(--hit);
    padding: 0;
  }
</style>
