<script lang="ts">
  import { api } from "../api/client";
  import CurationToggles from "../lib/components/CurationToggles.svelte";
  import type { CurationSettings } from "../lib/curationView";

  let settings = $state<CurationSettings | null>(null);
  let error = $state("");

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
</script>

<section data-testid="settings-curation-page">
  <h1>Curation settings</h1>
  <p class="muted">
    Two opt-ins decide whether the name of an inspiration (a Muse) may appear outside this app. Both are off unless you turn them on.
    Agents can read them but not change them.
  </p>
  {#if error}<p role="alert">{error}</p>{/if}
  {#if settings}
    <CurationToggles {settings} />
    <p>
      <a class="btn" href="/human/curation" data-testid="curation-change-link">Change these settings</a>
    </p>
  {:else if !error}
    <p class="muted" role="status">Loading…</p>
  {/if}
</section>
