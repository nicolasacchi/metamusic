<script lang="ts">
  import { untrack } from "svelte";
  import { api } from "../api/client";
  import type { components } from "../api/schema";
  import { trackPageQueue } from "../lib/queue";
  import { player } from "../lib/player.svelte";
  import { fmt, parseT } from "../lib/time";
  import { judge, createTagSuggestions } from "../lib/judge.svelte";
  import { jumpTo } from "../lib/jump";
  import StarRating from "../lib/components/StarRating.svelte";
  import StatusChips from "../lib/components/StatusChips.svelte";
  import TagEditor from "../lib/components/TagEditor.svelte";
  import CommentsPanel from "../lib/components/CommentsPanel.svelte";
  import Waveform from "../lib/components/Waveform.svelte";
  import LyricsPanel from "../lib/components/LyricsPanel.svelte";
  import AddToPlaylist from "../lib/components/AddToPlaylist.svelte";
  import ContextButton from "../lib/components/ContextButton.svelte";
  import MetricsCard from "../lib/components/MetricsCard.svelte";
  import EarsPanel from "../lib/components/EarsPanel.svelte";
  import LineageDuels from "../lib/components/LineageDuels.svelte";
  import TrackBadges from "../lib/components/TrackBadges.svelte";
  import PickBadge from "../lib/components/PickBadge.svelte";
  import RecipeUnknownPanel from "../lib/components/RecipeUnknownPanel.svelte";
  import CoverArt from "../lib/components/CoverArt.svelte";
  import DownloadFlacButton from "../lib/components/DownloadFlacButton.svelte";
  import OfflineButton from "../lib/components/OfflineButton.svelte";
  import PromptButtons from "../lib/components/PromptButtons.svelte";
  import GenerateButton from "../lib/components/GenerateButton.svelte";
  import ProvenancePanel from "../lib/components/ProvenancePanel.svelte";
  import type { Viewer } from "../lib/genIntent";
  import { fetchViewer } from "../lib/viewer";

  // `song` and `is_pick` are library read amendments (D-L1-21, C98) that the generated type learns at integration.
  type Track = components["schemas"]["TrackDetail"] & {
    song?: { id: string; title: string; angle?: string | null; muse_id?: string | null; pick_source?: string | null } | null;
    is_pick?: boolean;
  };
  interface Props {
    id: string;
    /** `?t=` deep link ("72" or "1:12"). */
    t?: string;
    navigate?: (to: string, replace?: boolean) => void;
    /** Pre-loaded detail (server-side rendering in unit tests; the page otherwise fetches it). */
    initial?: Track;
    /** The caller (unit tests pass it; the page otherwise asks /api/me once). */
    viewer?: Viewer | null;
  }
  let { id, t = "", navigate, initial, viewer = null }: Props = $props();
  // svelte-ignore state_referenced_locally
  let me = $state<Viewer | null>(viewer);
  $effect(() => {
    if (viewer) return;
    void fetchViewer().then((v) => (me = v));
  });

  /** Router-less fallback (the router listens to popstate). */
  function defaultNavigate(to: string) {
    history.pushState(null, "", to);
    window.dispatchEvent(new PopStateEvent("popstate"));
  }

  // svelte-ignore state_referenced_locally
  let track = $state<Track | null>(initial ?? null);
  let error = $state("");
  interface Level {
    data: number[];
    bits: number;
  }
  // The Pick take of this take's Song, fetched only when this take is not the Pick.
  let pickTake = $state<{ id: string; title: string } | null>(null);
  let overview = $state<Level | null>(null);
  let detail = $state<Level | null>(null);
  let width = $state(0);
  let comments = $state<components["schemas"]["Comment"][]>([]);
  let tagNames = $state<string[]>([]);
  // One list_tags call per page visit.
  const suggestions = createTagSuggestions(api);
  const j = $derived(judge.get(id));
  const pins = $derived(comments.flatMap((c) => (c.at_second === null ? [] : [{ id: c.id, t: c.at_second, kind: c.kind }])));

  const isCurrent = $derived(player.state.trackId === id);
  const position = $derived(isCurrent ? player.state.current : 0);
  const duration = $derived(isCurrent && player.state.duration > 0 ? player.state.duration : (track?.duration_s ?? 0));
  // The detail level is only worth fetching when the canvas has more device pixels than overview columns.
  const wantDetail = $derived(width * (typeof window === "undefined" ? 1 : window.devicePixelRatio || 1) > 2048);
  // Detail replaces the overview only when it is actually finer.
  const level = $derived(wantDetail && detail && (!overview || detail.data.length > overview.data.length) ? detail : overview);
  const peaks = $derived(level?.data ?? null);
  const bits = $derived(level?.bits ?? 8);

  // Load the track (and its overview peaks) whenever the route id changes.
  $effect(() => {
    const mine = id;
    track = null;
    error = "";
    overview = null;
    detail = null;
    void (async () => {
      try {
        const [tr, pk] = await Promise.all([
          api.GET("/api/tracks/{id}", { params: { path: { id: mine } } }),
          api.GET("/api/tracks/{id}/peaks", { params: { path: { id: mine }, query: { level: "overview" } } }),
        ]);
        if (mine !== id) return;
        if (!tr.data) {
          error = tr.response.status === 404 ? "Track not found" : (tr.error?.message ?? "Failed to load track");
          return;
        }
        track = tr.data;
        pickTake = null;
        const sg = (tr.data as Track).song;
        if (sg && (tr.data as Track).is_pick !== true) {
          void api
            .GET("/api/songs/{id}", { params: { path: { id: sg.id }, query: { take_limit: 50, include_events: 0 } } })
            .then((r) => {
              const p = r.data?.takes.find((x) => x.is_pick);
              if (mine === id && p) pickTake = { id: p.id, title: p.title };
            })
            .catch(() => {});
        }
        const r = tr.data.rating;
        judge.seed(mine, {
          stars: r.human?.stars ?? null,
          verdict: r.human?.verdict ?? null,
          critic: r.critic ? { stars: r.critic.stars, verdict: r.critic.verdict } : null,
          status: tr.data.status,
          tags: tr.data.tags,
        });
        void suggestions.names().then((n) => {
          if (mine === id) tagNames = n;
        });
        overview = pk.data?.peaks.data ? { data: pk.data.peaks.data, bits: pk.data.peaks.bits } : null;
      } catch {
        if (mine === id) error = "Failed to load track";
      }
    })();
  });

  // Deep link: load paused; the player applies the seek in loadedmetadata (or at once if already current).
  // Re-runs on in-app navigation to the same track with a new ?t=; the param is then dropped
  // (replace) so a history entry never re-applies it.
  $effect(() => {
    const tr = track;
    const start = parseT(t);
    if (!tr || tr.id !== id || start === null) return;
    player.load(id, untrack(() => trackPageQueue(id, player.state.queue, tr.lineage.siblings)), { title: tr.title, startAt: start, autoplay: false });
    navigate?.(`/tracks/${encodeURIComponent(id)}`, true);
  });

  $effect(() => {
    // Detail is 20 pairs/s: pointless when the whole track fits in the overview columns.
    if (!wantDetail || detail || !track || track.duration_s * 20 <= 2048) return;
    const mine = id;
    void (async () => {
      try {
        const r = await api.GET("/api/tracks/{id}/peaks", { params: { path: { id: mine }, query: { level: "detail" } } });
        if (mine === id && r.data?.peaks.data) {
          detail = { data: r.data.peaks.data, bits: r.data.peaks.bits };
        }
      } catch {
        /* the overview stays */
      }
    })();
  });

  function play(startAt?: number) {
    if (!track) return;
    if (isCurrent && startAt === undefined) {
      player.toggle();
      return;
    }
    if (isCurrent) {
      player.seek(startAt ?? 0);
      if (!player.state.playing) player.toggle();
      return;
    }
    player.play(id, trackPageQueue(id, player.state.queue, track.lineage.siblings), { title: track.title, ...(startAt !== undefined ? { startAt } : {}) });
  }
  function seek(s: number) {
    if (isCurrent) player.seek(s);
    else play(s);
  }
</script>

{#if error}
  <p role="alert">{error}</p>
  <p><a class="tap" href="/">Back to the library</a></p>
{:else if !track}
  <p class="muted">Loading…</p>
{:else}
  <article>
    <div class="head">
      <CoverArt trackId={id} title={track.title} size={128} />
      <div class="headtext">
        <h1 data-testid="track-title">{track.title}</h1>
        {#if track.song}
          <p class="song-strip" data-testid="song-strip">
            Song <a href="/songs/{track.song.id}" data-testid="song-link">{track.song.title}</a>
            {#if track.is_pick}
              <PickBadge source={track.song.pick_source ?? "newest"} />
            {:else if pickTake}
              · Pick of this Song: <a href="/tracks/{pickTake.id}" data-testid="song-pick-link">{pickTake.title}</a>
            {:else}
              · <a href="/songs/{track.song.id}" data-testid="song-pick-link">Pick of this Song</a>
            {/if}
          </p>
        {/if}
    {#if !track.recipe_known || track.lossy}
      <p class="badges" data-testid="track-badges"><TrackBadges recipeKnown={track.recipe_known} lossy={track.lossy} /></p>
    {/if}
    <p class="meta muted">
      {fmt(track.duration_s)} · recorded {track.recorded_at.slice(0, 10)} · {track.status} · seed {track.recipe.seed}
      {#if track.bpm_declared} · {track.bpm_declared} BPM{/if}
    </p>
      </div>
    </div>

    <div class="controls">
      <button type="button" data-testid="track-play" onclick={() => play()}>
        {isCurrent && player.state.playing ? "Pause" : "Play"}
      </button>
      <span class="time" data-testid="track-time">{fmt(position)} / {fmt(duration)}</span>
      <GenerateButton trackId={id} recipeKnown={track.recipe_known} lossy={track.lossy} {me} />
    </div>

    <div class="controls" data-testid="track-actions">
      <DownloadFlacButton trackId={id} lossy={track.lossy} />
      <OfflineButton trackId={id} />
    </div>

    <div class="wave" bind:clientWidth={width}>
      <Waveform {peaks} {duration} current={position} onseek={seek} {bits} height={96} label="Track waveform" {pins} onpin={(cid) => { const c = comments.find((x) => x.id === cid); if (c?.at_second != null) jumpTo(id, c.at_second); }} />
    </div>

    {#if j}
      <div class="judge" data-testid="track-judge">
        <StarRating value={j.stars} onchange={(n) => judge.rate(id, n)} label="Your rating" />
        <StatusChips value={j.status} onchange={(st) => judge.setStatus(id, st)} />
        {#if j.critic}
          <span class="muted" data-testid="critic">critic ★{j.critic.stars ?? "—"}{j.critic.verdict ? ` — ${j.critic.verdict}` : ""}</span>
        {/if}
      </div>
      <TagEditor tags={j.tags} suggestions={tagNames} onadd={(n) => judge.addTag(id, n)} onremove={(n) => judge.removeTag(id, n)} />
    {/if}

    <div class="terms">
      {#each track.style_terms as term (term)}<span class="chip">{term}</span>{/each}
    </div>

    <div class="grid">
      {#if track.recipe_known}
        <section>
          <h2>Lyrics</h2>
          <LyricsPanel sections={track.sections} timed={track.timed} current={isCurrent ? player.state.current : null} onseek={seek} />
        </section>
      {:else}
        <RecipeUnknownPanel title={track.title} lossy={track.lossy} masterBytes={track.audio.master_bytes} durationS={track.duration_s} />
      {/if}
      <aside>
        <section class="card">
          <h2>Comments</h2>
          <CommentsPanel trackId={id} bind:comments />
        </section>
        <section class="card">
          <AddToPlaylist trackId={id} member={track.playlists} />
        </section>
        <section class="card">
          <h2>Agent context</h2>
          <ContextButton trackId={id} />
          <PromptButtons trackId={id} recipeKnown={track.recipe_known} />
        </section>
        <MetricsCard analysis={track.analysis} truncated={track.truncated} />
        <EarsPanel trackId={id} />
        <LineageDuels
          trackId={id}
          rootId={track.lineage.root_track_id}
          parentId={track.lineage.parent_track_id}
          navigate={(to) => (navigate ?? defaultNavigate)(to)}
        />
        {#if track.generation}
          <ProvenancePanel
            trackId={id}
            generation={track.generation}
            parentId={track.lineage.parent_track_id}
            truncated={track.truncated}
            navigate={(to) => (navigate ?? defaultNavigate)(to)}
          />
        {/if}
        {#if track.recipe_known}
          <section class="card">
            <h2>Recipe</h2>
            <p class="style" data-testid="recipe-style">{track.recipe.style}</p>
            <p class="muted">request {track.recipe.request_id}</p>
          </section>
        {/if}
        <section class="card">
          <h2>Lineage</h2>
          <p><a class="tap" href="/lineage/{id}" data-testid="lineage-link">Full lineage tree</a></p>
          <p class="muted">
            generation {track.lineage.generation}{track.lineage.inferred ? " (inferred)" : ""}
            {#if track.lineage.parent_track_id}
              · <a href="/tracks/{track.lineage.parent_track_id}">parent</a>
            {/if}
            {#if track.lineage.root_track_id !== track.id}
              · <a href="/tracks/{track.lineage.root_track_id}">root</a>
            {/if}
          </p>
          {#if track.lineage.siblings.length > 0}
            <ul>
              {#each track.lineage.siblings as s (s.id)}
                <li><a class="tap" href="/tracks/{s.id}">{s.title}</a> <span class="muted">seed {s.seed}</span></li>
              {/each}
            </ul>
          {/if}
        </section>
      </aside>
    </div>
  </article>
{/if}

<style>
  h1 {
    margin-bottom: 0.2rem;
  }
  .meta {
    margin-top: 0;
  }
  .head {
    display: flex;
    gap: 1rem;
    align-items: flex-start;
  }
  .headtext {
    min-width: 0;
  }
  .badges {
    margin: 0.2rem 0;
  }
  .controls {
    display: flex;
    align-items: center;
    gap: 1rem;
    margin: 0.6rem 0;
  }
  .judge {
    display: flex;
    flex-wrap: wrap;
    gap: 0.8rem;
    align-items: center;
    margin: 0.4rem 0;
  }
  .time {
    font-variant-numeric: tabular-nums;
  }
  .wave {
    width: 100%;
  }
  .terms {
    margin: 0.6rem 0;
  }
  .chip {
    display: inline-block;
    font-size: 0.75rem;
    padding: 0 0.5rem;
    margin: 0 0.25rem 0.2rem 0;
    border-radius: 0.6rem;
    background: #3a322b;
  }
  .grid {
    display: grid;
    gap: 1.5rem;
    grid-template-columns: minmax(0, 2fr) minmax(16rem, 1fr);
  }
  @media (max-width: 760px) {
    .grid {
      grid-template-columns: minmax(0, 1fr);
    }
  }
  aside {
    display: grid;
    gap: 1rem;
    align-content: start;
  }
  .card {
    border: 1px solid #3a322b;
    border-radius: 0.5rem;
    padding: 0.5rem 0.9rem;
  }
  .card h2 {
    margin: 0 0 0.4rem;
    font-size: 1rem;
  }
  .style {
    margin: 0;
    overflow-wrap: anywhere;
  }
  ul {
    margin: 0;
    padding-left: 1.1rem;
  }
</style>
