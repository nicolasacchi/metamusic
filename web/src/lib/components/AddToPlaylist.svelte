<script lang="ts">
  import { api } from "../../api/client";
  import type { components } from "../../api/schema";
  import { addToPlaylist, createPlaylist } from "../exportView";
  import { pushToast } from "../judge.svelte";

  type Summary = components["schemas"]["PlaylistSummary"];
  interface Props {
    trackId: string;
    /** Playlists the track is already in (from the track detail). */
    member?: readonly { id: string; name: string }[];
  }
  let { trackId, member = [] }: Props = $props();

  let lists = $state<Summary[]>([]);
  let loaded = $state(false);
  let chosen = $state("");
  let newName = $state("");
  let busy = $state(false);
  let added = $state<{ id: string; name: string }[]>([]);
  const inLists = $derived([...member, ...added.filter((a) => !member.some((m) => m.id === a.id))]);

  async function load() {
    if (loaded) return;
    loaded = true;
    try {
      const r = await api.GET("/api/playlists", {});
      if (r.data) {
        lists = r.data.items;
        if (!chosen && lists[0]) chosen = lists[0].id;
      }
    } catch {
      loaded = false;
    }
  }
  $effect(() => {
    void load();
  });

  async function add() {
    const pl = lists.find((l) => l.id === chosen);
    if (!pl || busy) return;
    busy = true;
    const r = await addToPlaylist(api, pl, trackId);
    busy = false;
    if (r.ok) {
      added = [...added, { id: pl.id, name: pl.name }];
      pl.revision = r.playlist.revision;
    } else pushToast(r.message);
  }
  async function create() {
    const name = newName.trim();
    if (!name || busy) return;
    busy = true;
    const r = await createPlaylist(api, name, [trackId]);
    busy = false;
    if (r.ok) {
      lists = [...lists, { id: r.playlist.id, name: r.playlist.name, items_count: 1, duration_s: 0, hidden: false, revision: r.playlist.revision, updated_at: r.playlist.updated_at }];
      added = [...added, { id: r.playlist.id, name: r.playlist.name }];
      chosen = r.playlist.id;
      newName = "";
    } else pushToast(r.message);
  }
</script>

<section class="add" data-testid="add-to-playlist">
  <h2>Playlists</h2>
  {#if inLists.length > 0}
    <p data-testid="in-playlists">
      In:
      {#each inLists as p, i (p.id)}{i > 0 ? ", " : ""}<a href="/playlists/{p.id}">{p.name}</a>{/each}
    </p>
  {/if}
  {#if lists.length > 0}
    <label>
      <span class="sr">Playlist</span>
      <select bind:value={chosen} aria-label="Playlist" data-testid="playlist-select">
        {#each lists as l (l.id)}<option value={l.id}>{l.name}</option>{/each}
      </select>
    </label>
    <button type="button" onclick={add} disabled={busy || !chosen} data-testid="playlist-add">Add to playlist</button>
  {/if}
  <form
    onsubmit={(e) => {
      e.preventDefault();
      void create();
    }}
  >
    <input bind:value={newName} placeholder="New playlist name" aria-label="New playlist name" maxlength="120" data-testid="playlist-new-name" />
    <button type="submit" disabled={busy || !newName.trim()} data-testid="playlist-create-add">Create and add</button>
  </form>
</section>

<style>
  .add {
    display: grid;
    gap: 0.4rem;
  }
  h2 {
    margin: 0;
    font-size: 1rem;
  }
  .sr {
    position: absolute;
    left: -9999px;
  }
</style>
