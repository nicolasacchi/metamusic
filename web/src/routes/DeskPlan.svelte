<script lang="ts">
  // /desk/plans/:id (DESK-W1, C363.2): one plan as notation with its sections, a bar editor and the verdict buttons (Owner).
  // Listening to the plan and rendering it are DESK-W2's. Reads get_plan; edits go through edit_plan and open the new plan.
  import { api as defaultApi } from "../api/client";
  import ScoreView from "../lib/components/ScoreView.svelte";
  import BarEditor from "../lib/components/BarEditor.svelte";
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
  }
  let { id, navigate, client = defaultApi, initial, mayEdit = can("edit_plan"), mayJudge = can("set_plan_verdict"), load }: Props = $props();

  // svelte-ignore state_referenced_locally
  let plan = $state<Plan | null>(initial ?? null);
  let failure = $state<DeskFailure | null>(null);
  let selected = $state<BarRef | null>(null);
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

  <ScoreView {plan} {selected} onselect={(b) => (selected = b)} {load} />

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
