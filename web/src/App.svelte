<script lang="ts">
  import { untrack } from "svelte";
  import { APP_ROUTES, createRouter } from "./lib/router";
  import { randomId } from "./lib/id";
  import { player } from "./lib/player.svelte";
  import { dismissToast, toasts } from "./lib/toast.svelte";
  import { installDevice } from "./lib/device.svelte";
  import { installMediaSession, mediaSessionEnv } from "./lib/mediaSession";
  import { covers } from "./lib/cover";
  import { installListen } from "./lib/listen.svelte";
  import { isTabRoot, tabFor, titleFor, TABS, type Tab } from "./lib/nav";
  import { api } from "./api/client";
  import { loadDuelBadge } from "./lib/navBadge";
  import { approvals, refreshApprovals } from "./lib/approvals.svelte";
  import { badgeText } from "./lib/approvals";
  import { badge as inboxBadge, inbox, installHeard, refreshInboxBadge } from "./lib/inbox.svelte";
  import { restoreMore } from "./lib/queueSource";
  import PlayerDock from "./lib/components/PlayerDock.svelte";
  import NowPlayingSheet from "./lib/components/NowPlayingSheet.svelte";
  import { installSheet, sheet } from "./lib/nowPlaying.svelte";
  import { installPersistence, load as loadSaved, persistEnv } from "./lib/playerStore";
  import TopBar from "./lib/components/shell/TopBar.svelte";
  import TabBar from "./lib/components/shell/TabBar.svelte";
  import SubNav from "./lib/components/shell/SubNav.svelte";
  import { viewport } from "./lib/viewport.svelte";
  import { judgeCard } from "./lib/judgeCard.svelte";
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
  import Judge from "./routes/Judge.svelte";
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
  import New from "./routes/New.svelte";
  import Queue from "./routes/Queue.svelte";
  import More from "./routes/More.svelte";
  import NotFound from "./routes/NotFound.svelte";
  import { offline } from "./lib/offline.svelte";
  import { applyUpdate, watchForUpdate } from "./api/swRegister";

  const router = createRouter(APP_ROUTES);
  const audioId = randomId();
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
  // New tab badge: unseen new takes plus needs-you items; refreshed on navigation and with the approvals poll.
  $effect(() => {
    void $router.path;
    void refreshInboxBadge();
  });
  $effect(() => {
    const t = setInterval(() => {
      if (!document.hidden) void refreshApprovals();
      if (!document.hidden) void refreshInboxBadge();
    }, 30_000);
    return () => clearInterval(t);
  });

  // The player bar is fixed to the viewport bottom; routed content reserves its (variable) height so that no control
  // (Approve, Reject, duel verdicts) can ever sit under it.
  let playerH = $state(0);
  // scroll-padding keeps scrollIntoView, focus and find-in-page from parking a control under the bar.
  $effect(() => {
    document.documentElement.style.setProperty("scroll-padding-bottom", `calc(${playerH}px + var(--nav-b))`);
    document.documentElement.style.setProperty("--footer-h", `${playerH}px`);
  });
  // A route change under an open phone sheet (not one the sheet made itself) dismisses it.
  // (Closing the sheet with Back re-publishes the same location: only a changed path counts.)
  let sheetPath = "";
  const sheetKey = $derived($router.path + "?" + JSON.stringify($router.query));
  $effect(() => {
    const k = sheetKey;
    const changed = sheetPath !== "" && k !== sheetPath;
    sheetPath = k;
    if (changed && untrack(() => sheet.open && viewport.phone)) sheet.dismissForNavigation();
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

  const routeName = $derived($router.route?.name);
  // The phone sheet is modal: the page behind it cannot be focused or tapped.
  const modalSheet = $derived(sheet.open && viewport.phone);
  const tabBadges = $derived.by(() => {
    const b: Partial<Record<Tab, { text: string; label: string; testid: string }>> = {};
    const nb = inboxBadge();
    if (nb) b.new = { text: nb, label: `${nb} new or waiting for you`, testid: "new-badge" };
    if (duelBadge) b.judge = { text: String(duelBadge), label: `${duelBadge} open duels`, testid: "duels-badge" };
    if (approvalBadge) b.more = { text: approvalBadge, label: `${approvalBadge} approvals pending`, testid: "approvals-badge" };
    return b;
  });
  // Phone Back button: one step in history, or the tab's landing page for a deep link.
  function goBack() {
    if (history.length > 1) history.back();
    else router.navigate(TABS.find((t) => t.id === tabFor(routeName))?.path ?? "/");
  }

  let audio: HTMLAudioElement;
  // Install once: untracked, because attach() writes player.el and the installers read player state;
  // tracked, the effect would re-run itself forever (one device-probe request per iteration).
  $effect(() =>
    untrack(() => {
      player.attach(audio);
      installDevice(player);
      const offMediaSession = installMediaSession(player, audio, mediaSessionEnv(covers.get));
      const offListen = installListen(player);
      const offHeard = installHeard(player);
      const offActivation = player.watchActivation(document);
      const offSheet = installSheet();
      // Boot restore (D2): paused, one tap resumes. Before persistence is installed, so an empty player never
      // overwrites the saved queue; after the Media Session, so the lock screen shows the restored track at once.
      const env = persistEnv();
      const saved = loadSaved(env.kv, env.now());
      if (saved && player.restore(saved) && saved.source) {
        // a restored /new or Library queue keeps paging: more() is rebuilt lazily from the saved source href
        const more = restoreMore(saved.source.href, {
          search: (query) => api.GET("/api/tracks", { params: { query } }).then((r) => r.data ?? null),
          seenAt: () => inbox.seenAt,
          queue: () => player.state.queue,
        });
        if (more) player.setQueueSource(more);
      }
      const offPersist = installPersistence(player, env);
      return () => {
        offPersist();
        offSheet();
        offActivation();
        offListen();
        offHeard();
        offMediaSession();
        player.attach(null);
        router.destroy();
      };
    }),
  );
</script>

<div class="shell">
  <TopBar title={titleFor(routeName)} back={viewport.phone && !isTabRoot(routeName)} onback={goBack} inert={modalSheet}>
    <TabBar {routeName} badges={tabBadges} />
  </TopBar>

  <main style:--player-h="{playerH}px" inert={modalSheet}>
    <SessionBanner />
    {#if isTabRoot(routeName)}<SubNav tab={tabFor(routeName)} {routeName} />{/if}
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
    {#if $router.route?.name === "new"}
      <New />
    {:else if $router.route?.name === "queue"}
      <Queue />
    {:else if $router.route?.name === "more"}
      <More />
    {:else if $router.route?.name === "home"}
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
    {:else if $router.route?.name === "judge"}
      <Judge search={searchOf($router)} navigate={router.navigate} />
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

  <div class="toasts" class:above-sheet={sheet.open} role="status" aria-live="polite">
    {#each toasts as t (t.id)}
      <div class="toast {t.kind}" data-testid="toast">
        {t.message}
        {#if t.action}
          {@const act = t.action}
          <button type="button" class="act" data-testid="toast-action" onclick={() => { dismissToast(t.id); act.run(); }}>{act.label}</button>
        {/if}
        <button type="button" aria-label="Dismiss" onclick={() => dismissToast(t.id)}>×</button>
      </div>
    {/each}
  </div>

  <NowPlayingSheet />

  <footer class="player" class:idle={!player.state.trackId} class:card={judgeCard.active} inert={modalSheet} aria-label="Player" bind:clientHeight={playerH} data-testid="player-footer">
    <PlayerDock {player} duel={routeName === "duel"} />
    <audio bind:this={audio} data-audio-id={audioId} preload="none"></audio>
  </footer>
</div>

<style>
  :global(:root) {
    /* Space the phone tab bar takes at the bottom (the desktop nav is a top row). */
    --nav-b: 0px;
  }
  @media (max-width: 767px) {
    :global(:root) {
      --nav-b: calc(var(--tabbar-h) + var(--safe-b));
    }
  }
  .shell {
    min-height: 100dvh;
    display: grid;
    grid-template-columns: minmax(0, 1fr);
    grid-template-rows: auto 1fr auto;
  }
  .update {
    margin: 0 0 1rem;
    padding: 0.4rem 0.7rem;
    border: 1px solid var(--line);
    border-radius: 0.4rem;
  }
  /* The player is fixed to the viewport bottom (above the phone tab bar); main reserves its height plus the tab bar's,
     so no control (Approve, Reject, duel verdicts) can ever sit under either. */
  main {
    min-width: 0;
    padding: 1rem;
    padding-bottom: calc(1rem + var(--player-h, 0px) + var(--nav-b));
    /* the desktop now-playing panel takes the right edge while it is open */
    padding-right: calc(1rem + var(--panel-pad, 0px));
  }
  .player {
    display: flex;
    gap: 1rem;
    align-items: center;
    padding: 0.5rem 1rem calc(0.5rem + env(safe-area-inset-bottom));
    min-height: var(--bar-desktop-h);
    border-top: 1px solid var(--line);
    position: fixed;
    left: 0;
    right: 0;
    bottom: 0;
    z-index: 5;
    box-sizing: border-box;
    background: var(--bg-raised);
  }
  @media (max-width: 767px) {
    .player {
      padding: 0;
      min-height: 0;
      height: var(--mini-h);
      bottom: var(--nav-b);
    }
    .player.idle {
      display: none;
    }
  }
  .toasts {
    position: fixed;
    right: 1rem;
    bottom: calc(7rem + var(--nav-b));
    display: grid;
    gap: 0.4rem;
    z-index: var(--z-toast, 30);
  }
  /* While the now-playing sheet is open the toasts sit at the top of the viewport: never over its transport and stars. */
  .toasts.above-sheet {
    top: calc(var(--safe-t, 0px) + 0.5rem);
    bottom: auto;
    left: 1rem;
    right: calc(1rem + var(--panel-pad, 0px));
    justify-items: end;
  }
  .toast {
    background: #5a2a22;
    border: 1px solid #a02a1f;
    border-radius: 0.4rem;
    padding: 0.4rem 0.7rem;
  }
  .toast.info,
  .toast.success {
    background: var(--surface-2, #2c2620);
    border-color: var(--line-strong, #786a5e);
  }
  .toast.success {
    border-color: var(--ok, #7bc47f);
  }
  .toast .act {
    border: 1px solid currentColor;
    border-radius: 0.3rem;
    padding: 0.2rem 0.5rem;
    margin: 0 0.3rem;
    min-height: var(--hit, 44px);
  }
  .toast button {
    background: none;
    border: none;
    color: inherit;
    cursor: pointer;
  }
  /* The Judge card is the player while it is on screen. */
  .player.card {
    display: none;
  }
  .player audio {
    display: none;
  }
</style>
