<script lang="ts">
  import { api } from "../../api/client";
  import type { components } from "../../api/schema";

  type Link = components["schemas"]["TrackDownloadLink"];
  interface Props {
    trackId: string;
    lossy?: boolean;
  }
  let { trackId, lossy = false }: Props = $props();

  let link = $state<Link | null>(null);
  let error = $state("");

  async function fetchLink(id: string): Promise<Link | null> {
    try {
      const r = await api.GET("/api/tracks/{id}/download", { params: { path: { id } } });
      if (!r.data) error = r.error?.message || "Could not prepare the download";
      return r.data ?? null;
    } catch {
      error = "Could not prepare the download";
      return null;
    }
  }

  $effect(() => {
    const id = trackId;
    link = null;
    error = "";
    void fetchLink(id).then((l) => {
      if (id === trackId) link = l;
    });
  });

  /** A link about to expire is replaced first; the download itself is a plain navigation. */
  async function onclick(e: MouseEvent) {
    if (!link || Date.parse(link.expires_at) - Date.now() > 30_000) return;
    e.preventDefault();
    const fresh = await fetchLink(trackId);
    if (!fresh) return;
    link = fresh;
    const a = document.createElement("a");
    a.href = fresh.url;
    a.download = fresh.filename;
    a.click();
  }
</script>

{#if link}
  <a class="dl" data-testid="download-flac" href={link.url} download={link.filename} {onclick}>{lossy ? "Download MP3" : "Download FLAC"}</a>
{:else if error}
  <span role="alert" class="muted">{error}</span>
{/if}

<style>
  .dl {
    display: inline-block;
    padding: 0.2rem 0.7rem;
    border: 1px solid currentColor;
    border-radius: 0.3rem;
    text-decoration: none;
  }
  @media (pointer: coarse) {
    .dl {
      display: inline-flex;
      align-items: center;
      min-height: var(--hit);
    }
  }
</style>
