<script lang="ts">
  import type { Chip } from "../boardView";

  interface Props {
    chip: Chip;
    selected?: boolean;
    /** Chips on the Muse page are links to the Song, not selectable. */
    onclick?: (e: MouseEvent) => void;
    href?: string;
  }
  let { chip, selected = false, onclick, href }: Props = $props();
  const GLYPH = { draft: "○", rendered: "●", judged: "✓", picked: "★" } as const;
  const tip = $derived(`${chip.title} · ${chip.state}${chip.pick_source ? ` · pick: ${chip.pick_source}` : ""}`);
</script>

{#snippet body()}
  <span class="glyph" aria-hidden="true">{GLYPH[chip.state]}</span>
  <span class="title">{chip.title}</span>
  {#if chip.takes > 0}<span class="takes" title="{chip.takes} takes">×{chip.takes}</span>{/if}
  {#if chip.queued}<span class="queued" data-testid="chip-queued">queued</span>{/if}
  {#if chip.tempo_mismatch}<span class="mismatch" role="img" aria-label="tempo mismatch" title="requested and measured tempo disagree">●</span>{/if}
{/snippet}

{#if href}
  <a class="chip {chip.state}" {href} title={tip} data-testid="song-chip" data-song-id={chip.id}>{@render body()}</a>
{:else}
  <button type="button" class="chip {chip.state}" aria-pressed={selected} title={tip} data-testid="song-chip" data-song-id={chip.id} {onclick}>{@render body()}</button>
{/if}

<style>
  .chip {
    display: inline-flex;
    align-items: center;
    gap: 0.3rem;
    max-width: 14rem;
    min-height: 2rem;
    padding: 0.15rem 0.5rem;
    border: 1px solid #5a4d43;
    border-radius: 0.4rem;
    background: transparent;
    color: inherit;
    font: inherit;
    font-size: 0.85rem;
    text-decoration: none;
    cursor: pointer;
  }
  @media (pointer: coarse), (max-width: 767px) {
    .chip {
      min-height: 44px;
    }
  }
  .chip[aria-pressed="true"] {
    background: #3a322b;
    border-color: #f2a65a;
  }
  .chip.draft {
    border-style: dashed;
  }
  .chip.rendered {
    border-color: #7a8a9a;
  }
  .chip.judged {
    border-color: #7aa07a;
  }
  .chip.picked {
    border-color: #f2a65a;
  }
  .title {
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .takes,
  .queued {
    font-size: 0.75rem;
    opacity: 0.75;
  }
  .mismatch {
    color: #e0a030;
    font-size: 0.6rem;
  }
</style>
