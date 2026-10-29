<script lang="ts">
  import { api } from "../../api/client";
  import { searchSongs } from "../curationApi";
  import type { components } from "../../api/schema";

  type Song = components["schemas"]["SongSummary"];
  interface Props {
    /** Song ids already in the Record (not offered). */
    existing: readonly string[];
    disabled?: boolean;
    onadd: (songId: string) => void;
    /** Test seam: replaces the network search. */
    search?: (q: string) => Promise<Song[]>;
  }
  let { existing, disabled = false, onadd, search }: Props = $props();
  let q = $state("");
  let results = $state<Song[]>([]);
  let seq = 0;

  $effect(() => {
    const text = q.trim();
    const mine = ++seq;
    if (text.length < 1) {
      results = [];
      return;
    }
    const h = setTimeout(async () => {
      const found = await (search ? search(text) : searchSongs(api, text));
      if (mine === seq) results = found;
    }, 250);
    return () => clearTimeout(h);
  });
  const offered = $derived(results.filter((s) => !existing.includes(s.id)));
</script>

<div class="picker" data-testid="song-picker">
  <label for="song-picker-q">Add a song</label>
  <input id="song-picker-q" type="search" bind:value={q} placeholder="Search songs" autocomplete="off" {disabled} />
  {#if offered.length > 0}
    <ul>
      {#each offered as s (s.id)}
        <li>
          <span class="t">{s.title}</span>
          <button type="button" {disabled} onclick={() => onadd(s.id)} aria-label="Add {s.title}" data-testid="song-picker-add">Add</button>
        </li>
      {/each}
    </ul>
  {:else if q.trim() && results.length > 0}
    <p class="muted">Those songs are already in this record.</p>
  {/if}
</div>

<style>
  .picker {
    display: grid;
    gap: var(--sp-2);
  }
  input {
    width: 100%;
    box-sizing: border-box;
  }
  ul {
    list-style: none;
    margin: 0;
    padding: 0;
  }
  li {
    display: flex;
    align-items: center;
    gap: var(--sp-2);
    min-height: var(--hit);
    border-bottom: 1px solid var(--line);
  }
  .t {
    flex: 1;
    min-width: 0;
    overflow-wrap: anywhere;
  }
</style>
