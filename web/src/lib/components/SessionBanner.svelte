<script lang="ts">
  import { session } from "../../api/session";
  const banner = session.banner;
</script>

{#if $banner === "expired"}
  <p role="alert" class="banner" data-testid="session-banner">
    Session expired.
    <button type="button" onclick={() => location.reload()}>Sign in again</button>
  </p>
{:else if $banner === "offline"}
  <p role="status" class="banner notice" data-testid="session-offline">You are offline.</p>
{:else if $banner === "connection"}
  <p role="status" class="banner notice" data-testid="session-connection">Connection problem. Retrying on the next action.</p>
{/if}

<style>
  .banner {
    margin: 0;
    padding: 0.5rem 1rem;
    background: #5a2a1a;
  }
  .notice {
    background: #3a322b;
  }
  button {
    margin-left: 0.5rem;
  }
</style>
