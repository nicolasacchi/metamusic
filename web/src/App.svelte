<script lang="ts">
  import { createRouter, type Route } from "./lib/router";
  import { randomId } from "./lib/id";
  import { player } from "./lib/player.svelte";
  import { installDevice } from "./lib/device.svelte";
  import { installMediaSession } from "./lib/mediaSession";
  import PlayerBar from "./lib/components/PlayerBar.svelte";
  import SessionBanner from "./lib/components/SessionBanner.svelte";
  import Library from "./routes/Library.svelte";
  import Track from "./routes/Track.svelte";
  import Backlog from "./routes/Backlog.svelte";
  import Draft from "./routes/Draft.svelte";
  import Import from "./routes/Import.svelte";
  import Settings from "./routes/Settings.svelte";
  import NotFound from "./routes/NotFound.svelte";

  const routes: Route[] = [
    { pattern: "/", name: "home" },
    { pattern: "/tracks/:id", name: "track" },
    { pattern: "/backlog", name: "backlog" },
    { pattern: "/backlog/:id", name: "draft" },
    { pattern: "/import", name: "import" },
    { pattern: "/settings", name: "settings" },
  ];
  const router = createRouter(routes);
  const audioId = randomId();
  // The router's query map collapses repeated keys; the library needs the raw string.
  // The argument only makes the call re-run on every navigation.
  function searchOf(_loc: unknown): string {
    void _loc;
    return window.location.search;
  }

  let audio: HTMLAudioElement;
  $effect(() => {
    player.attach(audio);
    installDevice(player);
    const offMediaSession = installMediaSession(player, audio);
    return () => {
      offMediaSession();
      player.attach(null);
      router.destroy();
    };
  });
</script>

<div class="shell">
  <header>
    <strong>metamusic</strong>
    <nav aria-label="Main">
      <a href="/" aria-current={$router.route?.name === "home" ? "page" : undefined}>Library</a>
      <a href="/backlog" aria-current={$router.route?.name === "backlog" || $router.route?.name === "draft" ? "page" : undefined}>Backlog</a>
      <a href="/import" aria-current={$router.route?.name === "import" ? "page" : undefined}>Import</a>
      <a href="/settings" aria-current={$router.route?.name === "settings" ? "page" : undefined}>Settings</a>
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
    {:else if $router.route?.name === "draft"}
      {#key $router.params.id}
        <Draft id={$router.params.id ?? ""} />
      {/key}
    {:else if $router.route?.name === "import"}
      <Import />
    {:else if $router.route?.name === "settings"}
      <Settings />
    {:else}
      <NotFound />
    {/if}
  </main>

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
  .player audio {
    display: none;
  }
</style>
