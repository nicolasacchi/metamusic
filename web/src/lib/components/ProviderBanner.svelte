<script lang="ts">
  import type { components } from "../../api/schema";
  import { can } from "../me.svelte";

  type Brief = components["schemas"]["ProviderBrief"];
  interface Props {
    provider: Brief | null;
  }
  let { provider }: Props = $props();

  /** Why generation cannot run, or null when it can. */
  const problem = $derived.by(() => {
    if (!provider) return null;
    if (!provider.generation_enabled) return `Generation is disabled${provider.disabled_reason ? ` (${provider.disabled_reason})` : ""}.`;
    // an Editor's brief has no armed or tripped flag (C310.5): absent is unknown, never "disarmed"
    if (provider.tripped === true) return "The provider is tripped: new jobs are held until a human resets it.";
    if (provider.armed === false) return "The provider is disarmed: new jobs are held until a human arms it.";
    return null;
  });
</script>

{#if problem}
  <div class="banner" role="alert" data-testid="provider-banner">
    {problem}{#if can("get_spend")} <a href="/human/spend">Spend and provider settings</a>{/if}
  </div>
{/if}

<style>
  .banner {
    border: 1px solid #a02a1f;
    background: #3a1c18;
    border-radius: 0.4rem;
    padding: 0.5rem 0.8rem;
    margin: 0.5rem 0;
  }
</style>
