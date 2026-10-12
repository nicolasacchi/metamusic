<script lang="ts">
  import { api } from "../api/client";
  import type { components } from "../api/schema";
  import { getDevice, type DeviceProfile } from "../lib/device.svelte";
  import type { Quality } from "../lib/player.svelte";

  type System = components["schemas"]["SystemOutBody"];

  const device = getDevice();
  let info = $state<System | null>(null);
  let error = $state("");
  let profiles = $state<DeviceProfile[]>([]);
  let profilesError = $state("");
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
  fieldset label {
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
