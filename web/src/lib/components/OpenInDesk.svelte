<script lang="ts">
  // "Open in the Desk" on a Take that carries a score (DESK-W1, C363.2): create_plan_from_take, then the plan page. One plan
  // per take, so a repeat just opens the existing one. A refusal (not a YuE2 score, a lossy import, ...) is said in words.
  import { api as defaultApi } from "../../api/client";
  import { openTakeInDesk, type DeskFailure } from "../deskApi";
  import { deskPlanHref, openRefusalText } from "../deskView";

  interface Props {
    trackId: string;
    go: (to: string) => void;
    client?: typeof defaultApi;
  }
  let { trackId, go, client = defaultApi }: Props = $props();

  let busy = $state(false);
  let refusal = $state<DeskFailure | null>(null);

  async function open() {
    if (busy) return;
    busy = true;
    refusal = null;
    const r = await openTakeInDesk(client, trackId)();
    busy = false;
    if (r.ok) go(deskPlanHref(r.value.plan.id));
    else refusal = r;
  }
</script>

<section class="card" data-testid="open-in-desk">
  <h2>Composer's Desk</h2>
  <p class="muted">Read this take's score as notation and edit its bars. It makes a plan, not a take, and costs nothing.</p>
  <button type="button" disabled={busy} onclick={() => void open()} data-testid="open-in-desk-button">{busy ? "Opening…" : "Open in the Desk"}</button>
  {#if refusal}<p class="err" role="alert" data-testid="open-in-desk-error">{openRefusalText(refusal)}</p>{/if}
</section>

<style>
  button {
    min-height: var(--hit);
  }
</style>
