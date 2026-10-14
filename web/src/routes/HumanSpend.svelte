<script lang="ts">
  import { api } from "../api/client";
  import type { components } from "../api/schema";
  import BudgetBar from "../lib/components/BudgetBar.svelte";
  import ProviderBanner from "../lib/components/ProviderBanner.svelte";
  import { pushToast } from "../lib/judge.svelte";
  import { formatMicroUSD } from "../lib/money";
  import { CAP_FIELDS, capsToForm, formToCaps, textToToken, tokenToText, type CapsForm, type TokenLimitText } from "../lib/spendCaps";

  type Budget = components["schemas"]["Budget"];
  type Status = components["schemas"]["ProviderStatus"];
  type TokenInfo = components["schemas"]["TokenInfo"];

  let budget = $state<Budget | null>(null);
  let status = $state<Status | null>(null);
  let tokens = $state<TokenInfo[]>([]);
  let form = $state<CapsForm | null>(null);
  let tokenText = $state<Record<string, TokenLimitText>>({});
  let error = $state("");
  let capsError = $state("");
  let busy = $state(false);
  let reason = $state("");

  async function load() {
    try {
      const [b, s, t] = await Promise.all([api.GET("/api/budget", {}), api.GET("/api/provider", {}), api.GET("/api/tokens", {})]);
      if (b.data) {
        budget = b.data;
        form = capsToForm(b.data.caps);
        tokenText = Object.fromEntries(b.data.caps.tokens.map((c) => [c.token_id, tokenToText(c)]));
        error = "";
      } else error = b.error?.message ?? "Could not load the budget";
      if (s.data) status = s.data;
      if (t.data) tokens = t.data.items;
    } catch {
      error = "Could not load the spend settings";
    }
  }
  $effect(() => {
    void load();
  });

  async function saveCaps(e: Event) {
    e.preventDefault();
    if (!budget || !form || busy) return;
    capsError = "";
    const changed = [];
    for (const c of budget.caps.tokens) {
      const t = tokenText[c.token_id];
      if (!t) continue;
      const before = tokenToText(c);
      if (t.daily === before.daily && t.takes === before.takes && t.autoUSD === before.autoUSD) continue;
      const v = textToToken(c.token_id, t);
      if (typeof v === "string") return void (capsError = v);
      changed.push(v);
    }
    const res = formToCaps(form, budget.caps, changed);
    if (!res.ok) return void (capsError = res.error);
    busy = true;
    try {
      const r = await api.PUT("/human/api/spend/caps", { body: res.body });
      if (r.data) {
        pushToast("Caps saved.");
        await load();
      } else capsError = r.error?.message ?? "Could not save the caps";
    } catch {
      capsError = "Could not save the caps";
    } finally {
      busy = false;
    }
  }

  async function providerState(body: { armed?: boolean; reset_trip?: boolean }) {
    if (busy) return;
    if (reason.trim() === "") return void pushToast("A reason is required.");
    busy = true;
    try {
      const r = await api.POST("/human/api/provider/state", { body: { ...body, reason: reason.trim() } });
      if (r.data) {
        reason = "";
        await load();
      } else pushToast(r.error?.message ?? "Could not change the provider state");
    } catch {
      pushToast("Could not change the provider state");
    } finally {
      busy = false;
    }
  }
  const tokenName = (id: string) => tokens.find((t) => t.id === id)?.name ?? id;
</script>

<h1>Spend</h1>
<ProviderBanner provider={budget?.provider ?? null} />
{#if error}<p role="alert">{error}</p>{/if}

{#if budget && form}
  <section aria-label="Budget">
    <h2>Budget</h2>
    <BudgetBar label="Month" settled={budget.month.settled_micro_usd} reserved={budget.month.outstanding_micro_usd} cap={budget.month.cap_micro_usd} />
    <BudgetBar label="Today (you)" settled={budget.day.settled_micro_usd} reserved={budget.day.outstanding_micro_usd} cap={budget.day.cap_micro_usd} />
    {#if budget.balance.micro_usd !== null}<p class="muted">Provider balance {formatMicroUSD(budget.balance.micro_usd)}.</p>{/if}
  </section>

  <form onsubmit={saveCaps} data-testid="caps-form">
    <h2>Caps</h2>
    {#each CAP_FIELDS as f (f.key)}
      <label>{f.label} (USD)
        <input type="text" inputmode="decimal" bind:value={form.amounts[f.key]} data-testid="cap-{f.key}" />
      </label>
    {/each}
    <label>Agent auto-approve up to (takes)
      <input type="text" inputmode="numeric" bind:value={form.autoApproveTakes} data-testid="cap-auto_approve_takes" />
    </label>

    <h3>Per-token limits</h3>
    {#if budget.caps.tokens.length === 0}
      <p class="muted">No agent tokens have limits. Blank fields use the defaults above.</p>
    {:else}
      <table data-testid="token-limits">
        <thead><tr><th scope="col">Token</th><th scope="col">Daily (USD)</th><th scope="col">Auto-approve takes</th><th scope="col">Auto-approve (USD)</th></tr></thead>
        <tbody>
          {#each budget.caps.tokens as c (c.token_id)}
            {@const t = tokenText[c.token_id]}
            {#if t}
              <tr>
                <td>{c.name || tokenName(c.token_id)}</td>
                <td><input type="text" inputmode="decimal" bind:value={t.daily} placeholder="default" aria-label="Daily budget for {c.name}" /></td>
                <td><input type="text" inputmode="numeric" bind:value={t.takes} placeholder="default" aria-label="Auto-approve takes for {c.name}" /></td>
                <td><input type="text" inputmode="decimal" bind:value={t.autoUSD} placeholder="default" aria-label="Auto-approve amount for {c.name}" /></td>
              </tr>
            {/if}
          {/each}
        </tbody>
      </table>
    {/if}
    {#if capsError}<p role="alert" data-testid="caps-error">{capsError}</p>{/if}
    <p><button type="submit" class="primary" disabled={busy} data-testid="save-caps">Save caps</button></p>
  </form>
{/if}

{#if status}
  <section aria-label="Provider" data-testid="provider-section">
    <h2>Provider</h2>
    <p>
      Backend <b>{status.backend}</b> ·
      {status.generation_enabled ? "generation enabled" : `generation disabled${status.disabled_reason ? ` (${status.disabled_reason})` : ""}`} ·
      <b data-testid="armed-state">{status.armed ? "armed" : "disarmed"}</b>{#if status.trip} · <b>tripped</b>{/if}
    </p>
    {#if status.trip}
      <p role="alert" data-testid="open-trip">Open trip: <code>{status.trip.reason}</code> since {status.trip.tripped_at}. Actions: {status.trip.actions.join(", ") || "none"}.</p>
    {/if}
    <label>Reason (required)
      <input type="text" bind:value={reason} maxlength="500" data-testid="provider-reason" />
    </label>
    <p>
      {#if status.armed}
        <button type="button" disabled={busy} onclick={() => providerState({ armed: false })} data-testid="disarm">Disarm</button>
      {:else}
        <button type="button" disabled={busy} onclick={() => providerState({ armed: true })} data-testid="arm">Arm</button>
      {/if}
      {#if status.trip}
        <button type="button" disabled={busy} onclick={() => providerState({ reset_trip: true })} data-testid="reset-trip">Reset trip</button>
      {/if}
    </p>
    <h3>Recent trips</h3>
    {#if status.trips_recent.length === 0}
      <p class="muted">None.</p>
    {:else}
      <ul data-testid="trip-history">
        {#each status.trips_recent as t (t.id)}
          <li><code>{t.reason}</code> at {t.tripped_at}{t.reset_at ? `, reset ${t.reset_at}` : ", open"}</li>
        {/each}
      </ul>
    {/if}
  </section>
{/if}

<style>
  label {
    display: grid;
    gap: 0.2rem;
    margin: 0.5rem 0;
    max-width: 24rem;
  }
  td {
    padding-right: 0.8rem;
  }
</style>
