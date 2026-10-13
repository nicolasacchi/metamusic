<script lang="ts">
  import { api } from "../api/client";
  import type { components } from "../api/schema";
  import { player } from "../lib/player.svelte";
  import { pushToast } from "../lib/judge.svelte";
  import { createPlaylistZip, fetchExport, moveItem, savePlaylist } from "../lib/exportView";
  import PlaylistItems from "../lib/components/PlaylistItems.svelte";

  type Playlist = components["schemas"]["Playlist"];
  type Export = components["schemas"]["Export"];
  let { id }: { id: string } = $props();

  let pl = $state<Playlist | null>(null);
  let error = $state("");
  let busy = $state(false);
  let name = $state("");
  let zip = $state<Export | null>(null);
  let zipBusy = $state(false);
  let zipError = $state("");

  async function load() {
    const mine = id;
    try {
      const r = await api.GET("/api/playlists/{id}", { params: { path: { id: mine } } });
      if (mine !== id) return;
      if (r.data) {
        pl = r.data;
        name = r.data.name;
      } else error = r.response.status === 404 ? "Playlist not found" : (r.error?.message ?? "Failed to load playlist");
    } catch {
      if (mine === id) error = "Failed to load playlist";
    }
  }
  $effect(() => {
    void load();
  });

  async function save(patch: Parameters<typeof savePlaylist>[2]) {
    if (!pl || busy) return;
    busy = true;
    const r = await savePlaylist(api, pl, patch);
    busy = false;
    if (r.ok) {
      pl = r.playlist;
      name = r.playlist.name;
    } else {
      pushToast(r.conflict ? "The playlist changed elsewhere — reloaded" : r.message);
      if (r.conflict) await load();
    }
  }
  const ids = $derived(pl?.items.map((i) => i.track.id) ?? []);
  function move(i: number, d: number) {
    void save({ trackIds: moveItem(ids, i, d) });
  }
  function remove(i: number) {
    void save({ trackIds: ids.filter((_, k) => k !== i) });
  }
  function playFrom(trackId: string) {
    const t = pl?.items.find((i) => i.track.id === trackId);
    player.play(trackId, ids, { title: t?.track.title ?? "" });
  }
  function playAll() {
    if (ids[0]) playFrom(ids[0]);
  }

  async function makeZip() {
    if (!pl || zipBusy) return;
    zipBusy = true;
    zipError = "";
    const r = await createPlaylistZip(api, pl.id);
    zipBusy = false;
    if (r.ok) zip = r.export;
    else zipError = r.message;
  }
  // A signed link close to its expiry is replaced by a fresh one (get_export).
  async function refreshZip() {
    if (!zip) return;
    const r = await fetchExport(api, zip.id);
    if (r.ok) zip = r.export;
    else zipError = r.message;
  }
  $effect(() => {
    if (!zip) return;
    const wait = Date.parse(zip.download.expires_at) - Date.now() - 60_000;
    const h = setTimeout(() => void refreshZip(), Math.min(Math.max(wait, 5000), 2_000_000_000));
    return () => clearTimeout(h);
  });
</script>

{#if error}
  <p role="alert">{error}</p>
  <p><a href="/playlists">Back to playlists</a></p>
{:else if !pl}
  <p class="muted">Loading…</p>
{:else}
  <h1 data-testid="playlist-title">{pl.name}</h1>
  {#if pl.description}<p>{pl.description}</p>{/if}
  <form
    class="rename"
    onsubmit={(e) => {
      e.preventDefault();
      if (name.trim() && name.trim() !== pl?.name) void save({ name: name.trim() });
    }}
  >
    <input bind:value={name} aria-label="Playlist name" maxlength="120" />
    <button type="submit" disabled={busy || !name.trim() || name.trim() === pl.name}>Rename</button>
    <button type="button" disabled={busy} onclick={() => save({ hidden: !pl?.hidden })}>{pl.hidden ? "Unhide" : "Hide"}</button>
  </form>

  <PlaylistItems items={pl.items} {busy} playingId={player.state.trackId} onmove={move} onremove={remove} onplay={playFrom} onplayall={playAll} />

  <section class="zip">
    <h2>Export</h2>
    <button type="button" onclick={makeZip} disabled={zipBusy || pl.items.length === 0} data-testid="playlist-zip">{zipBusy ? "Building…" : "Export MP3 ZIP"}</button>
    {#if zipError}<span role="alert">{zipError}</span>{/if}
    {#if zip}
      <p data-testid="playlist-zip-result">
        <a href={zip.download.url} download={zip.filename} data-testid="zip-download">Download {zip.filename}</a>
        <span class="muted">{zip.zip ? `${zip.zip.entries} tracks · ` : ""}{zip.bytes} bytes · sha256 {zip.sha256.slice(0, 12)}…</span>
      </p>
      {#if zip.zip && zip.zip.missing_mp3.length > 0}
        <p role="status">No MP3 yet for: {zip.zip.missing_mp3.join(", ")}</p>
      {/if}
    {/if}
  </section>
{/if}

<style>
  .rename {
    display: flex;
    gap: 0.5rem;
    flex-wrap: wrap;
    margin: 0.5rem 0;
  }
  .zip {
    margin-top: 1.5rem;
  }
</style>
