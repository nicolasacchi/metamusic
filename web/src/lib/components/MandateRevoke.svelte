<script lang="ts">
  // Revoke one mandate with a reason (revoke_mandate, Owner only). Takes effect at once: a night in flight stops using it.
  import { api } from "../../api/client";
  import type { Mandate } from "../mandateView";
  import { reasonError } from "../mandateView";
  import { pushToast } from "../judge.svelte";

  interface Props {
    mandate: Pick<Mandate, "id" | "title">;
    onrevoked: (m: Mandate) => void;
  }
  let { mandate, onrevoked }: Props = $props();
  let open = $state(false);
  let reason = $state("");
  let error = $state("");
  let busy = $state(false);

  async function revoke(e: Event) {
    e.preventDefault();
    if (busy) return;
    const bad = reasonError(reason);
    if (bad) return void (error = bad);
    busy = true;
    try {
      const r = await api.POST("/api/mandates/{id}/revoke", { params: { path: { id: mandate.id } }, body: { reason: reason.trim() } });
      if (r.data) {
        pushToast("Mandate revoked.");
        open = false;
        reason = "";
        error = "";
        onrevoked(r.data);
      } else error = r.error?.message ?? "Could not revoke the mandate";
    } catch {
      error = "Could not revoke the mandate";
    } finally {
      busy = false;
    }
  }
</script>

{#if !open}
  <button type="button" onclick={() => (open = true)} data-testid="mandate-revoke-open">Revoke</button>
{:else}
  <form onsubmit={revoke} data-testid="mandate-revoke-form">
    <label>Why revoke "{mandate.title}"?
      <input type="text" bind:value={reason} maxlength="500" data-testid="mandate-revoke-reason" />
    </label>
    {#if error}<p role="alert" data-testid="mandate-revoke-error">{error}</p>{/if}
    <button type="submit" disabled={busy} data-testid="mandate-revoke-confirm">Revoke now</button>
    <button type="button" onclick={() => { open = false; error = ""; }}>Keep it</button>
  </form>
{/if}

<style>
  form {
    display: flex;
    flex-wrap: wrap;
    gap: var(--sp-2);
    align-items: end;
  }
  label {
    display: grid;
    gap: var(--sp-1);
    font-size: var(--fs-sm);
    flex: 1 1 14rem;
  }
</style>
