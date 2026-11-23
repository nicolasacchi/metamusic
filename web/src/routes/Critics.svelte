<script lang="ts">
  import { api } from "../api/client";
  import WilsonBar from "../lib/components/WilsonBar.svelte";
  import { ariaFor, isSlotBiased, disagreementHref, ruleText, scoreRows, stateText, tierText, type Bench } from "../lib/benchView";

  let { critic = "" }: { critic?: string } = $props();
  let bench = $state<Bench | null>(null);
  let error = $state("");

  $effect(() => {
    const c = critic;
    bench = null;
    error = "";
    void (async () => {
      try {
        const r = await api.GET("/api/critic-bench", { params: { query: c ? { critic: c } : {} } });
        if (c !== critic) return;
        if (r.data) bench = r.data;
        else error = r.error?.message ?? "Could not load the critic bench";
      } catch {
        error = "Could not load the critic bench";
      }
    })();
  });
</script>

<section data-testid="critics-page">
  <h1>Critics{critic ? " (one)" : ""}</h1>
  <p class="muted">How often each automatic critic would have agreed with your own stars and duel verdicts, within one lineage. A label only: it never decides anything but a last tie-break.</p>
  {#if error}<p role="alert" data-testid="critics-error">{error}</p>{/if}
  {#if !bench && !error}
    <p class="muted" role="status">Loading…</p>
  {:else if bench}
    <p class="muted" data-testid="critics-rule">{ruleText(bench.rule)}</p>
    <p class="muted">{bench.window.rating_pairs} rated pairs, {bench.window.duel_pairs} duel pairs, {bench.window.lines} sung lines{bench.window.since ? ` since ${bench.window.since.slice(0, 10)}` : ""}.</p>
    {#if bench.critics.length === 0}
      <p class="muted" data-testid="critics-empty">No critics are registered.</p>
    {:else}
      <table data-testid="critics-table">
        <thead>
          <tr><th>Critic</th><th>State</th><th>Agreement</th></tr>
        </thead>
        <tbody>
          {#each bench.critics as c (c.id)}
            <tr data-testid="critic-row" data-critic={c.id}>
              <td>
                <a href="/critics?critic={encodeURIComponent(c.id)}">{c.label}</a>
                <div class="muted">{tierText(c.tier)}, {c.kind}</div>
              </td>
              <td><span class="chip {c.state}" data-testid="critic-state">{stateText(c.state)}</span>{#if isSlotBiased(c)} <span class="chip biased" title="Picks the same side of a duel too often">slot bias</span>{/if}</td>
              <td>
                {#each scoreRows(c) as r (r.key)}
                  <WilsonBar label={r.label} score={r.score} rule={bench.rule} />
                {:else}
                  <span class="muted">No pairs yet</span>
                {/each}
                {#if c.thresholds}
                  <details>
                    <summary>By threshold</summary>
                    <ul>
                      {#each c.thresholds as t (t.mixed)}<li>{t.agree}/{t.n} at {t.mixed}</li>{/each}
                    </ul>
                  </details>
                {/if}
              </td>
            </tr>
            {#if critic === c.id}
              <tr data-testid="critic-disagreements">
                <td colspan="3">
                  <h2>Where {c.label} disagreed with you</h2>
                  {#each scoreRows(c) as r (r.key)}
                    <h3>{r.label}</h3>
                    <p class="muted">{ariaFor(r.label, r.score)}</p>
                    {#if r.score.disagreements && r.score.disagreements.length > 0}
                      <ul>
                        {#each r.score.disagreements as k (k)}<li><a href={disagreementHref(k)}>{k.includes("|") ? "Compare the pair" : "Open the duel"}</a></li>{/each}
                      </ul>
                    {:else}
                      <p class="muted">None in the newest pairs.</p>
                    {/if}
                  {/each}
                  <p><a href="/critics">All critics</a></p>
                </td>
              </tr>
            {/if}
          {/each}
        </tbody>
      </table>
    {/if}
  {/if}
</section>

<style>
  table {
    width: 100%;
    border-collapse: collapse;
  }
  th,
  td {
    text-align: left;
    vertical-align: top;
    padding: var(--sp-2);
    border-bottom: 1px solid var(--line);
  }
  .chip {
    padding: 0 var(--sp-2);
    border-radius: var(--r-pill);
    font-size: var(--fs-xs);
    background: var(--surface-2);
    color: var(--text-2);
  }
  .chip.trusted {
    background: color-mix(in srgb, var(--ok) 25%, transparent);
    color: var(--ok);
  }
  .chip.contrary,
  .chip.biased {
    background: var(--bad-soft);
    color: var(--bad);
  }
</style>
