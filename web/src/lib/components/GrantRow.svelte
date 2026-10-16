<script lang="ts">
  import { api } from "../../api/client";
  import type { components } from "../../api/schema";
  import { grantToForm, saveGrant, toggleScope } from "../grantForm";
  import { pushToast } from "../judge.svelte";

  type Grant = components["schemas"]["ConnectorGrant"];
  interface Props {
    grant: Grant;
    onchanged: () => void;
  }
  let { grant, onchanged }: Props = $props();

  // The parent keys rows by id + updated_at, so a changed grant remounts this row.
  // svelte-ignore state_referenced_locally
  let f = $state(grantToForm(grant));
  let busy = $state(false);
  let error = $state("");

  async function save() {
    if (busy) return;
    busy = true;
    error = "";
    const r = await saveGrant(
      async (id, body) => {
        const x = await api.PUT("/api/connectors/{id}", { params: { path: { id } }, body });
        return { data: x.data, error: x.error };
      },
      grant.id,
      f,
    );
    busy = false;
    if (r.ok) {
      f = grantToForm(r.grant);
      onchanged();
    } else error = r.error;
  }

  async function setRevoked(revoked: boolean) {
    if (busy) return;
    if (revoked && !confirm(`Revoke "${grant.label}"? Its next request is refused.`)) return;
    busy = true;
    try {
      const r = await api.PUT("/api/connectors/{id}", { params: { path: { id: grant.id } }, body: { revoked } });
      if (!r.data) pushToast(r.error?.message ?? "Could not change the grant");
    } catch {
      pushToast("Could not change the grant");
    }
    busy = false;
    onchanged();
  }
</script>

<tr data-revoked={grant.revoked_at !== null} data-testid="grant-row">
  <td>{grant.label}{#if grant.username}<br /><span class="muted">{grant.username}</span>{/if}</td>
  <td>
    <label><input type="checkbox" checked disabled /> read</label>
    <label><input type="checkbox" checked={f.write} onchange={() => (f = toggleScope(f, "write"))} data-testid="grant-write" /> write</label>
    <label><input type="checkbox" checked={f.generate} onchange={() => (f = toggleScope(f, "generate"))} data-testid="grant-generate" /> generate</label>
  </td>
  <td>
    <label>Daily $ <input bind:value={f.dailyUsd} inputmode="decimal" placeholder={grant.effective.daily_budget_usd} size="6" data-testid="grant-daily" /></label>
    <label>Auto-approve $ <input bind:value={f.autoApproveUsd} inputmode="decimal" placeholder={grant.effective.auto_approve_usd} size="6" data-testid="grant-auto" /></label>
    <label>Takes <input bind:value={f.autoApproveTakes} inputmode="numeric" placeholder={String(grant.effective.auto_approve_takes)} size="3" data-testid="grant-takes" /></label>
  </td>
  <td>{grant.last_used_at ?? "never"}</td>
  <td>{grant.revoked_at ? `revoked ${grant.revoked_at}` : "active"}</td>
  <td>
    <button type="button" disabled={busy} onclick={save} data-testid="grant-save">Save</button>
    {#if grant.revoked_at}
      <button type="button" disabled={busy} onclick={() => setRevoked(false)} data-testid="grant-restore">Restore</button>
    {:else}
      <button type="button" disabled={busy} onclick={() => setRevoked(true)} data-testid="grant-revoke">Revoke</button>
    {/if}
    {#if error}<span role="alert">{error}</span>{/if}
  </td>
</tr>
