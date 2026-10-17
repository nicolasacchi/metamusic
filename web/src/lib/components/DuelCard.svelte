<script lang="ts">
  import type { components } from "../../api/schema";
  import { keyAction, type Verdict } from "../duelKeys";
  import ABPlayer from "./ABPlayer.svelte";
  import VerdictBar from "./VerdictBar.svelte";
  import DuelReveal from "./DuelReveal.svelte";

  type Duel = components["schemas"]["Duel"];
  export interface Submission {
    verdict: Verdict;
    note: string;
    latency_ms: number;
    switches: number;
  }
  interface Props {
    duel: Duel;
    /** Submit the verdict; resolves to the updated duel (revealed) or rejects. */
    onsubmit: (s: Submission) => Promise<Duel>;
    onskip?: () => void;
    onnext?: () => void;
    /** get_duel: fresh clip URLs after a 403/404. */
    remint?: () => Promise<{ aUrl: string; bUrl: string } | null>;
  }
  let { duel, onsubmit, onskip, onnext, remint }: Props = $props();

  let chosen = $state<Verdict | null>(null);
  let note = $state("");
  let busy = $state(false);
  let error = $state("");
  let result = $state<Duel | null>(null);
  let player: ReturnType<typeof ABPlayer> | undefined = $state();
  let firstPlayAt = 0;

  const shown = $derived(result ?? (duel.state === "decided" ? duel : null));

  async function submit() {
    if (!chosen || busy) return;
    busy = true;
    error = "";
    try {
      const switches = player?.stats().switches ?? 0;
      const latency = firstPlayAt > 0 ? Math.round(performance.now() - firstPlayAt) : 0;
      result = await onsubmit({ verdict: chosen, note: note.trim(), latency_ms: latency, switches });
      player?.releaseFocus({ resume: true });
    } catch (e) {
      error = e instanceof Error ? e.message : "Could not submit";
    } finally {
      busy = false;
    }
  }

  function onkeydown(e: KeyboardEvent) {
    if (shown || !player) return;
    const a = keyAction(e, { chosen });
    if (!a) return;
    switch (a.type) {
      case "select":
        player.select(a.slot);
        break;
      case "toggle":
        player.toggle();
        break;
      case "playpause":
        player.playPause();
        break;
      case "restart":
        player.restart();
        break;
      case "choose":
        chosen = a.verdict;
        break;
      case "submit":
        void submit();
        break;
    }
  }
</script>

<svelte:window {onkeydown} />

<article class="duel" data-testid="duel-card">
  <header>
    <h2>{duel.blind ? "Blind duel" : "Duel"}</h2>
    {#if duel.loudness_matched}<span class="chip">loudness matched</span>{/if}
    <span class="chip">{duel.window.duration_s.toFixed(0)} s window</span>
  </header>
  {#if shown}
    <DuelReveal duel={shown} {onnext} />
  {:else if duel.state === "expired"}
    <p role="status">This duel expired.</p>
  {:else}
    {#if !duel.blind}
      <p class="titles" data-testid="duel-titles">
        <span>A: {duel.slots.a.track?.title ?? "?"}</span> · <span>B: {duel.slots.b.track?.title ?? "?"}</span>
      </p>
    {/if}
    <ABPlayer
      bind:this={player}
      aUrl={duel.slots.a.clip.url}
      bUrl={duel.slots.b.clip.url}
      {remint}
      onfirstplay={() => (firstPlayAt = performance.now())}
    />
    <VerdictBar bind:chosen bind:note {busy} onsubmit={() => void submit()} />
    {#if error}<p class="err" role="alert">{error}</p>{/if}
    {#if onskip}<button type="button" class="skip" onclick={() => onskip()}>Skip</button>{/if}
  {/if}
</article>

<style>
  .duel {
    display: grid;
    gap: 1rem;
  }
  header {
    display: flex;
    gap: 0.5rem;
    align-items: center;
  }
  header h2 {
    margin: 0;
    flex: 1;
  }
  .chip {
    font-size: 0.8rem;
    padding: 0.1rem 0.5rem;
    border: 1px solid #8886;
    border-radius: 999px;
  }
  .titles {
    margin: 0;
  }
  .err {
    color: #e5484d;
    margin: 0;
  }
  .skip {
    min-height: 48px;
  }
</style>
