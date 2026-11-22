<script lang="ts">
  // The karaoke view of a take's lyrics (M5b C347.3): the line and the word at the playhead, each line classed by how clearly it was
  // sung (the recogniser's class, or a person's mark), a tap on a line seeks, and two buttons per line mark it clear or unclear.
  // A line whose time is only estimated (no listening check, or the line was not heard) is dashed and has no word highlight.
  import { api } from "../../api/client";
  import { getTrackDiction, setDictionMarks } from "../dictionApi";
  import { activeLine, activeWord, humanMark, lineKey, lineTitle, markBody, mergePages, sectionsOf, toggleMark, wordTitle, type Line, type Page } from "../karaoke";
  import { can } from "../me.svelte";
  import { fmt } from "../time";

  interface Props {
    trackId: string;
    /** Playback position in seconds, or null when this take is not the one playing. */
    current: number | null;
    onseek: (seconds: number) => void;
    /** Unit tests: a loaded page (the component then fetches nothing). */
    initial?: Page;
  }
  let { trackId, current, onseek, initial }: Props = $props();

  // svelte-ignore state_referenced_locally
  let page = $state<Page | null>(initial ?? null);
  let loading = $state(false);
  let message = $state("");
  let saving = $state<Record<string, boolean>>({});
  let markMessage = $state("");

  const lines = $derived(page?.lines ?? []);
  const groups = $derived(sectionsOf(lines));
  const active = $derived(activeLine(lines, current));
  const activeW = $derived(active >= 0 ? activeWord(lines[active]!, current) : -1);
  const canMark = $derived(can("set_diction_marks"));
  const marked = $derived(lines.filter((l) => humanMark(l) !== "none").length);

  async function loadFrom(cursor: string | null, into: Page | null): Promise<boolean> {
    const mine = trackId;
    const r = await getTrackDiction(api, mine, cursor);
    if (mine !== trackId) return false; // the page now shows another take: this answer is not for it
    if (!r.ok) {
      message = r.message;
      return false;
    }
    page = into ? mergePages(into, r.value) : r.value;
    return true;
  }

  $effect(() => {
    if (initial) return;
    const mine = trackId;
    page = null;
    message = "";
    loading = true;
    void loadFrom(null, null).finally(() => {
      if (mine === trackId) loading = false;
    });
  });

  async function more() {
    if (!page?.next_cursor || loading) return;
    loading = true;
    const mine = trackId;
    await loadFrom(page.next_cursor, page);
    if (mine === trackId) loading = false;
  }

  /** Reload every page that is on screen (a withdrawn mark gives the recogniser's class back, which only the server knows). */
  async function reload() {
    const want = lines.length;
    const mine = trackId;
    let cur: Page | null = null;
    let cursor: string | null = null;
    for (;;) {
      const r = await getTrackDiction(api, mine, cursor);
      if (mine !== trackId) return;
      if (!r.ok) {
        message = r.message;
        return;
      }
      cur = cur ? mergePages(cur, r.value) : r.value;
      cursor = r.value.next_cursor;
      if (!cursor || cur.lines.length >= want) break;
    }
    page = cur;
  }

  async function mark(l: Line, wanted: "clear" | "unclear") {
    const key = lineKey(l);
    if (!canMark || saving[key] || !page) return;
    const next = toggleMark(l, wanted);
    saving[key] = true;
    markMessage = "";
    const r = await setDictionMarks(api, trackId, [markBody(l, next)]);
    saving[key] = false;
    if (!r.ok) {
      markMessage = r.message;
      return;
    }
    if (next === "none") {
      await reload();
      return;
    }
    const cur = page;
    if (!cur) return;
    page = { ...cur, lines: cur.lines.map((x) => (lineKey(x) === key ? { ...x, class: next, class_source: "human", mark: { ...x.mark, human: next } } : x)) };
  }
</script>

{#if page}
  <h2 class="sub">Diction</h2>
  <section class="karaoke" data-testid="karaoke" aria-label="Diction" data-basis={page.basis}>
    <p class="muted note" data-testid="karaoke-note">
      {#if page.basis === "asr"}
        Lines coloured by how clearly they were sung ({page.lang}), as a recogniser heard them: a suspicion, not a verdict.
      {:else}
        No listening check: line times are estimated from the plan, and nothing is coloured by what was heard.
      {/if}
      {#if canMark}Mark a line clear or unclear to teach the thresholds.{/if}
      {#if marked > 0}<span data-testid="karaoke-marked">{marked} marked here.</span>{/if}
    </p>
    {#if page.lexicon_hits.length > 0}
      <p class="muted note" data-testid="karaoke-hits">
        Words that are often mangled:
        {#each page.lexicon_hits as h, i (h.word)}{i > 0 ? ", " : " "}<strong>{h.word}</strong> ({h.mangled} of {h.attempts}){/each}.
        <a href="/diction">Diction Lab</a>
      </p>
    {/if}
    {#each groups as g (g.section_idx)}
      <div class="sec" data-testid="karaoke-section">
        {#each g.lines as l, j (lineKey(l))}
          {@const idx = g.first + j}
          {@const mine = humanMark(l)}
          <div
            class="line k-{l.class}"
            class:estimated={l.timing === "estimated"}
            class:active={idx === active}
            data-testid="karaoke-line"
            data-class={l.class}
            data-source={l.class_source}
            data-timing={l.timing ?? "none"}
            data-active={idx === active ? "true" : "false"}
            aria-current={idx === active ? "true" : undefined}
          >
            <button
              type="button"
              class="text"
              disabled={l.start_s === null}
              title={lineTitle(l) + (l.start_s !== null ? ` · play from ${fmt(l.start_s)}` : "")}
              onclick={() => l.start_s !== null && onseek(l.start_s)}
            >
              {#if l.timing === "heard" && l.words.length > 0}
                {#each l.words as w, wi (wi)}
                  <span class="w m-{w.match}" class:now={idx === active && wi === activeW} title={wordTitle(w) || undefined}>{w.text}</span>
                {/each}
              {:else}
                {l.text}
              {/if}
            </button>
            {#if canMark}
              <span class="marks" role="group" aria-label="Mark this line">
                <button type="button" class="mk" aria-pressed={mine === "clear"} disabled={saving[lineKey(l)]} onclick={() => mark(l, "clear")} data-testid="mark-clear">Clear</button>
                <button type="button" class="mk" aria-pressed={mine === "unclear"} disabled={saving[lineKey(l)]} onclick={() => mark(l, "unclear")} data-testid="mark-unclear">Unclear</button>
              </span>
            {:else if mine !== "none"}
              <span class="muted tag">marked {mine}</span>
            {/if}
          </div>
        {/each}
      </div>
    {/each}
    {#if markMessage}<p role="alert" data-testid="karaoke-mark-error">{markMessage}</p>{/if}
    {#if page.next_cursor}
      <button type="button" class="more" disabled={loading} onclick={more} data-testid="karaoke-more">Show more lines</button>
    {/if}
  </section>
{:else if loading}
  <p class="muted" data-testid="karaoke-loading">Loading the diction…</p>
{:else if message}
  <p class="muted" role="status" data-testid="karaoke-unavailable">{message}</p>
{/if}

<style>
  h2.sub {
    margin: 1rem 0 0.4rem;
    font-size: 1rem;
  }
  .note {
    margin: 0 0 var(--sp-2);
    font-size: var(--fs-sm);
  }
  .sec {
    margin-bottom: var(--sp-3);
  }
  .line {
    display: flex;
    align-items: center;
    gap: var(--sp-2);
    padding: 2px var(--sp-2);
    border-left: 3px solid var(--line);
    border-radius: var(--r-sm);
  }
  .line.active {
    background: var(--surface);
    outline: 1px solid var(--accent);
  }
  .k-clear {
    border-left-color: var(--ok);
  }
  .k-mixed {
    border-left-color: var(--accent);
  }
  .k-unclear {
    border-left-color: var(--bad);
  }
  .k-unknown {
    border-left-color: var(--line-strong);
  }
  .line.estimated {
    border-left-style: dashed;
  }
  .text {
    flex: 1;
    min-width: 0;
    text-align: left;
    background: none;
    border: none;
    color: var(--text);
    font: inherit;
    padding: var(--sp-1) 0;
    cursor: pointer;
    overflow-wrap: anywhere;
  }
  .text:disabled {
    color: var(--text-2);
    cursor: default;
  }
  .w {
    display: inline-block;
    margin-right: 0.3em;
  }
  .w.m-miss {
    text-decoration: underline wavy var(--bad);
  }
  .w.m-dropped {
    text-decoration: line-through;
    color: var(--text-2);
  }
  .w.m-close {
    text-decoration: underline dotted var(--accent);
  }
  .w.now {
    background: var(--accent-soft);
    color: var(--focus);
    border-radius: 3px;
  }
  .marks {
    display: inline-flex;
    gap: var(--sp-1);
    flex: none;
  }
  .mk {
    font-size: var(--fs-xs);
    min-height: 1.75rem;
    padding: 0 var(--sp-2);
  }
  .mk[aria-pressed="true"] {
    background: var(--accent);
    color: var(--accent-ink);
  }
  .tag {
    font-size: var(--fs-xs);
  }
  .more {
    margin-top: var(--sp-2);
  }
  @media (pointer: coarse) {
    .mk,
    .more {
      min-height: var(--hit);
      min-width: var(--hit);
    }
    .line {
      flex-wrap: wrap;
    }
  }
</style>
