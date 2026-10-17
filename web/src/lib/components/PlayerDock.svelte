<script lang="ts">
  // The one player in the footer: the 64 px mini-player on a phone, the 72 px bar on desktop. The switch lives here so
  // the footer in App.svelte stays free of conditionals around the audio element.
  import type { Player } from "../player.svelte";
  import { viewport } from "../viewport.svelte";
  import { judgeCard } from "../judgeCard.svelte";
  import MiniPlayer from "./MiniPlayer.svelte";
  import PlayerBar from "./PlayerBar.svelte";

  let { player, duel = false }: { player: Player; duel?: boolean } = $props();
</script>

<!-- The Judge card is the player while it is on screen: no mini-player (and no title in the DOM for blind mode). -->
{#if judgeCard.active}
  <!-- nothing -->
{:else if viewport.phone}
  <MiniPlayer {player} {duel} />
{:else}
  <PlayerBar {player} />
{/if}
