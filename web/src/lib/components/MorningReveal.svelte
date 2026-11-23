<script lang="ts">
  // After the reveal: each chapter with its title, your blind stars, the verdict the headset and the stars made, the
  // markers, and "Duel these two" (create_duel, blind) for any pair (M6N-W2, C384.3).
  import { api } from "../../api/client";
  import { newIdempotencyKey } from "../../api/idem";
  import { createDuel } from "../duelApi";
  import { pushToast } from "../judge.svelte";
  import { VERDICT_TEXT, duelPair, type MorningCrate } from "../morningCrate";
  import { fmt } from "../time";

  interface Props {
    crate: MorningCrate;
    navigate: (to: string) => void;
  }
  let { crate, navigate }: Props = $props();

  let picked = $state<number[]>([]);
  let busy = $state(false);
  const pair = $derived(duelPair(crate, picked));

  function toggle(n: number) {
    picked = picked.includes(n) ? picked.filter((x) => x !== n) : [...picked, n].slice(-2);
  }

  async function duel() {
    if (!pair || busy) return;
    busy = true;
    try {
      const d = await createDuel(api, pair[0], pair[1], newIdempotencyKey(), { blind: true });
      navigate(`/duels/${encodeURIComponent(d.id)}`);
    } catch (e) {
      pushToast(e instanceof Error ? e.message : "Could not create the duel");
    } finally {
      busy = false;
    }
  }
</script>

<div data-testid="morning-reveal">
  <p>
    Revealed{#if crate.revealed_at} {new Date(crate.revealed_at).toLocaleString()}{/if}.
    {#if crate.playlist_id}<a href="/playlists/{crate.playlist_id}" data-testid="morning-playlist">The playlist</a> is in your library and in Subsonic.{/if}
  </p>
  <ol class="rows">
    {#each crate.chapters as c (c.n)}
      <li data-testid="morning-reveal-row" data-verdict={c.verdict} data-anchor={c.anchor ? "true" : "false"}>
        <div class="head">
          <span class="n">{c.n}.</span>
          {#if c.track}<a href="/tracks/{c.track.id}" class="title">{c.track.title}</a>{:else}<span class="muted">Not available</span>{/if}
          {#if c.anchor}<span class="chip" title="A take you rated before, mixed in blind to check your ear">anchor</span>{/if}
          <span class="chip {c.verdict}">{VERDICT_TEXT[c.verdict]}</span>
        </div>
        <p class="muted">
          {#if c.stars !== null}You rated it {"★".repeat(c.stars)}{"☆".repeat(5 - c.stars)}.{:else}Not rated.{/if}
          {#if c.markers > 0}{c.markers} {c.markers === 1 ? "marker" : "markers"}{#if c.strong_markers > 0}, {c.strong_markers} kept{/if}.{/if}
          {#if c.skipped_at_ms !== null}Skipped at {fmt(c.skipped_at_ms / 1000)}.{/if}
        </p>
        {#if c.track && !c.anchor}
          <label class="pick"><input type="checkbox" checked={picked.includes(c.n)} onchange={() => toggle(c.n)} data-testid="morning-pick" /> Pick for a duel</label>
        {/if}
      </li>
    {/each}
  </ol>
  <p>
    <button type="button" class="btn" disabled={!pair || busy} onclick={() => void duel()} data-testid="morning-duel">Duel these two</button>
    {#if !pair}<span class="muted">Pick two takes.</span>{/if}
  </p>
</div>

<style>
  .rows {
    list-style: none;
    margin: 0 0 var(--sp-3);
    padding: 0;
    display: grid;
    gap: var(--sp-2);
  }
  li {
    padding: var(--sp-2) var(--sp-3);
    background: var(--surface);
    border: 1px solid var(--line);
    border-radius: var(--r-md);
  }
  .head {
    display: flex;
    gap: var(--sp-2);
    align-items: center;
    flex-wrap: wrap;
  }
  .title {
    font-weight: var(--fw-medium);
  }
  p {
    margin: var(--sp-1) 0;
  }
  .chip {
    padding: 0 var(--sp-2);
    border-radius: var(--r-pill);
    font-size: var(--fs-xs);
    background: var(--surface-2);
    color: var(--text-2);
  }
  .chip.kept {
    background: color-mix(in srgb, var(--ok) 25%, transparent);
    color: var(--ok);
  }
  .chip.skipped {
    background: var(--bad-soft);
    color: var(--bad);
  }
  .pick {
    display: flex;
    gap: var(--sp-2);
    align-items: center;
    min-height: var(--hit);
  }
  .btn {
    min-height: var(--hit);
    padding: 0 var(--sp-4);
    border: 1px solid var(--accent);
    border-radius: var(--r-pill);
    background: transparent;
    color: var(--accent);
  }
</style>
