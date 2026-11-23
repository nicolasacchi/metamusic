<script lang="ts">
  // "Plan new scores" on /desk (DESK-W2, m5b-desk C363.2): ask the GPU to draft scores for 1..16 seeds of a take (plan_seeds).
  // It never creates a job: it hands a plan_seeds intent to /human/generate, which prices it (an estimate) and asks for
  // confirmation like any generation; an Editor's request waits for the Owner. While the worker image is closed for planning the
  // form says so plainly and offers no button, instead of failing after a click.
  import { api as defaultApi } from "../../api/client";
  import { buildPlanSeeds, defaultSeedsForm, planSeedsGate, SEEDS_MAX, type SeedsForm } from "../deskRender";
  import { startGeneration } from "../genIntent";
  import { me } from "../me.svelte";

  interface Props {
    /** The takes the Desk already has plans for, offered as bases. */
    bases?: readonly { id: string; title: string }[];
    base?: string;
    go: (to: string) => void;
    client?: typeof defaultApi;
    allowed?: readonly string[];
    /** Worker capabilities when known (unit tests); otherwise read through get_provider_status when the caller may. */
    caps?: readonly string[] | null;
  }
  let { bases = [], base = "", go, client = defaultApi, allowed = me.allowed, caps: givenCaps }: Props = $props();

  // svelte-ignore state_referenced_locally
  let form = $state<SeedsForm>(defaultSeedsForm(base));
  let touched = $state(false);
  // svelte-ignore state_referenced_locally
  let caps = $state<readonly string[] | null>(givenCaps ?? null);
  $effect(() => {
    if (base !== "") form.base = base;
  });
  $effect(() => {
    if (givenCaps !== undefined || !allowed.includes("get_provider_status")) return;
    let live = true;
    void (async () => {
      try {
        const r = await client.GET("/api/provider", {});
        if (live && r.data) caps = r.data.worker.caps;
      } catch {
        // unknown: the estimate decides, in words
      }
    })();
    return () => {
      live = false;
    };
  });

  const gate = $derived(planSeedsGate(allowed, caps));
  const built = $derived(buildPlanSeeds(form));
  function submit() {
    touched = true;
    if (built.intent) startGeneration(sessionStorage, built.intent, go);
  }
</script>

{#if gate.kind !== "hidden"}
  <section class="card" aria-labelledby="seeds-h" data-testid="plan-seeds">
    <h2 id="seeds-h">Plan new scores</h2>
    <p class="muted">Have the model draft a score for each of up to {SEEDS_MAX} seeds of a take, then hear them here before paying for audio. You see the price first.</p>
    {#if gate.kind === "closed"}
      <p class="muted" role="status" data-testid="plan-seeds-unavailable">{gate.reason}</p>
    {:else}
      <form
        onsubmit={(e) => {
          e.preventDefault();
          submit();
        }}
      >
        <label>Take
          <input type="text" list="seeds-bases" autocomplete="off" spellcheck="false" value={form.base} oninput={(e) => (form.base = e.currentTarget.value)} aria-invalid={touched && built.errors.base ? "true" : undefined} data-testid="seeds-base" />
          <datalist id="seeds-bases">
            {#each bases as b (b.id)}<option value={b.id}>{b.title}</option>{/each}
          </datalist>
        </label>
        {#if touched && built.errors.base}<small class="err" role="alert">{built.errors.base}</small>{/if}
        <label>Seeds (1–{SEEDS_MAX})
          <input type="text" inputmode="numeric" value={form.seeds} oninput={(e) => (form.seeds = e.currentTarget.value)} aria-invalid={touched && built.errors.seeds ? "true" : undefined} data-testid="seeds-count" />
        </label>
        {#if touched && built.errors.seeds}<small class="err" role="alert">{built.errors.seeds}</small>{/if}
        <label>Planning style
          <select value={form.cot} onchange={(e) => (form.cot = e.currentTarget.value as SeedsForm["cot"])} data-testid="seeds-cot">
            <option value="">As the take</option>
            <option value="full">Full score</option>
            <option value="melody">Melody only</option>
          </select>
        </label>
        <button type="submit" class="primary" data-testid="seeds-price">Price the planning…</button>
      </form>
    {/if}
  </section>
{/if}

<style>
  form {
    display: grid;
    gap: var(--sp-2);
  }
  label {
    display: flex;
    flex-direction: column;
    gap: var(--sp-1);
    font-size: var(--fs-sm);
    min-width: 0;
  }
  input,
  select,
  button {
    min-height: var(--hit);
    max-width: 100%;
    font: inherit;
    box-sizing: border-box;
  }
  .primary {
    background: var(--accent);
    color: var(--accent-ink);
    border: none;
    border-radius: var(--r-md);
    font-weight: var(--fw-bold);
  }
  .err {
    color: var(--bad);
  }
</style>
