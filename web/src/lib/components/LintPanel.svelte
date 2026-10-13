<script lang="ts">
  import type { components } from "../../api/schema";
  import { lintIssues } from "../draftForm";

  type LintResult = components["schemas"]["LintResult"];
  interface Props {
    lint: LintResult | null;
    busy?: boolean;
    /** Called with the 1-based lyrics line of an issue that names one. */
    onjump?: (line: number) => void;
  }
  let { lint, busy = false, onjump }: Props = $props();

  const issues = $derived(lintIssues(lint));
  const errors = $derived(issues.filter((i) => i.severity === "error").length);
  const warnings = $derived(issues.length - errors);
</script>

<section class="lint" aria-busy={busy} data-testid="lint-panel">
  <h2>Lint</h2>
  {#if lint}
    <p class="muted" data-testid="lint-summary">
      {errors} errors · {warnings} warnings · ~{Math.round(lint.estimated_duration_s)} s ({lint.duration_basis})
      {#if lint.language.detected || lint.language.tagged}· language {lint.language.tagged || "?"}/{lint.language.detected || "?"}{/if}
    </p>
    <ul>
      {#each issues as i, n (n)}
        <li class={i.severity} data-testid="lint-issue" data-severity={i.severity} data-code={i.code}>
          <b>{i.severity}</b>
          <code>{i.code}</code>
          {i.text}
          {#if i.line !== undefined}
            <button type="button" class="link" onclick={() => onjump?.(i.line!)}>line {i.line}</button>
          {:else if i.field}<span class="muted">({i.field})</span>{/if}
        </li>
      {:else}
        <li class="muted">No issues.</li>
      {/each}
    </ul>
  {:else}
    <p class="muted">{busy ? "Checking…" : "No lint result yet."}</p>
  {/if}
</section>

<style>
  ul {
    list-style: none;
    padding: 0;
    margin: 0;
  }
  li {
    padding: 0.25rem 0;
    overflow-wrap: anywhere;
  }
  .error b {
    color: #e66;
  }
  .warning b {
    color: #db5;
  }
  .link {
    background: none;
    border: 0;
    color: inherit;
    text-decoration: underline;
    cursor: pointer;
    padding: 0;
  }
</style>
