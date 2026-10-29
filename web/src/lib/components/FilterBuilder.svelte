<script lang="ts">
  import { api } from "../../api/client";
  import { countMatches } from "../crateApi";
  import {
    emptyFilterHint,
    fieldTable,
    filterFromForm,
    formFromFilter,
    splitList,
    unknownKeys,
    validateFilter,
    type CrateFilter,
    type FilterErrors,
    type FormModel,
    type Subject,
  } from "../crateFilter";

  interface Props {
    subject: Subject;
    /** The filter to start from. Re-mount (`{#key}`) to load another. */
    value: Record<string, unknown>;
    onchange: (filter: CrateFilter, errors: FilterErrors) => void;
    /** Debounce of the live count (ms). */
    debounceMs?: number;
    /** Test seam: a count to show without fetching. */
    count?: number | null;
  }
  let { subject, value, onchange, debounceMs = 300, count: seeded = null }: Props = $props();

  const table = $derived(fieldTable(subject));
  // svelte-ignore state_referenced_locally
  let form = $state<FormModel>(formFromFilter(subject, value));
  // Free-text list fields keep their typing here so the caret does not jump while the array is rebuilt.
  // svelte-ignore state_referenced_locally
  let texts = $state<Record<string, string>>(
    Object.fromEntries(Object.entries(formFromFilter(subject, value)).flatMap(([k, v]) => (Array.isArray(v) && !fieldTable(subject)[k]?.enum ? [[k, v.join(", ")]] : []))),
  );
  // svelte-ignore state_referenced_locally
  const dropped = unknownKeys(subject, value);

  const filter = $derived(filterFromForm(subject, form));
  const errors = $derived(validateFilter(subject, filter));
  const groups = $derived.by(() => {
    const out: { name: string; keys: string[] }[] = [];
    for (const [k, spec] of Object.entries(table)) {
      let g = out.find((x) => x.name === spec.group);
      if (!g) out.push((g = { name: spec.group, keys: [] }));
      g.keys.push(k);
    }
    return out;
  });

  $effect(() => {
    onchange(filter, errors);
  });

  // Live count: the same list op with limit 1, debounced; `total` is the count.
  // svelte-ignore state_referenced_locally
  let live = $state<number | null>(seeded);
  let counting = $state(false);
  let countError = $state("");
  let seq = 0;
  $effect(() => {
    const f = filter;
    const bad = Object.keys(errors).length > 0;
    if (seeded !== null || bad) return;
    const mine = ++seq;
    counting = true;
    const t = setTimeout(() => {
      void countMatches(api, subject, f).then((r) => {
        if (mine !== seq) return;
        counting = false;
        if (r.ok) {
          live = r.value;
          countError = "";
        } else countError = r.message;
      });
    }, debounceMs);
    return () => {
      clearTimeout(t);
    };
  });

  function toggle(k: string, v: string) {
    const cur = (form[k] as string[]) ?? [];
    form[k] = cur.includes(v) ? cur.filter((x) => x !== v) : [...cur, v];
  }
  function typed(k: string, text: string) {
    texts[k] = text;
    form[k] = splitList(text);
  }
  function reset() {
    form = formFromFilter(subject, {});
    texts = {};
  }
  const label = (s: string) => s.replaceAll("_", " ");
</script>

<div class="fb" data-testid="crate-filter-builder">
  {#if dropped.length > 0}
    <p role="status" class="warn" data-testid="filter-unknown">Not editable here and dropped on save: {dropped.join(", ")}.</p>
  {/if}
  {#each groups as g (g.name)}
    <fieldset>
      <legend>{g.name}</legend>
      {#each g.keys as k (k)}
        {@const spec = table[k]!}
        <div class="field" data-key={k}>
          {#if spec.kind === "bool"}
            <label for="fb-{k}">{spec.label}</label>
            <select id="fb-{k}" bind:value={form[k] as string} data-testid="fb-{k}" aria-invalid={errors[k] ? "true" : undefined}>
              <option value="">any</option>
              <option value="true">yes</option>
              <option value="false">no</option>
            </select>
          {:else if spec.kind === "strings" && spec.enum}
            <span class="lbl" id="fb-{k}-l">{spec.label}</span>
            <div class="checks" role="group" aria-labelledby="fb-{k}-l">
              {#each spec.enum as opt (opt)}
                <label class="check"><input type="checkbox" checked={(form[k] as string[]).includes(opt)} onchange={() => toggle(k, opt)} data-testid="fb-{k}-{opt}" /> {label(opt)}</label>
              {/each}
            </div>
          {:else if spec.kind === "strings"}
            <label for="fb-{k}">{spec.label}</label>
            <input id="fb-{k}" value={texts[k] ?? ""} oninput={(e) => typed(k, e.currentTarget.value)} placeholder={spec.hint ?? "comma separated"} data-testid="fb-{k}" aria-invalid={errors[k] ? "true" : undefined} />
          {:else if spec.kind === "int" || spec.kind === "number"}
            <label for="fb-{k}">{spec.label}</label>
            <input id="fb-{k}" type="number" inputmode={spec.kind === "int" ? "numeric" : "decimal"} min={spec.min} max={spec.max} step={spec.kind === "int" ? 1 : "any"} bind:value={form[k] as string} data-testid="fb-{k}" aria-invalid={errors[k] ? "true" : undefined} />
          {:else if spec.kind === "instant"}
            <label for="fb-{k}">{spec.label}</label>
            <input id="fb-{k}" type="date" bind:value={form[k] as string} data-testid="fb-{k}" />
          {:else}
            <label for="fb-{k}">{spec.label}</label>
            <input id="fb-{k}" bind:value={form[k] as string} placeholder={spec.hint ?? ""} data-testid="fb-{k}" />
          {/if}
          {#if errors[k]}<p class="err" role="alert" data-testid="fb-error-{k}">{errors[k]}</p>{/if}
        </div>
      {/each}
    </fieldset>
  {/each}
  {#if errors["filter"]}<p class="err" role="alert" data-testid="fb-error-filter">{errors["filter"]}</p>{/if}

  <div class="foot">
    <output class="count" data-testid="crate-live-count" aria-live="polite">
      {#if Object.keys(errors).length > 0}
        Fix the highlighted fields to see a count.
      {:else if countError}
        {countError}
      {:else if live === null}
        Counting…
      {:else}
        <strong>{live}</strong> {subject === "song" ? (live === 1 ? "Song" : "Songs") : live === 1 ? "Take" : "Takes"} match{live === 1 ? "es" : ""}{#if Object.keys(filter).length === 0}
          <span class="muted"> ({emptyFilterHint(subject)})</span>{/if}{#if counting}<span class="muted"> · updating…</span>{/if}
      {/if}
    </output>
    <button type="button" onclick={reset} data-testid="fb-reset">Clear all</button>
  </div>
</div>

<style>
  .fb {
    display: grid;
    gap: var(--sp-3);
  }
  fieldset {
    display: grid;
    gap: var(--sp-2);
    margin: 0;
    padding: var(--sp-3);
    border: 1px solid var(--line);
    border-radius: var(--r-md);
    background: var(--surface);
  }
  legend {
    padding: 0 var(--sp-2);
    color: var(--text-2);
    font-size: var(--fs-sm);
  }
  .field {
    display: grid;
    gap: var(--sp-1);
  }
  .lbl {
    font-size: var(--fs-sm);
  }
  .checks {
    display: flex;
    flex-wrap: wrap;
    gap: var(--sp-2);
  }
  .check {
    display: inline-flex;
    gap: var(--sp-2);
    align-items: center;
    min-height: var(--hit);
  }
  .err {
    margin: 0;
    color: var(--bad);
    font-size: var(--fs-sm);
  }
  .warn {
    color: var(--warn);
  }
  .foot {
    position: sticky;
    bottom: calc(var(--player-h, 0px) + var(--tabbar-h));
    display: flex;
    flex-wrap: wrap;
    gap: var(--sp-3);
    align-items: center;
    justify-content: space-between;
    padding: var(--sp-2) var(--sp-3);
    background: var(--bg-raised);
    border: 1px solid var(--line-strong);
    border-radius: var(--r-md);
  }
  .count {
    font-size: var(--fs-md);
  }
</style>
