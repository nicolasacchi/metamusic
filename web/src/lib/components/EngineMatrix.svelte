<script lang="ts">
  // The capability matrix (read-only): rows capabilities, columns engines, then adoption and backend state.
  import type { EngineList } from "../engines";
  import { adoptionText, backendText, engineMatrix } from "../engineMatrix";

  interface Props {
    list: EngineList;
  }
  let { list }: Props = $props();
  const rows = $derived(engineMatrix(list));
</script>

<div class="scroll">
  <table data-testid="engine-matrix">
    <thead>
      <tr>
        <th scope="col">Capability</th>
        {#each list.engines as e (e.name)}<th scope="col" data-testid="engine-col">{e.label}{#if e.name === list.default_engine} <span class="muted">(default)</span>{/if}</th>{/each}
      </tr>
    </thead>
    <tbody>
      {#each rows as r (r.key)}
        <tr data-testid="matrix-row-{r.key}">
          <th scope="row">{r.label}</th>
          {#each r.cells as c, i (i)}
            <td class={c.kind} data-kind={c.kind}>{c.text}</td>
          {/each}
        </tr>
      {/each}
      <tr data-testid="matrix-adoption">
        <th scope="row">Adoption</th>
        {#each list.engines as e (e.name)}<td>{adoptionText(e)}{#if e.adopt_hint}<br /><span class="muted">{e.adopt_hint}</span>{/if}</td>{/each}
      </tr>
      <tr data-testid="matrix-backend">
        <th scope="row">Backend</th>
        {#each list.engines as e (e.name)}<td>{backendText(e)}</td>{/each}
      </tr>
    </tbody>
  </table>
</div>

<style>
  .scroll {
    overflow-x: auto;
    max-width: 100%;
  }
  table {
    border-collapse: collapse;
    width: 100%;
  }
  th,
  td {
    text-align: left;
    padding: 0.4rem 0.6rem;
    border-bottom: 1px solid #3a322b;
    vertical-align: top;
    overflow-wrap: anywhere;
  }
  td.no {
    color: #8a7f74;
  }
  td.unverified {
    color: #e0a030;
  }
</style>
