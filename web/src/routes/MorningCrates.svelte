<script lang="ts">
  // The Morning Crates: the list, "Build one now" and the settings (M6N-W2, C384.6). The crate itself is blind until the
  // Owner has rated it; this page only ever shows counts, lengths and states.
  import { untrack } from "svelte";
  import { api } from "../api/client";
  import { idempotentAction } from "../api/idem";
  import { can } from "../lib/me.svelte";
  import { pushToast } from "../lib/judge.svelte";
  import {
    SETTING_RANGES,
    crateLine,
    crateName,
    formToPatch,
    settingsToForm,
    type MorningCrateSummary,
    type MorningSettings,
    type SettingsForm,
  } from "../lib/morningCrate";

  let items = $state<MorningCrateSummary[]>([]);
  let cursor = $state<string | null>(null);
  let loaded = $state(false);
  let error = $state("");
  let more = $state(false);
  let building = $state(false);
  let buildError = $state("");

  let settings = $state<MorningSettings | null>(null);
  let form = $state<SettingsForm | null>(null);
  let settingsError = $state("");
  let saving = $state(false);

  async function load(next?: string) {
    more = !!next;
    try {
      const r = await api.GET("/api/morning-crates", { params: { query: { limit: 30, ...(next ? { cursor: next } : {}) } } });
      if (r.data) {
        items = next ? [...items, ...r.data.items] : r.data.items;
        cursor = r.data.next_cursor ?? null;
        error = "";
      } else error = r.error?.message ?? "Could not load the Morning Crates";
    } catch {
      error = "Could not load the Morning Crates";
    } finally {
      loaded = true;
      more = false;
    }
  }

  async function loadSettings() {
    if (!can("get_morning_crate_settings")) return;
    try {
      const r = await api.GET("/api/morning-crates/settings", {});
      if (r.data) {
        settings = r.data;
        form = settingsToForm(r.data);
      }
    } catch {
      settingsError = "Could not load the settings";
    }
  }

  $effect(() => {
    untrack(() => {
      void load();
      void loadSettings();
    });
  });

  // a crate that is still being built is polled until it is done
  $effect(() => {
    if (!items.some((c) => c.state === "building")) return;
    const h = setInterval(() => void load(), 3000);
    return () => clearInterval(h);
  });

  let buildKey = idempotentAction(async (key: string) =>
    api.POST("/api/morning-crates", { params: { header: { "Idempotency-Key": key } }, body: {} }),
  );

  async function build() {
    if (building) return;
    building = true;
    buildError = "";
    try {
      const r = await buildKey();
      if (r.data) {
        buildKey = idempotentAction(async (key: string) => api.POST("/api/morning-crates", { params: { header: { "Idempotency-Key": key } }, body: {} }));
        pushToast("Building a Morning Crate from the takes nobody has judged yet.");
        await load();
      } else buildError = r.error?.message ?? "Could not start the build";
    } catch {
      buildError = "Could not start the build";
    } finally {
      building = false;
    }
  }

  async function save(e: Event) {
    e.preventDefault();
    if (saving || !settings || !form) return;
    const res = formToPatch(form, settings);
    if (!res.ok) return void (settingsError = res.error);
    saving = true;
    try {
      const r = await api.PUT("/api/morning-crates/settings", { body: res.body });
      if (r.data) {
        settings = r.data;
        form = settingsToForm(r.data);
        settingsError = "";
        pushToast("Morning Crate settings saved.");
      } else settingsError = r.error?.message ?? "Could not save the settings";
    } catch {
      settingsError = "Could not save the settings";
    } finally {
      saving = false;
    }
  }
</script>

<section data-testid="morning-page">
  <h1>Morning crates</h1>
  <p class="muted">
    A listening tape of the takes nobody has judged yet, built every morning. It is blind: no title, take or lineage is shown until you have rated every
    chapter or revealed it. Rate with the stars, or with the headset: previous marks, twice keeps, next skips.
  </p>
  {#if can("build_morning_crate")}
    <p>
      <button type="button" class="btn" disabled={building} onclick={() => void build()} data-testid="morning-build">Build one now</button>
    </p>
    {#if buildError}<p role="alert" data-testid="morning-build-error">{buildError}</p>{/if}
  {/if}
  {#if error}<p role="alert" data-testid="morning-error">{error}</p>{/if}
  {#if !loaded}
    <p class="muted" role="status">Loading…</p>
  {:else if items.length === 0 && !error}
    <p class="muted" data-testid="morning-empty">No Morning Crate yet. The first one is built at the time below, or press "Build one now".</p>
  {:else}
    <ul class="list">
      {#each items as c (c.id)}
        <li data-testid="morning-row" data-state={c.state} data-revealed={c.revealed}>
          <div class="head">
            {#if c.state === "ready"}<a href="/morning/{c.id}" class="title">{crateName(c)}</a>{:else}<span class="title">{crateName(c)}</span>{/if}
            <span class="chip {c.state}">{c.state}</span>
          </div>
          <p class="muted">{crateLine(c)} {c.trigger === "manual" ? "Built on request." : ""}</p>
        </li>
      {/each}
    </ul>
    {#if cursor}<button type="button" disabled={more} onclick={() => void load(cursor ?? undefined)}>Show more</button>{/if}
  {/if}

  {#if settings && form}
    <h2>Settings</h2>
    <form onsubmit={save} data-testid="morning-settings">
      <label class="row"><input type="checkbox" bind:checked={form.enabled} data-testid="morning-enabled" /> Build a crate every day</label>
      <label>Built at (24-hour, local time)
        <input type="text" bind:value={form.at} inputmode="numeric" data-testid="morning-at" />
      </label>
      {#each Object.entries(SETTING_RANGES) as [k, r] (k)}
        <label>{r.label} ({r.lo} to {r.hi})
          <input type="text" inputmode="numeric" bind:value={form[k as keyof typeof SETTING_RANGES]} data-testid="morning-set-{k}" />
        </label>
      {/each}
      <label class="row"><input type="checkbox" bind:checked={form.include_truncated} data-testid="morning-truncated" /> Include takes that were cut short</label>
      {#if settingsError}<p role="alert" data-testid="morning-settings-error">{settingsError}</p>{/if}
      <button type="submit" class="btn" disabled={saving} data-testid="morning-settings-save">Save settings</button>
      <p class="muted">Last changed by {settings.updated_by.name}. Settings hold no money: they only change how the tape is built.</p>
    </form>
  {/if}
</section>

<style>
  .list {
    list-style: none;
    margin: 0 0 var(--sp-4);
    padding: 0;
    display: grid;
    gap: var(--sp-3);
  }
  li {
    padding: var(--sp-3) var(--sp-4);
    background: var(--surface);
    border: 1px solid var(--line);
    border-radius: var(--r-md);
  }
  .head {
    display: flex;
    justify-content: space-between;
    gap: var(--sp-3);
    align-items: center;
  }
  .title {
    font-weight: var(--fw-medium);
  }
  p {
    margin: var(--sp-1) 0;
  }
  .chip {
    padding: 0 var(--sp-2);
    border-radius: var(--r-pill);
    font-size: var(--fs-xs);
    background: var(--surface-2);
    color: var(--text-2);
  }
  .chip.ready {
    background: color-mix(in srgb, var(--ok) 25%, transparent);
    color: var(--ok);
  }
  .chip.failed {
    background: var(--bad-soft);
    color: var(--bad);
  }
  form {
    display: grid;
    gap: var(--sp-3);
    max-width: 28rem;
  }
  label {
    display: grid;
    gap: var(--sp-1);
  }
  label.row {
    display: flex;
    align-items: center;
    gap: var(--sp-2);
    min-height: var(--hit);
  }
  .btn {
    min-height: var(--hit);
    padding: 0 var(--sp-4);
    border: 1px solid var(--accent);
    border-radius: var(--r-pill);
    background: transparent;
    color: var(--accent);
  }
</style>
