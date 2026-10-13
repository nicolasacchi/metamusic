<script lang="ts">
  import type { components } from "../../api/schema";
  import { formatAt, sectionChip } from "../comments";
  import { jumpTo } from "../jump";

  type Comment = components["schemas"]["Comment"];
  interface Props {
    comment: Comment;
    onstate: (c: Comment, state: "open" | "resolved") => void;
    onhide: (c: Comment) => void;
  }
  let { comment, onstate, onhide }: Props = $props();

  const chip = $derived(sectionChip(comment));
  const href = $derived(comment.link.startsWith("/") ? comment.link : `/tracks/${encodeURIComponent(comment.track_id)}?t=${comment.at_second ?? 0}`);

  function jump(e: MouseEvent) {
    if (comment.at_second === null) return;
    e.preventDefault();
    jumpTo(comment.track_id, comment.at_second);
  }
</script>

<li class="comment" data-testid="comment" data-comment-id={comment.id} class:resolved={comment.state === "resolved"}>
  <div class="head">
    {#if comment.at_second !== null}
      <a class="at" {href} onclick={jump} data-testid="comment-at" title="Jump to {formatAt(comment.at_second)}">{formatAt(comment.at_second)}</a>
    {/if}
    {#if chip}<span class="badge">{chip}</span>{/if}
    <span class="badge kind">{comment.kind}</span>
    <span class="badge who" title={comment.author.kind}>{comment.author.name}</span>
    <span class="muted when">{comment.created_at.slice(0, 10)}</span>
  </div>
  <p class="body">{comment.body}</p>
  <div class="actions">
    {#if comment.state === "open"}
      <button type="button" onclick={() => onstate(comment, "resolved")}>Resolve</button>
    {:else if comment.state === "resolved"}
      <button type="button" onclick={() => onstate(comment, "open")}>Reopen</button>
    {:else}
      <span class="badge">{comment.state}</span>
    {/if}
    <button type="button" onclick={() => onhide(comment)}>Hide</button>
  </div>
</li>

<style>
  .comment {
    list-style: none;
    border-bottom: 1px solid #2c2620;
    padding: 0.5rem 0;
  }
  .comment.resolved .body {
    opacity: 0.6;
  }
  .head {
    display: flex;
    flex-wrap: wrap;
    gap: 0.4rem;
    align-items: center;
    font-size: 0.85rem;
  }
  .at {
    font-variant-numeric: tabular-nums;
    font-weight: 700;
  }
  .badge {
    background: #3a322b;
    border-radius: 0.6rem;
    padding: 0 0.45rem;
    font-size: 0.75rem;
  }
  .body {
    margin: 0.3rem 0;
    white-space: pre-wrap;
    overflow-wrap: anywhere;
  }
  .actions {
    display: flex;
    gap: 0.4rem;
  }
  .actions button {
    min-height: 1.9rem;
  }
</style>
