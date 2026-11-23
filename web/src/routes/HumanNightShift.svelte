<script lang="ts">
  // The secure night-shift settings (update_night_shift is human-only, under /human/api). Enabling is a separate button
  // from saving so that switching the night on is always a deliberate click; it needs a live mandate to spend anything.
  import { api } from "../api/client";
  import { pushToast } from "../lib/judge.svelte";
  import { ATTENDED_NOTICE, OFF_NOTICE, formToPatch, nightStatus, settingsToForm, type NightForm, type NightShift } from "../lib/nightView";

  let ns = $state<NightShift | null>(null);
  let form = $state<NightForm | null>(null);
  let error = $state("");
  let busy = $state(false);

  async function load() {
    try {
      const r = await api.GET("/api/night-shift", {});
      if (r.data) {
        ns = r.data;
        form = settingsToForm(r.data.settings);
        error = "";
      } else error = r.error?.message ?? "Could not load the night shift";
    } catch {
      error = "Could not load the night shift";
    }
  }
  $effect(() => {
    void load();
  });

  async function save(enabled?: boolean) {
    if (busy || !ns || !form) return;
    const res = formToPatch(form, ns.settings, enabled);
    if (!res.ok) return void (error = res.error);
    busy = true;
    try {
      const r = await api.PUT("/human/api/night-shift", { body: res.body });
      if (r.data) {
        pushToast(enabled === true ? "Night shift enabled." : enabled === false ? "Night shift disabled." : "Saved.");
        await load();
      } else error = r.error?.message ?? "Could not save the night shift";
    } catch {
      error = "Could not save the night shift";
    } finally {
      busy = false;
    }
  }
  const status = $derived(ns ? nightStatus(ns) : null);
</script>

<section data-testid="human-night-shift-page">
  <h1>Night shift settings</h1>
  <p><strong>{OFF_NOTICE}</strong></p>
  <p>{ATTENDED_NOTICE}</p>
  {#if error}<p role="alert" data-testid="night-settings-error">{error}</p>{/if}
  {#if ns && form && status}
    <p data-testid="night-settings-status">Now: <strong>{status.headline}</strong>. {status.detail}</p>
    <form onsubmit={(e) => { e.preventDefault(); void save(); }} data-testid="night-settings-form">
      <label>Runs at (24-hour, local time)
        <input type="text" bind:value={form.at} data-testid="night-at" />
      </label>
      <label>Takes per night (1 to 12)
        <input type="text" inputmode="numeric" bind:value={form.maxTakes} data-testid="night-max-takes" />
      </label>
      <label>Explore share (percent, 0 to 50)
        <input type="text" inputmode="decimal" bind:value={form.explorePct} data-testid="night-explore" />
      </label>
      <label>Only lineages rated at least (stars)
        <input type="text" inputmode="numeric" bind:value={form.minStars} data-testid="night-min-stars" />
      </label>
      <label>Listening checks
        <select bind:value={form.listeningChecks} data-testid="night-listening">
          <option value="on">On</option>
          <option value="off">Off</option>
        </select>
      </label>
      <p class="muted">No money is set here: the mandate alone bounds what the night may spend.</p>
      <div class="row">
        <button type="submit" disabled={busy} data-testid="night-save">Save</button>
        {#if ns.settings.enabled && !ns.settings.halted}
          <button type="button" disabled={busy} onclick={() => void save(false)} data-testid="night-disable">Switch the night shift off</button>
        {:else}
          <button type="button" disabled={busy} onclick={() => void save(true)} data-testid="night-enable">{ns.settings.halted ? "Clear the halt and enable" : "Enable the night shift"}</button>
        {/if}
      </div>
    </form>
  {:else if !error}
    <p class="muted" role="status">Loading…</p>
  {/if}
  <p><a href="/night-shift">Back to the night shift</a></p>
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
  .row {
    display: flex;
    flex-wrap: wrap;
    gap: var(--sp-2);
  }
</style>
