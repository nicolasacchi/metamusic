<script lang="ts">
  import { untrack } from "svelte";
  import { APP_ROUTES, createRouter } from "./lib/router";
  import { randomId } from "./lib/id";
  import { player } from "./lib/player.svelte";
  import { dismissToast, toasts } from "./lib/judge.svelte";
  import { installDevice } from "./lib/device.svelte";
  import { installMediaSession, mediaSessionEnv } from "./lib/mediaSession";
  import { covers } from "./lib/cover";
  import { installListen } from "./lib/listen.svelte";
  import { createNav } from "./lib/nav";
  import { api } from "./api/client";
  import { loadDuelBadge } from "./lib/navBadge";
  import { approvals, refreshApprovals } from "./lib/approvals.svelte";
  import { badgeText } from "./lib/approvals";
  import PlayerBar from "./lib/components/PlayerBar.svelte";
  import SessionBanner from "./lib/components/SessionBanner.svelte";
  import Library from "./routes/Library.svelte";
  import Track from "./routes/Track.svelte";
  import Backlog from "./routes/Backlog.svelte";
  import Board from "./routes/Board.svelte";
  import Song from "./routes/Song.svelte";
  import Muse from "./routes/Muse.svelte";
  import Sounds from "./routes/Sounds.svelte";
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
  import HumanConnectors from "./routes/HumanConnectors.svelte";
  import HumanSubsonic from "./routes/HumanSubsonic.svelte";
  import HumanDevices from "./routes/HumanDevices.svelte";
  import Duels from "./routes/Duels.svelte";
  import Duel from "./routes/Duel.svelte";
  import Ladder from "./routes/Ladder.svelte";
  import Compare from "./routes/Compare.svelte";
  import HumanGenerate from "./routes/HumanGenerate.svelte";
  import HumanConfirm from "./routes/HumanConfirm.svelte";
  import HumanSpend from "./routes/HumanSpend.svelte";
  import Human from "./routes/Human.svelte";
  import Jobs from "./routes/Jobs.svelte";
  import Job from "./routes/Job.svelte";
  import Lineage from "./routes/Lineage.svelte";
  import Batch from "./routes/Batch.svelte";
  import Offline from "./routes/Offline.svelte";
  import NotFound from "./routes/NotFound.svelte";
  import { offline } from "./lib/offline.svelte";
  import { applyUpdate, watchForUpdate } from "./api/swRegister";

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

  // Pending approvals: refreshed on navigation and every 30 s while the tab is visible. The badge is for humans only.
  const approvalBadge = $derived(approvals.viewer === "human" ? badgeText(approvals.items.length, approvals.more) : null);
  $effect(() => {
    void $router.path;
    void refreshApprovals();
  });
  $effect(() => {
    const t = setInterval(() => {
      if (!document.hidden) void refreshApprovals();
    }, 30_000);
    return () => clearInterval(t);
  });

  // The player bar is fixed to the viewport bottom; routed content reserves its (variable) height so that no control
  // (Approve, Reject, duel verdicts) can ever sit under it.
  let playerH = $state(0);
  // scroll-padding keeps scrollIntoView, focus and find-in-page from parking a control under the bar.
  $effect(() => {
    document.documentElement.style.setProperty("scroll-padding-bottom", `${playerH}px`);
  });
  // A new service worker is waiting: offer the update (C115.1); never forced.
  let waitingWorker = $state<ServiceWorkerRegistration | null>(null);
  // "Reload to update" never reloads under music: while the player is playing (or waiting for a tap) the click is
  // remembered and applied as soon as playback stops.
  let updateQueued = $state(false);
  const playbackBusy = $derived(player.state.playing || player.state.pendingPlay);
  function requestUpdate() {
    if (!waitingWorker) return;
    if (playbackBusy) updateQueued = true;
    else applyUpdate(waitingWorker);
  }
  $effect(() => {
    if (updateQueued && !playbackBusy && waitingWorker) {
      updateQueued = false;
      applyUpdate(waitingWorker);
    }
  });
  $effect(() =>
    untrack(() => {
      const onReg = (e: Event) => watchForUpdate((e as CustomEvent<ServiceWorkerRegistration>).detail, (r) => (waitingWorker = r));
      window.addEventListener("mm:sw-registered", onReg);
      // Offline badge: the set is fetched (never downloaded) so the nav can show downloaded/total.
      if (offline.available) {
        void offline.refresh();
        // The set is fetched only when the network is believed to be up (an offline start touches no API).
        if (navigator.onLine !== false) void offline.refreshSet();
      }
      return () => window.removeEventListener("mm:sw-registered", onReg);
    }),
  );

  let audio: HTMLAudioElement;
  // Install once: untracked, because attach() writes player.el and the installers read player state;
  // tracked, the effect would re-run itself forever (one device-probe request per iteration).
  $effect(() =>
    untrack(() => {
      player.attach(audio);
      installDevice(player);
      const offMediaSession = installMediaSession(player, audio, mediaSessionEnv(covers.get));
      const offListen = installListen(player);
      const offActivation = player.watchActivation(document);
      return () => {
        offActivation();
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
      <a href="/board" aria-current={["board", "muse", "song"].includes($router.route?.name ?? "") ? "page" : undefined}>Board</a>
      <a href="/sounds" aria-current={$router.route?.name === "sounds" ? "page" : undefined}>Sounds</a>
      <a href="/duels" aria-current={$router.route?.name === "duels" || $router.route?.name === "duel" ? "page" : undefined}>Duels{#if duelBadge}<span class="badge" data-testid="duels-badge" aria-label="{duelBadge} open">{duelBadge}</span>{/if}</a>
      <a href="/ladder" aria-current={$router.route?.name === "ladder" ? "page" : undefined}>Ladder</a>
      <a href="/playlists" aria-current={$router.route?.name === "playlists" || $router.route?.name === "playlist" ? "page" : undefined}>Playlists</a>
      <a href="/export" aria-current={$router.route?.name === "export" ? "page" : undefined}>Export</a>
      <a href="/import" aria-current={$router.route?.name === "import" ? "page" : undefined}>Import</a>
      <a href="/settings" aria-current={$router.route?.name === "settings" ? "page" : undefined}>Settings</a>
      {#if offline.available}
        <a href="/offline" aria-current={$router.route?.name === "offline" ? "page" : undefined} data-testid="nav-offline">Offline{#if offline.badge}<span class="badge" data-testid="offline-badge">{offline.badge}</span>{/if}</a>
      {/if}
      <a href="/jobs" aria-current={["jobs", "job", "batch"].includes($router.route?.name ?? "") ? "page" : undefined}>Jobs</a>
      <a href="/human/" onclick={humanClick} class="approvals-link" data-testid="approvals-nav" aria-current={$router.route?.name === "human" ? "page" : undefined} aria-label={approvalBadge ? `Approvals, ${approvalBadge} pending` : "Approvals"}>Approvals{#if approvalBadge}<span class="badge" data-testid="approvals-badge">{approvalBadge}</span>{/if}</a>
      <a href="/human/spend" onclick={humanClick} aria-current={$router.route?.name === "human-spend" ? "page" : undefined}>Spend</a>
      <a href="/human/tokens" onclick={humanClick} aria-current={$router.route?.name === "human-tokens" ? "page" : undefined}>Tokens</a>
      <a href="/human/devices" onclick={humanClick} aria-current={$router.route?.name === "human-devices" ? "page" : undefined}>Devices</a>
      <a href="/human/connectors" onclick={humanClick} aria-current={$router.route?.name === "human-connectors" ? "page" : undefined}>Connectors</a>
      <a href="/human/subsonic" onclick={humanClick} aria-current={$router.route?.name === "human-subsonic" ? "page" : undefined}>Subsonic</a>
    </nav>
  </header>

  <main style:padding-bottom="calc(1rem + {playerH}px)">
    <SessionBanner />
    {#if waitingWorker}
      <p class="update" role="status" data-testid="sw-update">
        A new version is available.
        {#if updateQueued}
          <span data-testid="sw-update-queued">The update applies when playback stops.</span>
        {:else}
          <button type="button" onclick={requestUpdate}>Reload to update</button>
        {/if}
      </p>
    {/if}
    {#if $router.route?.name === "home"}
      <Library search={searchOf($router)} navigate={router.navigate} />
    {:else if $router.route?.name === "track"}
      {#key $router.params.id}
        <Track id={$router.params.id ?? ""} t={$router.query.t ?? ""} navigate={router.navigate} />
      {/key}
    {:else if $router.route?.name === "backlog"}
      <Backlog search={searchOf($router)} navigate={router.navigate} />
    {:else if $router.route?.name === "board"}
      <Board search={searchOf($router)} navigate={router.navigate} />
    {:else if $router.route?.name === "song"}
      {#key $router.params.id}
        <Song id={$router.params.id ?? ""} navigate={router.navigate} />
      {/key}
    {:else if $router.route?.name === "muse"}
      {#key $router.params.id}
        <Muse id={$router.params.id ?? ""} navigate={router.navigate} />
      {/key}
    {:else if $router.route?.name === "sounds"}
      <Sounds search={searchOf($router)} navigate={router.navigate} />
    {:else if $router.route?.name === "draft-new"}
      <DraftNew navigate={router.navigate} />
    {:else if $router.route?.name === "draft-upload"}
      <DraftUpload />
    {:else if $router.route?.name === "draft"}
      {#key $router.params.id}
        <Draft id={$router.params.id ?? ""} navigate={router.navigate} />
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
    {:else if $router.route?.name === "human"}
      <Human />
    {:else if $router.route?.name === "human-tokens"}
      <HumanTokens />
    {:else if $router.route?.name === "human-devices"}
      <HumanDevices />
    {:else if $router.route?.name === "human-generate"}
      <HumanGenerate navigate={router.navigate} />
    {:else if $router.route?.name === "human-confirm"}
      {#key $router.params.batch}
        <HumanConfirm batch={$router.params.batch ?? ""} />
      {/key}
    {:else if $router.route?.name === "human-spend"}
      <HumanSpend />
    {:else if $router.route?.name === "jobs"}
      <Jobs />
    {:else if $router.route?.name === "job"}
      {#key $router.params.id}
        <Job id={$router.params.id ?? ""} />
      {/key}
    {:else if $router.route?.name === "batch"}
      {#key $router.params.id}
        <Batch id={$router.params.id ?? ""} />
      {/key}
    {:else if $router.route?.name === "lineage"}
      {#key $router.params.id}
        <Lineage id={$router.params.id ?? ""} />
      {/key}
    {:else if $router.route?.name === "offline"}
      <Offline />
    {:else if $router.route?.name === "human-connectors"}
      <HumanConnectors />
    {:else if $router.route?.name === "human-subsonic"}
      <HumanSubsonic />
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

  <footer class="player" aria-label="Player" bind:clientHeight={playerH} data-testid="player-footer">
    <PlayerBar {player} />
    <audio bind:this={audio} data-audio-id={audioId} preload="none"></audio>
  </footer>
</div>

<style>
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
    border-bottom: 1px solid var(--line);
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
    background: var(--accent);
    color: var(--accent-ink);
    font-size: 0.75rem;
  }
  .update {
    margin: 0 0 1rem;
    padding: 0.4rem 0.7rem;
    border: 1px solid var(--line);
    border-radius: 0.4rem;
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
    border-top: 1px solid var(--line);
    position: fixed;
    left: 0;
    right: 0;
    bottom: 0;
    z-index: 5;
    box-sizing: border-box;
    background: var(--bg-raised);
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
