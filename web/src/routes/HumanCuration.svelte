<script lang="ts">
  import { api } from "../api/client";
  import CurationToggles from "../lib/components/CurationToggles.svelte";
  import { patchFor, type CurationKey, type CurationSettings } from "../lib/curationView";
  import { pushToast } from "../lib/judge.svelte";

  let settings = $state<CurationSettings | null>(null);
  let error = $state("");
  let busy = $state(false);

  async function load() {
    try {
      const r = await api.GET("/api/curation/settings", {});
      if (r.data) {
        settings = r.data;
        error = "";
      } else error = r.error?.message ?? "Could not load the curation settings";
    } catch {
      error = "Could not load the curation settings";
    }
  }
  $effect(() => {
    void load();
  });

  async function change(key: CurationKey, value: boolean) {
    if (busy) return;
    busy = true;
    try {
      const r = await api.PUT("/human/api/curation/settings", { body: patchFor(key, value) });
      if (r.data) {
        settings = r.data;
        error = "";
        pushToast("Saved.");
      } else error = r.error?.message ?? "Could not save the setting";
    } catch {
      error = "Could not save the setting";
    } finally {
      busy = false;
    }
  }
</script>

<section data-testid="human-curation-page">
  <h1>Curation</h1>
  <p class="muted">Both opt-ins are off by default. Only you can change them; each change is recorded.</p>
  {#if error}<p role="alert" data-testid="curation-error">{error}</p>{/if}
  {#if settings}
    <CurationToggles {settings} {busy} onchange={(k, v) => void change(k, v)} />
  {:else if !error}
    <p class="muted" role="status">Loading…</p>
  {/if}
  <p><a href="/settings/curation">Back to the curation settings</a></p>
</section>
