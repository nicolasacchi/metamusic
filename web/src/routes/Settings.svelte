<script lang="ts">
  import { api } from "../api/client";
  import type { components } from "../api/schema";
  import { getDevice, type DeviceProfile } from "../lib/device.svelte";
  import type { Quality } from "../lib/player.svelte";
  import { unpairBrowser } from "../lib/exportView";
  import { pushToast } from "../lib/judge.svelte";

  type System = components["schemas"]["SystemOutBody"];
  type Targets = components["schemas"]["Targets"];

  const device = getDevice();
  let info = $state<System | null>(null);
  let error = $state("");
  let profiles = $state<DeviceProfile[]>([]);
  let profilesError = $state("");
  let kind = $state("");
  let unpairing = $state(false);
  const hasConnection = typeof navigator !== "undefined" && "connection" in navigator;

  $effect(() => {
    void (async () => {
      try {
        const r = await api.GET("/api/system");
        if (r.data) info = r.data;
        else error = "Failed to load system info";
      } catch {
        error = "Failed to load system info";
      }
    })();
  });

  // Songwriting duration targets: every principal reads them, only the operator (a human session) writes.
  let targets = $state<Targets | null>(null);
  let swMin = $state(0);
  let swTargetMin = $state(0);
  let swTargetMax = $state(0);
  let swError = $state("");
  let swSaved = $state(false);
  let swSaving = $state(false);
  function showTargets(t: Targets) {
    targets = t;
    swMin = t.min_duration_s;
    swTargetMin = t.target_min_duration_s;
    swTargetMax = t.target_max_duration_s;
  }
  $effect(() => {
    void (async () => {
      try {
        const r = await api.GET("/api/settings/songwriting");
        if (r.data) showTargets(r.data);
        else swError = "Failed to load the songwriting targets";
      } catch {
        swError = "Failed to load the songwriting targets";
      }
    })();
  });
  async function saveTargets(e: SubmitEvent) {
    e.preventDefault();
    if (swSaving) return;
    swSaving = true;
    swSaved = false;
    swError = "";
    try {
      const r = await api.PUT("/human/api/settings/songwriting", {
        body: { min_duration_s: swMin, target_min_duration_s: swTargetMin, target_max_duration_s: swTargetMax },
      });
      if (r.data) {
        showTargets(r.data);
        swSaved = true;
      } else {
        swError = (r.error as { message?: string } | undefined)?.message ?? "Could not save the songwriting targets";
      }
    } catch {
      swError = "Could not save the songwriting targets";
    } finally {
      swSaving = false;
    }
  }

  // Only a paired device can unpair itself.
  $effect(() => {
    void (async () => {
      try {
        const r = await api.GET("/api/me", {});
        if (r.data) kind = r.data.kind;
      } catch {
        /* the section stays hidden */
      }
    })();
  });
  async function unpair() {
    if (unpairing) return;
    unpairing = true;
    try {
      if (!(await unpairBrowser(api, (u) => location.assign(u)))) pushToast("Could not unpair this browser");
    } catch {
      pushToast("Could not unpair this browser");
    } finally {
      unpairing = false;
    }
  }

  async function refreshProfiles() {
    try {
      profiles = await device.listProfiles();
      profilesError = "";
    } catch {
      profilesError = "Failed to load device profiles";
    }
  }
  $effect(() => {
    void refreshProfiles();
  });

  // Refresh the list once a running probe or quality change has been stored.
  async function test() {
    device.testNow();
    await device.probing;
    await refreshProfiles();
  }
  async function choose(q: Quality) {
    await device.setQuality(q);
    await refreshProfiles();
  }

  const fields: (keyof System)[] = ["version", "commit", "auth_mode", "schema_version", "started_at", "uptime_s"];
  const qualities: { v: Quality; label: string }[] = [
    { v: "lossless", label: "Lossless (FLAC, ≈ 11 MB/min)" },
    { v: "high", label: "High (MP3, ≈ 2.4 MB/min)" },
    ...(hasConnection ? [{ v: "auto" as Quality, label: "Auto (MP3 on cellular or data saver)" }] : []),
  ];
</script>

<h1>Settings</h1>
{#if error}<p role="alert">{error}</p>{/if}
{#if info}
  <dl data-testid="system-info">
    {#each fields as k (k)}
      <dt>{k}</dt>
      <dd>{info[k]}</dd>
    {/each}
  </dl>
{:else if !error}
  <p class="muted">Loading…</p>
{/if}

<p><a href="/settings/backups">Backups</a> · <a href="/settings/audit">Audit log</a></p>

<h2>Songwriting targets</h2>
{#if swError}<p role="alert" data-testid="songwriting-error">{swError}</p>{/if}
{#if targets}
  <div data-testid="songwriting-targets">
    <p class="muted">
      Songs should be longer than the minimum and aim for the target band. Lint warns when the estimated duration misses them, and
      agents see these values.
    </p>
    {#if kind === "human"}
      <form onsubmit={saveTargets}>
        <label>Minimum (s) <input type="number" min="30" max="600" bind:value={swMin} data-testid="sw-min" /></label>
        <label>Target from (s) <input type="number" min="30" max="600" bind:value={swTargetMin} data-testid="sw-target-min" /></label>
        <label>Target to (s) <input type="number" min="30" max="600" bind:value={swTargetMax} data-testid="sw-target-max" /></label>
        <button type="submit" disabled={swSaving} data-testid="sw-save">Save</button>
        {#if swSaved}<span role="status" data-testid="sw-saved">Saved</span>{/if}
      </form>
    {:else}
      <p data-testid="sw-readonly">
        Minimum {targets.min_duration_s} s, target {targets.target_min_duration_s} to {targets.target_max_duration_s} s (set by the operator).
      </p>
    {/if}
  </div>
{/if}

{#if kind === "device"}
  <section data-testid="unpair-section">
    <h2>This browser</h2>
    <p class="muted">This browser is paired as a device. Unpairing signs it out until it is paired again.</p>
    <button type="button" onclick={unpair} disabled={unpairing} data-testid="unpair">Unpair this browser</button>
  </section>
{/if}

<h2>This device</h2>
<div data-testid="device-settings">
  <p>
    FLAC probe: <b data-testid="probe-result">{device.probeResult}</b>
    <button type="button" onclick={test} disabled={device.busy} data-testid="probe-test">Test this device</button>
  </p>
  <fieldset>
    <legend>Playback quality</legend>
    {#each qualities as q (q.v)}
      <label>
        <input type="radio" name="quality" value={q.v} checked={device.quality() === q.v} onchange={() => choose(q.v)} />
        {q.label}
      </label>
    {/each}
    <p class="muted" data-testid="quality-source">Quality: {device.quality()} ({device.qualitySource})</p>
  </fieldset>
</div>

<h2>Device profiles</h2>
{#if profilesError}<p role="alert">{profilesError}</p>{/if}
<table data-testid="device-profiles">
  <thead>
    <tr><th scope="col">Device</th><th scope="col">Quality</th><th scope="col">Probe</th><th scope="col">Format</th></tr>
  </thead>
  <tbody>
    {#each profiles as p (p.device_id)}
      <tr data-device-id={p.device_id} class:current={p.device_id === device.deviceId}>
        <td>{p.label || p.device_id}</td>
        <td>{p.quality} ({p.quality_source})</td>
        <td>{p.probe_result}</td>
        <td>{p.effective_format}</td>
      </tr>
    {/each}
  </tbody>
</table>

<style>
  fieldset label,
  form label {
    display: block;
    margin: 0.25rem 0;
  }
  td,
  th {
    text-align: left;
    padding: 0.25rem 0.75rem 0.25rem 0;
  }
  tr.current {
    font-weight: 700;
  }
</style>
