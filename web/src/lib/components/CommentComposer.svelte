<script lang="ts">
  import { api } from "../../api/client";
  import { idempotentAction } from "../../api/idem";
  import type { components } from "../../api/schema";
  import { pushToast } from "../judge.svelte";
  import { formatAt } from "../comments";

  type Comment = components["schemas"]["Comment"];
  type Kind = Comment["kind"];
  interface Props {
    trackId: string;
    /** Playback position when `trackId` is the current track; null otherwise (no "at" toggle). */
    current: number | null;
    playing?: boolean;
    oncreated?: (c: Comment) => void;
    oncancel?: () => void;
  }
  let { trackId, current, playing = false, oncreated, oncancel }: Props = $props();

  let body = $state("");
  let kind = $state<Kind>("note");
  // svelte-ignore state_referenced_locally
  let useTime = $state(current !== null && playing);
  // Frozen when the user starts typing so the timestamp matches what they heard.
  let at = $state<number | null>(null);
  let busy = $state(false);
  let action: (() => Promise<{ data?: Comment; message?: string }>) | null = null;
  let actionSig = "";

  const KINDS: Kind[] = ["note", "fix", "keep", "marker"];
  const shown = $derived(at ?? current);

  async function submit(e: Event) {
    e.preventDefault();
    const text = body.trim();
    if (!text || busy) return;
    const atSecond = useTime && shown !== null ? Math.round(shown * 10) / 10 : null;
    const payload = { body: text, kind, ...(atSecond !== null ? { at_second: atSecond } : {}) };
    // One key per user action: a retry of the same content reuses it; edited content is a new action.
    const sig = JSON.stringify(payload);
    if (!action || sig !== actionSig) {
      actionSig = sig;
      action = idempotentAction(async (key) => {
        const r = await api.POST("/api/tracks/{id}/comments", {
          params: { path: { id: trackId }, header: { "Idempotency-Key": key } },
          body: payload,
        });
        return r.data ? { data: r.data } : { message: r.error?.message ?? "Could not add the comment" };
      });
    }
    busy = true;
    try {
      const r = await action();
      if (r.data) {
        action = null;
        body = "";
        at = null;
        oncreated?.(r.data);
      } else pushToast(r.message ?? "Could not add the comment");
    } catch (err) {
      pushToast(err instanceof Error && err.name === "RateLimited" ? `Too many changes — try again in ${(err as { retryAfterS?: number }).retryAfterS ?? 30} s` : "Could not add the comment");
    } finally {
      busy = false;
    }
  }
</script>

<form class="composer" onsubmit={submit} data-testid="comment-composer">
  <label class="sr" for="cc-body-{trackId}">Comment</label>
  <textarea
    id="cc-body-{trackId}"
    bind:value={body}
    rows="2"
    maxlength="4000"
    placeholder="Comment"
    oninput={() => (at ??= current)}
  ></textarea>
  <div class="row">
    <select bind:value={kind} aria-label="Kind">
      {#each KINDS as k (k)}<option value={k}>{k}</option>{/each}
    </select>
    {#if current !== null}
      <label class="check"
        ><input type="checkbox" bind:checked={useTime} /> at current time {shown !== null ? formatAt(shown) : ""}</label
      >
    {/if}
    <button type="submit" disabled={busy || body.trim() === ""}>Comment</button>
    {#if oncancel}<button type="button" onclick={oncancel}>Cancel</button>{/if}
  </div>
</form>

<style>
  .composer {
    display: grid;
    gap: 0.4rem;
  }
  textarea {
    width: 100%;
    box-sizing: border-box;
    font: inherit;
  }
  .row {
    display: flex;
    flex-wrap: wrap;
    gap: 0.5rem;
    align-items: center;
  }
  .check {
    font-size: 0.85rem;
  }
  .sr {
    position: absolute;
    left: -9999px;
  }
</style>
