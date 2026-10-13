<script lang="ts">
  import type { components } from "../../api/schema";
  import { fmt } from "../time";
  import { judge } from "../judge.svelte";
  import { templateSelectable } from "../genIntent";
  import TrackBadges from "./TrackBadges.svelte";

  type Track = components["schemas"]["TrackSummary"];
  interface Props {
    tracks: readonly Track[];
    playingId?: string;
    onplay: (id: string) => void;
    /** Selection mode (C84.2): a checkbox column; reference and lossy rows are disabled. */
    selected?: ReadonlySet<string> | null;
    onselect?: (id: string, on: boolean) => void;
  }
  let { tracks, playingId = "", onplay, selected = null, onselect }: Props = $props();

  // A rating or status changed in this session shows at once, before the next fetch.
  function stars(t: Track): number | null {
    return judge.get(t.id)?.stars ?? t.stars;
  }
  function statusOf(t: Track): string {
    return judge.get(t.id)?.status ?? t.status;
  }
  function tagsOf(t: Track): string[] {
    const j = judge.get(t.id);
    return j ? j.tags.map((x) => x.name) : t.tags;
  }

  function lufs(v: number | null): string {
    return v === null ? "—" : v.toFixed(1);
  }
</script>

<div class="wrap">
  <table data-testid="track-table">
    <thead>
      <tr>
        {#if selected}<th scope="col"><span class="sr">Select</span></th>{/if}
        <th scope="col"><span class="sr">Play</span></th>
        <th scope="col">Title</th>
        <th scope="col">Style</th>
        <th scope="col">Stars</th>
        <th scope="col">Status</th>
        <th scope="col">Tags</th>
        <th scope="col" class="num">BPM</th>
        <th scope="col" class="num">Length</th>
        <th scope="col" class="num">LUFS</th>
        <th scope="col">Score</th>
        <th scope="col" class="num">Takes</th>
      </tr>
    </thead>
    <tbody>
      {#each tracks as t (t.id)}
        <tr data-testid="track-row" data-track-id={t.id} class:playing={t.id === playingId}>
          {#if selected}
            <td>
              <input
                type="checkbox"
                data-testid="row-select"
                aria-label="Select {t.title}"
                checked={selected.has(t.id)}
                disabled={!templateSelectable(t)}
                title={templateSelectable(t) ? "" : "recipe unknown"}
                onchange={(e) => onselect?.(t.id, e.currentTarget.checked)}
              />
              {#if !templateSelectable(t)}<span class="muted sel-note">recipe unknown</span>{/if}
            </td>
          {/if}
          <td>
            <button type="button" class="play" aria-label="Play {t.title}" onclick={() => onplay(t.id)}>▶</button>
          </td>
          <td>
            <a href="/tracks/{t.id}" data-testid="track-link">{t.title}</a>
            <TrackBadges recipeKnown={t.recipe_known} lossy={t.lossy} />
            {#if t.truncated}<span class="badge bad" title="The generation stream was cut short">truncated</span>{/if}
          </td>
          <td class="terms">
            {#each t.style_terms.slice(0, 5) as term (term)}<span class="chip">{term}</span>{/each}
          </td>
          <td class="stars" data-testid="row-stars" aria-label={stars(t) === null ? "Unrated" : `${stars(t)} stars`}>
            {#if stars(t) === null}<span class="muted">—</span>{:else}{"★".repeat(stars(t) ?? 0)}{/if}
            {#if t.critic_stars !== null}<span class="muted critic" title="Critic (agent) rating">critic {t.critic_stars}</span>{/if}
          </td>
          <td data-testid="row-status">{statusOf(t)}</td>
          <td class="terms">
            {#each tagsOf(t) as tag (tag)}<span class="chip">{tag}</span>{/each}
          </td>
          <td class="num">{t.bpm_declared ?? "—"}</td>
          <td class="num">{fmt(t.duration_s)}</td>
          <td class="num">{lufs(t.integrated_lufs)}</td>
          <td>
            {#if t.timed}<span class="badge ok">timed</span>{:else if t.has_score}<span class="badge">score</span>{:else}<span class="muted">—</span>{/if}
          </td>
          <td class="num">{t.children_count > 0 ? t.children_count : ""}</td>
        </tr>
      {:else}
        <tr><td colspan={selected ? 12 : 11} class="muted">No tracks match.</td></tr>
      {/each}
    </tbody>
  </table>
</div>

<style>
  .wrap {
    overflow-x: auto;
  }
  table {
    width: 100%;
    border-collapse: collapse;
  }
  th,
  td {
    text-align: left;
    padding: 0.35rem 0.5rem;
    border-bottom: 1px solid #2c2620;
    vertical-align: top;
  }
  .num {
    text-align: right;
    font-variant-numeric: tabular-nums;
    white-space: nowrap;
  }
  tr.playing {
    background: #231d17;
  }
  .play {
    min-width: 2.25rem;
    min-height: 2.25rem;
  }
  .chip,
  .badge {
    display: inline-block;
    font-size: 0.75rem;
    padding: 0 0.4rem;
    margin: 0 0.25rem 0.15rem 0;
    border-radius: 0.6rem;
    background: #3a322b;
  }
  .badge.bad {
    background: #a02a1f;
    color: #fff;
  }
  .badge.ok {
    background: #2a4a2c;
  }
  .stars {
    white-space: nowrap;
    color: #f2a65a;
  }
  .critic {
    font-size: 0.7rem;
    margin-left: 0.3rem;
    color: inherit;
  }
  .sel-note {
    font-size: 0.7rem;
  }
  .sr {
    position: absolute;
    left: -9999px;
  }
</style>
