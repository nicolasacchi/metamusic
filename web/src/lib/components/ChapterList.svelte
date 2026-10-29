<script lang="ts">
  // The chapters of a tape: select one to seek the playing tape there (recordPlayer.seekToChapter).
  import { chapterAt } from "../recordPlayer";
  import { fmt } from "../time";

  interface Chapter {
    index: number;
    slot_id: string;
    start_ms: number;
  }
  interface Props {
    chapters: readonly Chapter[];
    /** slot id -> name; a chapter without one reads "Track n". */
    names?: Record<string, string>;
    /** Playhead of the tape (ms); marks the playing chapter. Absent: none marked. */
    positionMs?: number | null;
    onselect(index: number): void;
  }
  let { chapters, names = {}, positionMs = null, onselect }: Props = $props();

  const current = $derived(positionMs === null ? -1 : chapterAt(chapters, positionMs));
  const rows = $derived([...chapters].sort((a, b) => a.index - b.index));
</script>

<ol class="chapters" data-testid="chapter-list" aria-label="Chapters">
  {#each rows as c (c.index)}
    <li>
      <button type="button" class="row" aria-current={c.index === current ? "true" : undefined} data-testid="chapter-row" onclick={() => onselect(c.index)}>
        <span class="n">{c.index}</span>
        <span class="name">{names[c.slot_id] ?? `Track ${c.index}`}</span>
        <span class="at">{fmt(c.start_ms / 1000)}</span>
      </button>
    </li>
  {/each}
</ol>

<style>
  .chapters {
    list-style: none;
    margin: 0;
    padding: 0;
    display: grid;
    gap: var(--sp-1);
  }
  .row {
    display: grid;
    grid-template-columns: 2ch 1fr auto;
    align-items: center;
    gap: var(--sp-3);
    width: 100%;
    min-height: var(--hit);
    padding: 0 var(--sp-3);
    text-align: left;
    font-variant-numeric: tabular-nums;
  }
  .row[aria-current="true"] {
    background: var(--accent-soft);
    border-color: var(--accent);
    color: var(--accent);
    font-weight: var(--fw-bold);
  }
  .n,
  .at {
    color: var(--text-3);
  }
  .name {
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
</style>
