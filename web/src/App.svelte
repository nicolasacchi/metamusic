<script lang="ts">
  import { createRouter, type Route } from "./lib/router";
  import { randomId } from "./lib/id";
  import { player } from "./lib/player.svelte";
  import { installMediaSession } from "./lib/mediaSession";
  import PlayerBar from "./lib/components/PlayerBar.svelte";
  import SessionBanner from "./lib/components/SessionBanner.svelte";
  import Home from "./routes/Home.svelte";
  import Settings from "./routes/Settings.svelte";
  import NotFound from "./routes/NotFound.svelte";

  const routes: Route[] = [
    { pattern: "/", name: "home" },
    { pattern: "/settings", name: "settings" },
  ];
  const router = createRouter(routes);
  const audioId = randomId();

  let audio: HTMLAudioElement;
  $effect(() => {
    player.attach(audio);
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
      <a href="/settings" aria-current={$router.route?.name === "settings" ? "page" : undefined}>Settings</a>
    </nav>
  </header>

  <main>
    <SessionBanner />
    {#if $router.route?.name === "home"}
      <Home />
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
    grid-template-rows: auto 1fr auto;
  }
  header {
    display: flex;
    gap: 1.5rem;
    align-items: center;
    padding: 0.75rem 1rem;
    border-bottom: 1px solid #3a322b;
  }
  nav {
    display: flex;
    gap: 1rem;
  }
  nav a[aria-current="page"] {
    font-weight: 700;
    text-decoration: none;
  }
  main {
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
