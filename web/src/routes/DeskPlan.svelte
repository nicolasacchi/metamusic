<script lang="ts">
  // /desk/plans/:id (C363.2): one plan as notation with its sections, a bar editor and the verdict buttons (Owner, DESK-W1);
  // the audition player, Render and Keep as Take (DESK-W2). Reads get_plan; edits go through edit_plan and open the new plan.
  // Render goes through the ordinary estimate and ConfirmSpend page (/human/generate): the Owner approves the spend, an Editor
  // asks and waits for the Owner; nothing is created from here.
  import { api as defaultApi } from "../api/client";
  import ScoreView from "../lib/components/ScoreView.svelte";
  import BarEditor from "../lib/components/BarEditor.svelte";
  import AuditionPlayer from "../lib/components/AuditionPlayer.svelte";
  import KeepAsTake from "../lib/components/KeepAsTake.svelte";
  import { renderGate, renderIntent, renderRows, RENDER_SEEDS_MAX } from "../lib/deskRender";
  import { startGeneration } from "../lib/genIntent";
  import { me } from "../lib/me.svelte";
  import type { AbcLoader, BarRef } from "../lib/abcRender";
  import { getPlan, setVerdict, type DeskFailure, type Plan } from "../lib/deskApi";
  import { criticVerdictOf, deskPlanHref, editRationaleOf, ownerVerdictOf, planFacts, planLabel, verdictText } from "../lib/deskView";
  import { can } from "../lib/me.svelte";

  interface Props {
    id: string;
    navigate?: (to: string, replace?: boolean) => void;
    client?: typeof defaultApi;
    /** Pre-loaded plan and capabilities (unit tests). */
    initial?: Plan;
    mayEdit?: boolean;
    mayJudge?: boolean;
    load?: AbcLoader;
    /** Unit tests: the audition and keep panels need the transform capabilities, which they would otherwise fetch. */
    listen?: boolean;
    mayKeep?: boolean;
    allowed?: readonly string[];
  }
  let {
    id,
    navigate,
    client = defaultApi,
    initial,
    mayEdit = can("edit_plan"),
    mayJudge = can("set_plan_verdict"),
    load,
    listen = can("create_plan_audition"),
    mayKeep = can("create_transform"),
    allowed = me.allowed,
  }: Props = $props();

  // svelte-ignore state_referenced_locally
  let plan = $state<Plan | null>(initial ?? null);
  let failure = $state<DeskFailure | null>(null);
  let selected = $state<BarRef | null>(null);
  let sounding = $state<BarRef | null>(null);
  let seeds = $state(1);
  let verdictBusy = $state(false);
  let verdictError = $state("");
  let notice = $state("");

  function go(to: string) {
    if (navigate) navigate(to);
    else {
      history.pushState(null, "", to);
      window.dispatchEvent(new PopStateEvent("popstate"));
    }
  }
  async function reload() {
    const r = await getPlan(client, id);
    if (r.ok) {
      plan = r.value;
      failure = null;
    } else failure = r;
  }
  $effect(() => {
    if (initial === undefined) void reload();
  });

  async function judge(v: "liked" | "rejected" | null) {
    if (!plan || verdictBusy) return;
    verdictBusy = true;
    verdictError = "";
    const r = await setVerdict(client, plan.id, v);
    verdictBusy = false;
    if (r.ok) plan = { ...plan, verdict: r.value.verdict, critic_verdict: r.value.critic_verdict, verdicts: r.value.verdicts };
    else verdictError = r.message;
  }
  function edited(p: Plan, created: boolean) {
    notice = created ? "" : "That edit already exists; here it is.";
    go(deskPlanHref(p.id));
  }
  const gate = $derived(renderGate(allowed));
  const rows = $derived(plan ? renderRows(plan.renders) : []);
  function render() {
    if (!plan) return;
    startGeneration(sessionStorage, renderIntent([plan.id], seeds), go);
  }
  const owner = $derived(plan ? ownerVerdictOf(plan) : null);
  const critic = $derived(plan ? criticVerdictOf(plan) : null);
  const rationale = $derived(plan ? editRationaleOf(plan) : null);
</script>

<p class="crumb"><a href="/desk">← Desk</a></p>
{#if failure}
  <p class="err" role="alert" data-testid="plan-error">{failure.status === 404 ? "That plan does not exist." : failure.message}</p>
{:else if !plan}
  <p class="muted" data-testid="plan-loading">Loading…</p>
{:else}
  <h1 data-testid="plan-title">{plan.title || plan.base.title}</h1>
  <p class="muted" data-testid="plan-facts">
    {planLabel(plan)} · {planFacts(plan)}
    {#if plan.q} · tempo {plan.q}{/if}
    {#if plan.base.track_id} · for <a href="/tracks/{plan.base.track_id}">{plan.base.title || "the take"}</a>{/if}
    {#if plan.parent_plan_id} · edited from <a href={deskPlanHref(plan.parent_plan_id)} data-testid="plan-parent">another plan</a>{/if}
  </p>
  {#if notice}<p class="muted" role="status">{notice}</p>{/if}
  {#if rationale}<p class="muted" data-testid="plan-rationale">Why: {rationale}</p>{/if}

  <ScoreView {plan} {selected} playing={sounding} onselect={(b) => (selected = b)} {load} />

  {#if listen}
    <AuditionPlayer {plan} {client} onbar={(b) => (sounding = b)} />
  {/if}

  {#if gate.kind === "ready"}
    <section class="card" aria-labelledby="plan-render-h" data-testid="plan-render">
      <h2 id="plan-render-h">Render</h2>
      <p class="muted">
        Make a real take from this score on the GPU. You see the price first; nothing is spent until the Owner approves it{#if !can("create_generation_human")}, so your request waits for them{/if}.
      </p>
      <div class="row">
        <label>Performances
          <select value={seeds} onchange={(e) => (seeds = Number(e.currentTarget.value))} data-testid="render-seeds">
            {#each Array.from({ length: RENDER_SEEDS_MAX }, (_, k) => k + 1) as n (n)}<option value={n}>{n === 1 ? "1 (this plan's seed)" : `${n} (same score, new performance)`}</option>{/each}
          </select>
        </label>
        <button type="button" class="primary" onclick={render} data-testid="render-plan">Price a render…</button>
      </div>
      {#if rows.length > 0}
        <ul class="renders" data-testid="plan-renders">
          {#each rows as r (r.key)}
            <li data-testid="plan-render-row">
              {r.text}
              {#if r.trackHref}· <a href={r.trackHref}>listen</a>{/if}
              · <a href={r.batchHref}>batch</a>
            </li>
          {/each}
        </ul>
      {/if}
    </section>
  {/if}

  {#if mayKeep}
    <KeepAsTake {plan} {client} />
  {/if}

  {#if (plan.sections ?? []).length > 0}
    <section class="card" aria-labelledby="plan-sections-h">
      <h2 id="plan-sections-h">Sections</h2>
      <ul class="sections" data-testid="plan-sections">
        {#each plan.sections ?? [] as s (s.idx)}
          <li><b>{s.label || s.kind}</b> <span class="muted">bars {s.start_bar + 1}–{s.start_bar + s.bars}</span></li>
        {/each}
      </ul>
    </section>
  {/if}

  {#if mayJudge}
    <section class="card" aria-labelledby="plan-verdict-h" data-testid="plan-verdict">
      <h2 id="plan-verdict-h">Your verdict</h2>
      <p data-testid="plan-verdict-state" data-verdict={owner ?? ""}>{verdictText(owner)}</p>
      <div class="row">
        <button type="button" aria-pressed={owner === "liked"} disabled={verdictBusy} onclick={() => void judge(owner === "liked" ? null : "liked")} data-testid="verdict-like">Like</button>
        <button type="button" aria-pressed={owner === "rejected"} disabled={verdictBusy} onclick={() => void judge(owner === "rejected" ? null : "rejected")} data-testid="verdict-reject">Reject</button>
      </div>
      {#if verdictError}<p class="err" role="alert">{verdictError}</p>{/if}
    </section>
  {:else if owner}
    <p class="muted" data-testid="plan-verdict-state" data-verdict={owner}>Owner's verdict: {verdictText(owner)}</p>
  {/if}
  {#if critic}<p class="muted" data-testid="plan-critic">Critic's verdict: {verdictText(critic)}</p>{/if}

  {#if mayEdit}
    <BarEditor {plan} {selected} onedited={edited} {client} />
  {/if}

  {#if (plan.children ?? []).length > 0}
    <section class="card" aria-labelledby="plan-children-h">
      <h2 id="plan-children-h">Edited from this plan</h2>
      <ul data-testid="plan-children">
        {#each plan.children ?? [] as c (c.plan_id)}<li><a href={deskPlanHref(c.plan_id)}>An edit</a></li>{/each}
      </ul>
    </section>
  {/if}

  <details class="card">
    <summary>Score text</summary>
    <pre data-testid="plan-abc">{plan.abc ?? ""}</pre>
  </details>
{/if}

<style>
  .crumb {
    margin: 0;
  }
  .sections {
    list-style: none;
    padding: 0;
    margin: 0;
    display: flex;
    flex-wrap: wrap;
    gap: var(--sp-3);
  }
  .row {
    display: flex;
    gap: var(--sp-2);
  }
  .row button {
    min-height: var(--hit);
    flex: 1 1 auto;
  }
  .row label {
    display: flex;
    flex-direction: column;
    gap: var(--sp-1);
    font-size: var(--fs-sm);
  }
  .row select {
    min-height: var(--hit);
  }
  .primary {
    background: var(--accent);
    color: var(--accent-ink);
    border: none;
    border-radius: var(--r-md);
    font-weight: var(--fw-bold);
  }
  .renders {
    list-style: none;
    padding: 0;
    margin: var(--sp-3) 0 0;
  }
  .renders li {
    min-height: var(--hit);
    display: flex;
    align-items: center;
    gap: var(--sp-2);
  }
  .row button[aria-pressed="true"] {
    background: var(--accent);
    color: var(--accent-ink);
    border-color: var(--accent);
  }
  pre {
    overflow-x: auto;
    max-width: 100%;
    white-space: pre;
  }
  summary {
    display: flex;
    align-items: center;
    min-height: var(--hit);
    cursor: pointer;
  }
  section,
  details {
    margin-top: var(--sp-3);
  }
</style>
