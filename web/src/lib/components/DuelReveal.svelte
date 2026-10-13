<script lang="ts">
  import type { components } from "../../api/schema";

  type Duel = components["schemas"]["Duel"];
  interface Props {
    duel: Duel;
    onnext?: () => void;
  }
  let { duel, onnext }: Props = $props();

  const verdict = $derived(duel.human_verdict ?? duel.my_verdict);
  const winner = $derived(verdict?.winner_track_id ?? null);
  const rows = $derived(
    (["a", "b"] as const).map((k) => {
      const s = duel.slots[k];
      return {
        key: k,
        title: s.track?.title ?? null,
        seed: s.track?.seed ?? null,
        trackId: s.track?.id ?? null,
        gain: s.gain_db,
        lufs: s.window_lufs,
        won: winner !== null && s.track?.id === winner,
      };
    }),
  );
  const outcome = $derived(
    !verdict
      ? "No verdict yet"
      : verdict.verdict === "tie"
        ? "Tie"
        : verdict.verdict === "both_bad"
          ? "Both bad"
          : `${verdict.verdict.toUpperCase()} is better`,
  );
  const a = $derived(duel.slots.a.track?.id);
  const b = $derived(duel.slots.b.track?.id);
</script>

<section class="reveal" data-testid="duel-reveal" aria-label="Result">
  <h3>{outcome}</h3>
  <table>
    <thead><tr><th>Slot</th><th>Track</th><th>Seed</th><th>Gain</th><th>Window loudness</th></tr></thead>
    <tbody>
      {#each rows as r (r.key)}
        <tr class:won={r.won}>
          <td>{r.key.toUpperCase()}{r.won ? " ★" : ""}</td>
          <td>{#if r.trackId}<a href="/tracks/{r.trackId}">{r.title}</a>{:else}hidden{/if}</td>
          <td>{r.seed ?? "–"}</td>
          <td>{r.gain === null ? "–" : `${r.gain.toFixed(1)} dB`}</td>
          <td>{r.lufs === null ? "–" : `${r.lufs.toFixed(1)} LUFS`}</td>
        </tr>
      {/each}
    </tbody>
  </table>
  {#if duel.critic_verdicts.length > 0}
    <ul class="critics">
      {#each duel.critic_verdicts as c (c.id)}
        <li>Critic: {c.verdict}{c.note ? ` — ${c.note}` : ""}</li>
      {/each}
    </ul>
  {/if}
  {#if verdict?.note}<p class="vnote">{verdict.note}</p>{/if}
  <p class="links">
    {#if a && b}<a href="/compare?a={a}&b={b}">Compare</a> ·{/if}
    <a href="/ladder">Ladder</a>
    {#if onnext}· <button type="button" onclick={() => onnext()}>Next duel</button>{/if}
  </p>
</section>

<style>
  table {
    width: 100%;
    border-collapse: collapse;
  }
  th,
  td {
    text-align: left;
    padding: 0.25rem 0.5rem;
  }
  tr.won {
    font-weight: 700;
  }
  .vnote {
    white-space: pre-wrap;
  }
</style>
