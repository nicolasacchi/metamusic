<script lang="ts">
  // Smart round (C258, C262): the pairs whose next human verdict teaches the ladder most, optionally locked to one section.
  // Blind by default: while blind, pairs show as "Pair n" with their information score only, never the titles, and a
  // created duel is opened straight away (the duel page keeps slots and windows hidden until the verdict).
  import { api } from "../../api/client";
  import { newIdempotencyKey } from "../../api/idem";
  import type { PairSuggestion } from "../pairSuggestApi";
  import {
    NO_SECTION,
    SECTION_KINDS,
    canSuggest,
    loadSuggestions,
    pairHint,
    pairTitle,
    startPair,
    type SectionChoice,
    type SmartScope,
  } from "../smartRound";

  interface Props {
    navigate: (to: string) => void;
  }
  let { navigate }: Props = $props();

  let scope = $state<"lineage" | "global">("global");
  let root = $state("");
  let k = $state(6);
  let kind = $state("");
  let ordinal = $state(1);
  let blind = $state(true);

  let pairs = $state<PairSuggestion[] | null>(null);
  let counted = $state(0);
  let loading = $state(false);
  let unavailable = $state(false);
  let error = $state("");
  let starting = $state<number | null>(null);
  // One key per pair and section choice, kept across retries of the same action.
  const keys = new Map<string, string>(); // eslint-disable-line svelte/prefer-svelte-reactivity

  const section = $derived<SectionChoice>(kind ? { kind, ordinal } : NO_SECTION);
  const smart = $derived<SmartScope>({ scope, rootTrackId: root, k, section });

  async function suggest() {
    if (!canSuggest(smart) || loading) return;
    loading = true;
    error = "";
    const r = await loadSuggestions(api, smart);
    if (r.kind === "pairs") {
      pairs = r.pairs;
      counted = r.verdictsCounted;
    } else if (r.kind === "unavailable") unavailable = true;
    else error = r.message;
    loading = false;
  }

  async function start(p: PairSuggestion, i: number) {
    if (starting !== null) return;
    starting = i;
    error = "";
    const sig = JSON.stringify([p.a_track_id, p.b_track_id, kind, ordinal, blind]);
    let key = keys.get(sig);
    if (!key) {
      key = newIdempotencyKey();
      keys.set(sig, key);
    }
    const r = await startPair(api, p, key, blind, section);
    if (r.kind === "created") navigate(`/duels/${encodeURIComponent(r.id)}`);
    else error = r.message;
    starting = null;
  }
</script>

<section class="smart" data-testid="smart-round" aria-label="Smart round">
  <h2>Smart round</h2>
  {#if unavailable}
    <p class="muted" data-testid="smart-unavailable">Pair suggestions are not available on this server yet.</p>
  {:else}
    <form
      class="ctl"
      onsubmit={(e) => {
        e.preventDefault();
        void suggest();
      }}
    >
      <label>From
        <select bind:value={scope} data-testid="smart-scope">
          <option value="global">Recent rated tracks</option>
          <option value="lineage">One lineage</option>
        </select>
      </label>
      {#if scope === "lineage"}
        <label>Root track id <input bind:value={root} data-testid="smart-root" autocomplete="off" /></label>
      {/if}
      <label>Section
        <select bind:value={kind} data-testid="smart-section">
          <option value="">Whole-track window</option>
          {#each SECTION_KINDS as s (s)}<option value={s}>{s}</option>{/each}
        </select>
      </label>
      {#if kind}
        <label>Number <input type="number" min="1" max="20" bind:value={ordinal} data-testid="smart-ordinal" inputmode="numeric" /></label>
      {/if}
      <label>Pairs <input type="number" min="1" max="15" bind:value={k} inputmode="numeric" /></label>
      <label class="chk"><input type="checkbox" bind:checked={blind} data-testid="smart-blind" /> Blind</label>
      <button type="submit" disabled={loading || !canSuggest(smart)} data-testid="smart-suggest">{loading ? "Looking…" : "Suggest pairs"}</button>
    </form>
    {#if error}<p role="alert">{error}</p>{/if}
    {#if pairs}
      {#if pairs.length === 0}
        <p class="muted" data-testid="smart-none">No pair needs another verdict right now.</p>
      {:else}
        <p class="muted">{pairs.length} suggested from {counted} human verdicts</p>
        <ul class="pairs" data-testid="smart-pairs">
          {#each pairs as p, i (p.a_track_id + p.b_track_id)}
            <li>
              <span class="what"><strong>{pairTitle(p, i, blind)}</strong> <span class="muted">{pairHint(p)}</span></span>
              <button type="button" disabled={starting !== null} data-testid="smart-start" onclick={() => void start(p, i)}>{starting === i ? "Starting…" : "Duel"}</button>
            </li>
          {/each}
        </ul>
      {/if}
    {/if}
  {/if}
</section>

<style>
  .smart {
    min-width: 0;
    margin: var(--sp-3) 0;
    padding: var(--sp-3);
    border: 1px solid var(--line-strong);
    border-radius: var(--r-md);
  }
  h2 {
    margin: 0 0 var(--sp-2);
    font-size: 1rem;
  }
  .ctl {
    display: flex;
    flex-wrap: wrap;
    gap: var(--sp-2) var(--sp-3);
    align-items: end;
  }
  .ctl label {
    display: grid;
    gap: var(--sp-1);
    min-width: 0;
  }
  .ctl .chk {
    display: flex;
    align-items: center;
    min-height: var(--hit);
  }
  .ctl select,
  .ctl input:not([type="checkbox"]) {
    min-height: var(--hit);
    max-width: 100%;
    box-sizing: border-box;
  }
  .ctl input[type="number"] {
    width: 5.5rem;
  }
  button {
    min-height: var(--hit);
    padding: 0 var(--sp-4);
  }
  .pairs {
    list-style: none;
    margin: var(--sp-2) 0 0;
    padding: 0;
    display: grid;
    gap: var(--sp-2);
  }
  .pairs li {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    justify-content: space-between;
    gap: var(--sp-2);
  }
  .what {
    min-width: 0;
    overflow-wrap: anywhere;
  }
</style>
