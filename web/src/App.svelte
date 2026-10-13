<script lang="ts">
  import { untrack } from "svelte";
  import { APP_ROUTES, createRouter } from "./lib/router";
  import { randomId } from "./lib/id";
  import { player } from "./lib/player.svelte";
  import { dismissToast, toasts } from "./lib/judge.svelte";
  import { installDevice } from "./lib/device.svelte";
  import { installMediaSession } from "./lib/mediaSession";
  import { installListen } from "./lib/listen.svelte";
  import { createNav } from "./lib/nav";
  import { api } from "./api/client";
  import { loadDuelBadge } from "./lib/navBadge";
  import PlayerBar from "./lib/components/PlayerBar.svelte";
  import SessionBanner from "./lib/components/SessionBanner.svelte";
  import Library from "./routes/Library.svelte";
  import Track from "./routes/Track.svelte";
  import Backlog from "./routes/Backlog.svelte";
  import Draft from "./routes/Draft.svelte";
  import Import from "./routes/Import.svelte";
  import Settings from "./routes/Settings.svelte";
  import DraftNew from "./routes/DraftNew.svelte";
  import DraftUpload from "./routes/DraftUpload.svelte";
  import Playlists from "./routes/Playlists.svelte";
  import Playlist from "./routes/Playlist.svelte";
  import Export from "./routes/Export.svelte";
  import SettingsBackups from "./routes/SettingsBackups.svelte";
  import SettingsAudit from "./routes/SettingsAudit.svelte";
  import HumanTokens from "./routes/HumanTokens.svelte";
  import HumanDevices from "./routes/HumanDevices.svelte";
  import Duels from "./routes/Duels.svelte";
  import Duel from "./routes/Duel.svelte";
  import Ladder from "./routes/Ladder.svelte";
  import Compare from "./routes/Compare.svelte";
  import NotFound from "./routes/NotFound.svelte";

  const router = createRouter(APP_ROUTES);
  const audioId = randomId();
  // /human/* is always a full-page load so the server's second-factor / step-up check runs first.
  const nav = createNav({ assign: (u) => location.assign(u), route: (u) => router.navigate(u) });
  const humanClick = (e: MouseEvent) => {
    const href = (e.currentTarget as HTMLAnchorElement).getAttribute("href") ?? "";
    nav.onLinkClick(e, href);
  };
  // The router's query map collapses repeated keys; the library needs the raw string.
  // The argument only makes the call re-run on every navigation.
  function searchOf(_loc: unknown): string {
    void _loc;
    return window.location.search;
  }

  // Open-duel count for the nav badge: list_duels(state=open, limit=1).total, refreshed on navigation.
  let duelBadge = $state<number | null>(null);
  $effect(() => {
    void $router.path;
    void loadDuelBadge({ GET: (p, i) => api.GET(p, i) }).then((n) => (duelBadge = n));
  });

  let audio: HTMLAudioElement;
  // Install once: untracked, because attach() writes player.el and the installers read player state;
  // tracked, the effect would re-run itself forever (one device-probe request per iteration).
  $effect(() =>
    untrack(() => {
      player.attach(audio);
      installDevice(player);
      const offMediaSession = installMediaSession(player, audio);
      const offListen = installListen(player);
      return () => {
        offListen();
        offMediaSession();
        player.attach(null);
        router.destroy();
      };
    }),
  );
</script>

<div class="shell">
  <header>
    <strong>metamusic</strong>
    <nav aria-label="Main">
      <a href="/" aria-current={$router.route?.name === "home" ? "page" : undefined}>Library</a>
      <a href="/backlog" aria-current={$router.route?.name === "backlog" || $router.route?.name === "draft" ? "page" : undefined}>Backlog</a>
      <a href="/duels" aria-current={$router.route?.name === "duels" || $router.route?.name === "duel" ? "page" : undefined}>Duels{#if duelBadge}<span class="badge" data-testid="duels-badge" aria-label="{duelBadge} open">{duelBadge}</span>{/if}</a>
      <a href="/ladder" aria-current={$router.route?.name === "ladder" ? "page" : undefined}>Ladder</a>
      <a href="/playlists" aria-current={$router.route?.name === "playlists" || $router.route?.name === "playlist" ? "page" : undefined}>Playlists</a>
      <a href="/export" aria-current={$router.route?.name === "export" ? "page" : undefined}>Export</a>
      <a href="/import" aria-current={$router.route?.name === "import" ? "page" : undefined}>Import</a>
      <a href="/settings" aria-current={$router.route?.name === "settings" ? "page" : undefined}>Settings</a>
      <a href="/human/tokens" onclick={humanClick} aria-current={$router.route?.name === "human-tokens" ? "page" : undefined}>Tokens</a>
      <a href="/human/devices" onclick={humanClick} aria-current={$router.route?.name === "human-devices" ? "page" : undefined}>Devices</a>
    </nav>
  </header>

  <main>
    <SessionBanner />
    {#if $router.route?.name === "home"}
      <Library search={searchOf($router)} navigate={router.navigate} />
    {:else if $router.route?.name === "track"}
      {#key $router.params.id}
        <Track id={$router.params.id ?? ""} t={$router.query.t ?? ""} navigate={router.navigate} />
      {/key}
    {:else if $router.route?.name === "backlog"}
      <Backlog search={searchOf($router)} navigate={router.navigate} />
    {:else if $router.route?.name === "draft-new"}
      <DraftNew />
    {:else if $router.route?.name === "draft-upload"}
      <DraftUpload />
    {:else if $router.route?.name === "draft"}
      {#key $router.params.id}
        <Draft id={$router.params.id ?? ""} />
      {/key}
    {:else if $router.route?.name === "duels"}
      <Duels navigate={router.navigate} />
    {:else if $router.route?.name === "duel"}
      {#key $router.params.id}
        <Duel id={$router.params.id ?? ""} />
      {/key}
    {:else if $router.route?.name === "ladder"}
      <Ladder root={$router.query.root ?? ""} />
    {:else if $router.route?.name === "compare"}
      <Compare a={$router.query.a ?? ""} b={$router.query.b ?? ""} />
    {:else if $router.route?.name === "playlists"}
      <Playlists />
    {:else if $router.route?.name === "playlist"}
      {#key $router.params.id}
        <Playlist id={$router.params.id ?? ""} />
      {/key}
    {:else if $router.route?.name === "export"}
      <Export />
    {:else if $router.route?.name === "settings-backups"}
      <SettingsBackups />
    {:else if $router.route?.name === "settings-audit"}
      <SettingsAudit />
    {:else if $router.route?.name === "human-tokens"}
      <HumanTokens />
    {:else if $router.route?.name === "human-devices"}
      <HumanDevices />
    {:else if $router.route?.name === "import"}
      <Import />
    {:else if $router.route?.name === "settings"}
      <Settings />
    {:else}
      <NotFound />
    {/if}
  </main>

  <div class="toasts" role="status" aria-live="polite">
    {#each toasts as t (t.id)}
      <div class="toast" data-testid="toast">
        {t.message}
        <button type="button" aria-label="Dismiss" onclick={() => dismissToast(t.id)}>×</button>
      </div>
    {/each}
  </div>

  <footer class="player" aria-label="Player">
    <PlayerBar {player} />
    <audio bind:this={audio} data-audio-id={audioId} preload="none"></audio>
  </footer>
</div>

<style>
  :global(body) {
    margin: 0;
    font-family: system-ui, sans-serif;
    background: #14110f;
    color: #f4ece4;
  }
  :global(a) {
    color: #f2a65a;
  }
  :global(.muted) {
    opacity: 0.6;
  }
  .shell {
    min-height: 100dvh;
    display: grid;
    grid-template-columns: minmax(0, 1fr);
    grid-template-rows: auto 1fr auto;
  }
  header {
    display: flex;
    flex-wrap: wrap;
    gap: 0.5rem 1.5rem;
    align-items: center;
    padding: 0.75rem 1rem;
    border-bottom: 1px solid #3a322b;
  }
  nav {
    display: flex;
    flex-wrap: wrap;
    gap: 0.25rem 1rem;
  }
  nav a[aria-current="page"] {
    font-weight: 700;
    text-decoration: none;
  }
  .badge {
    margin-left: 0.3rem;
    padding: 0 0.4rem;
    border-radius: 0.6rem;
    background: #f2a65a;
    color: #14110f;
    font-size: 0.75rem;
  }
  main {
    min-width: 0;
    padding: 1rem;
  }
  .player {
    display: flex;
    gap: 1rem;
    align-items: center;
    padding: 0.5rem 1rem calc(0.5rem + env(safe-area-inset-bottom));
    border-top: 1px solid #3a322b;
    position: sticky;
    bottom: 0;
    background: #1d1814;
  }
  .toasts {
    position: fixed;
    right: 1rem;
    bottom: 7rem;
    display: grid;
    gap: 0.4rem;
    z-index: 10;
  }
  .toast {
    background: #5a2a22;
    border: 1px solid #a02a1f;
    border-radius: 0.4rem;
    padding: 0.4rem 0.7rem;
  }
  .toast button {
    background: none;
    border: none;
    color: inherit;
    cursor: pointer;
  }
  .player audio {
    display: none;
  }
</style>
