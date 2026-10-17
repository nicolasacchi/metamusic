<script lang="ts">
  // The play queue: now playing, then the upcoming takes with play-from-here, up/down, remove and "Clear upcoming".
  // Shared by the /queue tab and the queue view inside the now-playing sheet. Every change goes through the player
  // store, which announces it (live region + toast).
  import { player } from "../player.svelte";
  import { loadTitle } from "../trackTitles";
  import { fmt } from "../time";
  import CoverArt from "./CoverArt.svelte";
  import PlayIcon from "./icons/PlayIcon.svelte";
  import PauseIcon from "./icons/PauseIcon.svelte";
  import UpIcon from "./icons/UpIcon.svelte";
  import DownIcon from "./icons/DownIcon.svelte";
  import CloseIcon from "./icons/CloseIcon.svelte";

  const SHOWN = 50;
  const uid = $props.id();

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
  const at = (i: number) => s.index + 1 + i;
  const name = (id: string) => titles[id] ?? "this take";
</script>

{#if !s.trackId}
  <p class="muted" data-testid="queue-empty">Nothing is queued. Play a take from the Library to start.</p>
{:else}
  <section aria-labelledby="{uid}-np">
    <h2 id="{uid}-np">Now playing</h2>
    {#if s.source}<p class="muted from" data-testid="queue-source">Playing from <a href={s.source.href}>{s.source.label}</a></p>{/if}
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

  <section aria-labelledby="{uid}-up">
    <div class="uphead">
      <h2 id="{uid}-up">Up next{#if upcoming.length > 0}<span class="muted count">({upcoming.length})</span>{/if}</h2>
      {#if upcoming.length > 0}
        <button type="button" data-testid="queue-clear" onclick={() => player.clearUpcoming()}>Clear upcoming</button>
      {/if}
    </div>
    {#if upcoming.length === 0}
      <p class="muted" data-testid="queue-end">{s.endOfQueue ? "End of queue." : "Nothing after this take."}</p>
    {:else}
      <ol data-testid="queue-list">
        {#each shown as id, i (id)}
          <li class="row" data-testid="queue-item" data-track-id={id}>
            <CoverArt trackId={id} title="" size={40} />
            <a class="body" href="/tracks/{id}"><span class="title">{titles[id] ?? "Loading…"}</span></a>
            <button type="button" class="ico" aria-label="Play {name(id)} from here" onclick={() => playFrom(id)}><PlayIcon size={20} /></button>
            <button type="button" class="ico" aria-label="Move {name(id)} up" data-testid="queue-up" disabled={i === 0} onclick={() => player.move(at(i), at(i - 1))}><UpIcon size={20} /></button>
            <button type="button" class="ico" aria-label="Move {name(id)} down" data-testid="queue-down" disabled={at(i) >= s.queue.length - 1} onclick={() => player.move(at(i), at(i + 1))}><DownIcon size={20} /></button>
            <button type="button" class="ico" aria-label="Remove {name(id)} from the queue" data-testid="queue-remove" onclick={() => player.remove(at(i))}><CloseIcon size={20} /></button>
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
    gap: var(--sp-2);
    min-height: 56px;
    border-bottom: 1px solid var(--line);
  }
  .now {
    border: 1px solid var(--accent);
    border-radius: var(--r-md);
    padding: var(--sp-2) var(--sp-3);
    background: var(--accent-soft);
  }
  .uphead {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: var(--sp-3);
  }
  .uphead button {
    min-height: var(--hit);
  }
  .count {
    margin-left: 0.4em;
  }
  .from {
    margin: 0 0 var(--sp-2);
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
    flex: none;
    min-width: var(--hit);
    min-height: var(--hit);
    padding: 0;
  }
  @media (max-width: 767px) {
    li .ico {
      min-width: 40px;
    }
  }
</style>
