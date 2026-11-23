<script lang="ts">
  // The secure create form (create_mandate is human-only, under /human/api, behind the step-up). It never raises a cap:
  // the caps from get_budget are shown beside the figures, and no money field is prefilled.
  import { api } from "../api/client";
  import { idempotentAction } from "../api/idem";
  import { owned, type OwnerBudget } from "../api/owner";
  import type { components } from "../api/schema";
  import { pushToast } from "../lib/judge.svelte";
  import { emptyMandateForm, formToMandate, MODES } from "../lib/mandateView";
  import { formatMicroUSD } from "../lib/money";

  let { navigate }: { navigate: (to: string) => void } = $props();

  let form = $state(emptyMandateForm());
  let caps = $state<OwnerBudget["caps"] | null>(null);
  let error = $state("");
  let busy = $state(false);
  // One idempotency key per submitted body: a retry of the same click repeats it, an edited form gets a new one.
  let attempt: { body: string; send: () => Promise<{ data?: components["schemas"]["Mandate"]; error?: { message?: string } }> } | null = null;

  $effect(() => {
    void (async () => {
      try {
        const r = await api.GET("/api/budget", {});
        if (r.data) caps = owned<components["schemas"]["Budget"], "caps" | "month" | "day" | "balance" | "auto_approve">(r.data).caps;
      } catch {
        caps = null;
      }
    })();
  });

  async function submit(e: Event) {
    e.preventDefault();
    if (busy) return;
    const res = formToMandate(form, new Date());
    if (!res.ok) return void (error = res.error);
    const key = JSON.stringify(res.body);
    if (!attempt || attempt.body !== key) {
      const body = res.body;
      attempt = { body: key, send: idempotentAction((k) => api.POST("/human/api/mandates", { params: { header: { "Idempotency-Key": k } }, body })) };
    }
    busy = true;
    try {
      const r = await attempt.send();
      if (r.data) {
        pushToast("Mandate created.");
        navigate("/mandates/" + r.data.id);
      } else error = r.error?.message ?? "Could not create the mandate";
    } catch {
      error = "Could not create the mandate";
    } finally {
      busy = false;
    }
  }
</script>

<section data-testid="human-mandate-new-page">
  <h1>New mandate</h1>
  <p class="muted">
    This lets the night shift spend, with no click, inside the figures below and nowhere else. This never raises a cap: the monthly and per-batch caps still apply, and a budget above what is left is refused.
  </p>
  <form onsubmit={submit} data-testid="mandate-form">
    <label>Title
      <input type="text" bind:value={form.title} maxlength="120" data-testid="mandate-title" />
    </label>
    <label>Budget for the whole mandate (USD)
      <input type="text" inputmode="decimal" bind:value={form.budget} data-testid="mandate-budget" autocomplete="off" />
      {#if caps}<span class="hint">Monthly cap {formatMicroUSD(caps.monthly_micro_usd)}</span>{/if}
    </label>
    <label>Ceiling per night (USD)
      <input type="text" inputmode="decimal" bind:value={form.ceiling} data-testid="mandate-ceiling" autocomplete="off" />
      {#if caps}<span class="hint">Per-batch cap {formatMicroUSD(caps.batch_micro_usd)}; the night is one batch</span>{/if}
    </label>
    <label>Starts (blank: now)
      <input type="datetime-local" bind:value={form.from} data-testid="mandate-from" />
    </label>
    <label>Ends (at most 31 days later)
      <input type="datetime-local" bind:value={form.until} data-testid="mandate-until" />
    </label>
    <label>Takes per night (1 to 12)
      <input type="text" inputmode="numeric" bind:value={form.takesPerNight} data-testid="mandate-takes-night" />
    </label>
    <label>Takes per lineage (1 to 50, over the whole mandate)
      <input type="text" inputmode="numeric" bind:value={form.takesPerLineage} data-testid="mandate-takes-lineage" />
    </label>
    <label>Only lineages rated at least (stars)
      <input type="text" inputmode="numeric" bind:value={form.minStars} data-testid="mandate-min-stars" />
    </label>
    <fieldset>
      <legend>Modes the night may use</legend>
      {#each MODES as m (m.key)}
        <label class="check"><input type="checkbox" bind:checked={form.modes[m.key]} data-testid="mandate-mode-{m.key}" /> {m.label}</label>
      {/each}
    </fieldset>
    <label>Lineage filter (root track ids, one per line; blank: every lineage)
      <textarea rows="3" bind:value={form.roots} data-testid="mandate-roots"></textarea>
    </label>
    <label class="check"><input type="checkbox" bind:checked={form.tierB} data-testid="mandate-tier-b" /> Allow the listening checks (Tier B) to be priced into a night</label>
    <p class="muted">Engine: yue2. One batch a night.</p>
    {#if error}<p role="alert" data-testid="mandate-form-error">{error}</p>{/if}
    <button type="submit" disabled={busy} data-testid="mandate-create">Create mandate</button>
  </form>
  <p><a href="/mandates">Back to the mandates</a></p>
</section>

<style>
  form {
    display: grid;
    gap: var(--sp-3);
    max-width: 32rem;
  }
  label {
    display: grid;
    gap: var(--sp-1);
  }
  label.check {
    display: flex;
    align-items: center;
    gap: var(--sp-2);
    min-height: var(--hit);
  }
  fieldset {
    border: 1px solid var(--line);
    border-radius: var(--r-md);
  }
  .hint {
    font-size: var(--fs-xs);
    color: var(--text-2);
  }
</style>
