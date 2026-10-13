<script lang="ts">
  import { encode } from "uqr";
  import { api } from "../../api/client";
  import { idempotentAction } from "../../api/idem";
  import type { components } from "../../api/schema";
  import { createCountdown } from "../humanView";

  type Pairing = components["schemas"]["PairingCodeOut"];
  interface Props {
    oncreated?: () => void;
  }
  let { oncreated }: Props = $props();

  let name = $state("");
  let busy = $state(false);
  let error = $state("");
  let pairing = $state<Pairing | null>(null);
  let now = $state(Date.now());
  let cd = $state<ReturnType<typeof createCountdown> | null>(null);

  const expired = $derived(cd ? cd.expired(now) : false);
  const label = $derived(cd ? cd.label(now) : "");
  const qr = $derived(pairing?.pair_url && !expired ? encode(pairing.pair_url, { ecc: "M", border: 2 }).data : null);

  $effect(() => {
    if (!cd || expired) return;
    const h = setInterval(() => (now = Date.now()), 500);
    return () => clearInterval(h);
  });

  function pretty(code: string): string {
    return code.includes("-") ? code : code.replace(/^(.{4})(.+)$/, "$1-$2");
  }

  async function submit(e: SubmitEvent) {
    e.preventDefault();
    if (busy || !name.trim()) return;
    busy = true;
    error = "";
    try {
      const send = idempotentAction((key) =>
        api.POST("/human/api/devices/pairing-codes", { params: { header: { "Idempotency-Key": key } }, body: { name: name.trim() } }),
      );
      const r = await send();
      if (r.data?.code) {
        pairing = r.data;
        now = Date.now();
        cd = createCountdown(Math.max(0, Date.parse(r.data.expires_at) - now), () => Date.now());
        name = "";
        oncreated?.();
      } else error = r.error?.message ?? "Could not create a pairing code";
    } catch {
      error = "Could not create a pairing code";
    } finally {
      busy = false;
    }
  }
</script>

<form onsubmit={submit} aria-label="Pair a device" data-testid="pairing-form">
  <label>Device name <input bind:value={name} maxlength="80" required data-testid="pairing-name" /></label>
  <button type="submit" disabled={busy} data-testid="pairing-create">Create pairing code</button>
  {#if error}<p role="alert">{error}</p>{/if}
</form>

{#if pairing?.code}
  <section aria-label="Pairing code" data-testid="pairing-panel" data-expired={expired}>
    <p>
      Code for <strong>{pairing.name}</strong>:
      <code class="code" data-testid="pairing-code" aria-disabled={expired}>{expired ? "expired" : pretty(pairing.code)}</code>
    </p>
    <p data-testid="pairing-countdown" aria-live="off">{label}</p>
    {#if qr}
      <svg viewBox={`0 0 ${qr.length} ${qr.length}`} width="200" height="200" role="img" aria-label="Pairing QR code" shape-rendering="crispEdges" data-testid="pairing-qr">
        <rect width={qr.length} height={qr.length} fill="#fff" />
        {#each qr as row, y (y)}
          {#each row as cell, x (x)}
            {#if cell}<rect {x} {y} width="1" height="1" fill="#000" />{/if}
          {/each}
        {/each}
      </svg>
      <p class="muted">Scan on the new device, or open <code>{pairing.pair_url}</code></p>
    {:else}
      <p class="muted">This code has expired. Create a new one.</p>
    {/if}
    <button type="button" disabled={expired} data-testid="pairing-copy" onclick={() => void navigator.clipboard?.writeText(pairing?.pair_url ?? "")}>Copy pairing link</button>
  </section>
{/if}

<style>
  .code {
    font-size: 1.6rem;
    letter-spacing: 0.1em;
  }
  .code[aria-disabled="true"] {
    opacity: 0.4;
    text-decoration: line-through;
  }
</style>
