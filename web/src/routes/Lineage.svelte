<script lang="ts">
  // /lineage/:id (C84.5): get_lineage rendered pre-order; beyond the 200 default, "show more" asks for 500.
  import { api } from "../api/client";
  import type { components } from "../api/schema";
  import LineageTree from "../lib/components/LineageTree.svelte";
  import type { Viewer } from "../lib/genIntent";
  import { fetchViewer } from "../lib/viewer";

  type Tree = components["schemas"]["LineageTree"];
  interface Props {
    id: string;
    /** Pre-loaded tree (server-side rendering in unit tests). */
    initial?: Tree;
    me?: Viewer | null;
  }
  let { id, initial, me: meInit = null }: Props = $props();

  // svelte-ignore state_referenced_locally
  let tree = $state<Tree | null>(initial ?? null);
  // svelte-ignore state_referenced_locally
  let me = $state<Viewer | null>(meInit);
  let error = $state("");
  let loading = $state(false);
  let big = false;

  async function load(mine: string, limit: number) {
    loading = true;
    try {
      const r = await api.GET("/api/tracks/{id}/lineage", { params: { path: { id: mine }, query: { limit } } });
      if (mine !== id) return;
      if (r.data) {
        tree = r.data;
        error = "";
      } else error = r.response.status === 404 ? "Track not found" : (r.error?.message ?? "Failed to load the lineage");
    } catch {
      if (mine === id) error = "Failed to load the lineage";
    } finally {
      if (mine === id) loading = false;
    }
  }

  $effect(() => {
    const mine = id;
    if (initial && initial.nodes.some((n) => n.id === mine)) return;
    tree = null;
    error = "";
    big = false;
    void load(mine, 200);
    void fetchViewer().then((v) => {
      if (mine === id && v) me = v;
    });
  });

  function more() {
    big = true;
    void load(id, 500);
  }
  const canMore = $derived(!!tree && tree.truncated && !big && tree.nodes.length < 500);
</script>

<h1>Lineage</h1>
{#if error}
  <p role="alert">{error}</p>
{:else if !tree}
  <p class="muted">Loading…</p>
{:else}
  <p class="muted" data-testid="lineage-count">
    {tree.nodes.length} of {tree.total} track{tree.total === 1 ? "" : "s"}
    {#if tree.truncated}<span class="chip" data-testid="lineage-truncated">truncated</span>{/if}
    · <a href="/tracks/{id}">Back to the track</a>
  </p>
  <LineageTree nodes={tree.nodes} currentId={id} {me} />
  {#if canMore}
    <p><button type="button" onclick={more} disabled={loading} data-testid="lineage-more">Show more</button></p>
  {/if}
{/if}

<style>
  .chip {
    font-size: 0.75rem;
    padding: 0 0.5rem;
    border-radius: 0.6rem;
    border: 1px solid #e0a030;
    color: #e0a030;
  }
</style>
