<script lang="ts">
  import { api } from "../../api/client";
  import type { components } from "../../api/schema";
  import { sortComments } from "../comments";
  import { pushToast } from "../judge.svelte";
  import { player } from "../player.svelte";
  import CommentComposer from "./CommentComposer.svelte";
  import CommentItem from "./CommentItem.svelte";

  type Comment = components["schemas"]["Comment"];
  type Kind = Comment["kind"];
  interface Props {
    trackId: string;
    /** Loaded comments, shared with the waveform pins. */
    comments?: Comment[];
  }
  let { trackId, comments = $bindable([]) }: Props = $props();

  let scope = $state<"open" | "all">("open");
  let kind = $state<"" | Kind>("");
  let order = $state<"newest" | "time">("time");
  let total = $state(0);
  let next = $state<string | null>(null);
  let loading = $state(true);
  let error = $state("");
  let seq = 0;

  const isCurrent = $derived(player.state.trackId === trackId);
  const items = $derived(sortComments(comments, order));

  async function load(more: boolean) {
    const mine = ++seq;
    const mineTrack = trackId;
    loading = true;
    error = "";
    try {
      const r = await api.GET("/api/tracks/{id}/comments", {
        params: {
          path: { id: mineTrack },
          query: {
            ...(scope === "open" ? { state: ["open" as const] } : {}),
            ...(kind ? { kind: [kind] } : {}),
            order,
            limit: 100,
            ...(more && next ? { cursor: next } : {}),
          },
        },
      });
      if (mine !== seq) return;
      if (!r.data) {
        error = r.error?.message ?? "Failed to load comments";
        return;
      }
      comments = more ? [...comments, ...r.data.items] : r.data.items;
      total = r.data.total;
      next = r.data.next_cursor;
    } catch (e) {
      if (mine === seq) error = e instanceof Error && e.name === "RateLimited" ? "Too many requests — try again shortly" : "Failed to load comments";
    } finally {
      if (mine === seq) loading = false;
    }
  }

  // Reload whenever the track or a filter changes.
  $effect(() => {
    void trackId;
    void scope;
    void kind;
    void order;
    void load(false);
  });

  function created(c: Comment) {
    if (scope === "open" && c.state !== "open") return;
    if (kind && c.kind !== kind) return;
    comments = [c, ...comments];
    total += 1;
  }

  async function patch(c: Comment, body: components["schemas"]["UpdateCommentInBody"]) {
    try {
      const r = await api.PATCH("/api/comments/{id}", { params: { path: { id: c.id } }, body });
      if (!r.data) {
        pushToast(r.error?.message ?? "Could not update the comment");
        return;
      }
      const updated = r.data;
      const dropped = updated.hidden || (scope === "open" && updated.state !== "open");
      comments = dropped ? comments.filter((x) => x.id !== c.id) : comments.map((x) => (x.id === c.id ? updated : x));
      if (dropped) total = Math.max(0, total - 1);
    } catch (e) {
      pushToast(e instanceof Error && e.name === "RateLimited" ? `Too many changes — try again in ${(e as { retryAfterS?: number }).retryAfterS ?? 30} s` : "Could not update the comment");
    }
  }
</script>

<section class="panel" aria-label="Comments" data-testid="comments-panel">
  <div class="filters">
    <label>
      Show
      <select bind:value={scope}>
        <option value="open">open</option>
        <option value="all">all</option>
      </select>
    </label>
    <label>
      Kind
      <select bind:value={kind}>
        <option value="">any</option>
        <option value="note">note</option>
        <option value="fix">fix</option>
        <option value="keep">keep</option>
        <option value="marker">marker</option>
      </select>
    </label>
    <label>
      Order
      <select bind:value={order}>
        <option value="time">by time</option>
        <option value="newest">newest</option>
      </select>
    </label>
    <span class="muted">{total} {total === 1 ? "comment" : "comments"}</span>
  </div>

  <CommentComposer {trackId} current={isCurrent ? player.state.current : null} playing={isCurrent && player.state.playing} oncreated={created} />

  {#if error}<p role="alert">{error}</p>{/if}
  <ul aria-busy={loading}>
    {#each items as c (c.id)}
      <CommentItem comment={c} onstate={(x, state) => patch(x, { state })} onhide={(x) => patch(x, { hidden: true })} />
    {:else}
      {#if !loading}<li class="muted none">No comments.</li>{/if}
    {/each}
  </ul>
  {#if next}<button type="button" disabled={loading} onclick={() => load(true)}>Load more</button>{/if}
</section>

<style>
  .panel {
    display: grid;
    gap: 0.7rem;
  }
  .filters {
    display: flex;
    flex-wrap: wrap;
    gap: 0.6rem;
    align-items: end;
    font-size: 0.8rem;
  }
  label {
    display: grid;
    gap: 0.15rem;
  }
  ul {
    margin: 0;
    padding: 0;
  }
  .none {
    list-style: none;
  }
</style>
