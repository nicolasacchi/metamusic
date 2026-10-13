<script lang="ts">
  import { api } from "../api/client";
  import type { components } from "../api/schema";
  import { fmt } from "../lib/time";
  import { createPlaylist } from "../lib/exportView";
  import { pushToast } from "../lib/judge.svelte";

  type Summary = components["schemas"]["PlaylistSummary"];
  let lists = $state<Summary[]>([]);
  let showHidden = $state(false);
  let loading = $state(true);
  let error = $state("");
  let name = $state("");
  let busy = $state(false);

  async function load(hidden: boolean) {
    loading = true;
    error = "";
    try {
      const r = await api.GET("/api/playlists", { params: { query: { include_hidden: hidden } } });
      if (hidden !== showHidden) return;
      if (r.data) lists = r.data.items;
      else error = r.error?.message ?? "Failed to load playlists";
    } catch {
      error = "Failed to load playlists";
    } finally {
      loading = false;
    }
  }
  $effect(() => {
    void load(showHidden);
  });

  async function create() {
    const n = name.trim();
    if (!n || busy) return;
    busy = true;
    const r = await createPlaylist(api, n);
    busy = false;
    if (r.ok) {
      name = "";
      await load(showHidden);
    } else pushToast(r.message);
  }
</script>

<h1>Playlists</h1>
{#if error}<p role="alert">{error}</p>{/if}

<form
  onsubmit={(e) => {
    e.preventDefault();
    void create();
  }}
>
  <input bind:value={name} placeholder="New playlist name" aria-label="Playlist name" maxlength="120" data-testid="playlist-name" />
  <button type="submit" disabled={busy || !name.trim()} data-testid="playlist-create">Create</button>
  <label><input type="checkbox" bind:checked={showHidden} /> Show hidden</label>
</form>

{#if loading && lists.length === 0}
  <p class="muted">Loading…</p>
{:else if lists.length === 0}
  <p class="muted">No playlists yet.</p>
{:else}
  <ul data-testid="playlist-list">
    {#each lists as l (l.id)}
      <li data-id={l.id}>
        <a href="/playlists/{l.id}">{l.name}</a>
        <span class="muted">{l.items_count} {l.items_count === 1 ? "track" : "tracks"} · {fmt(l.duration_s)}{l.hidden ? " · hidden" : ""}</span>
      </li>
    {/each}
  </ul>
{/if}

<style>
  form {
    display: flex;
    gap: 0.6rem;
    align-items: center;
    flex-wrap: wrap;
    margin: 0.5rem 0;
  }
  li {
    margin: 0.3rem 0;
  }
</style>
