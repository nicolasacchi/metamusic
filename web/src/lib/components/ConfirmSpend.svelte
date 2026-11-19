<script lang="ts">
  import { api } from "../../api/client";
  import { confirmButtonLabel, confirmDisabledReasons, confirmViaFor, createConfirmAction, describeConfirmFailure, type ConfirmOutcome, type EstimateLike } from "../confirm";
  import { can } from "../me.svelte";

  interface Props {
    estimate: EstimateLike;
    /** Called with the batch to open (created, or the one a consumed token already made). */
    onbatch: (id: string) => void;
    onreestimate: () => void;
  }
  let { estimate, onbatch, onreestimate }: Props = $props();

  // The Owner confirms and the batch starts; an Editor's confirmation is a request the Owner approves (C310.1).
  const via = $derived(confirmViaFor(can));
  let rationale = $state("");
  let busy = $state(false);
  let failure = $state<Extract<ConfirmOutcome, { kind: "failed" }> | null>(null);
  let networkError = $state(false);
  // One action (and so one Idempotency-Key) per click; a retry after a network error reuses it.
  let action: (() => Promise<ConfirmOutcome>) | null = null;

  const blocked = $derived(confirmDisabledReasons(estimate).length > 0);
  const view = $derived(failure ? describeConfirmFailure(failure) : null);

  async function click() {
    if (busy || blocked) return;
    if (!action || !networkError) action = createConfirmAction(api, estimate.confirmation_token, rationale, via);
    busy = true;
    failure = null;
    networkError = false;
    try {
      const r = await action();
      if (r.kind === "created" || r.kind === "existing") onbatch(r.batchId);
      else if (r.kind === "network") networkError = true;
      else failure = r;
    } finally {
      busy = false;
    }
  }
</script>

<section data-testid="confirm-spend">
  <label>
    Why (optional)
    <input type="text" bind:value={rationale} maxlength="500" disabled={busy || networkError} data-testid="rationale" />
  </label>
  {#if via === "request"}<p class="muted" data-testid="request-note">Nothing runs or is spent until the owner approves this request.</p>{/if}
  <p>
    <button type="button" class="primary" disabled={busy || blocked} onclick={click} data-testid="confirm-button">
      {networkError ? "Retry: " : ""}{confirmButtonLabel(estimate, via)}
    </button>
  </p>
  {#if networkError}<p role="alert" data-testid="confirm-network">Network error. Your request may have gone through: retrying is safe, it cannot spend twice.</p>{/if}
  {#if view}
    <p role="alert" data-testid="confirm-failure" data-code={failure?.code}>
      {view.text}
      {#if view.reestimate}<button type="button" onclick={onreestimate} data-testid="reestimate">Re-estimate</button>{/if}
    </p>
  {/if}
</section>
