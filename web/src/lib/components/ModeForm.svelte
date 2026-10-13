<script lang="ts">
  import { onDestroy } from "svelte";
  import { api } from "../../api/client";
  import type { components } from "../../api/schema";
  import { debounced, emptyForm, lintForm, toRequest, type DraftForm } from "../draftForm";
  import { isVariantMode, parseSeeds, type GenerationIntent, type VariantMode } from "../genIntent";
  import LintPanel from "./LintPanel.svelte";

  type Lint = components["schemas"]["LintResult"];
  interface Props {
    intent: GenerationIntent;
    /** The completed intent (seeds, patch, title, recipe applied) when the user presses Estimate. */
    onsubmit: (i: GenerationIntent) => void;
  }
  let { intent, onsubmit }: Props = $props();

  const editMode = $derived<VariantMode | null>(intent.mode === "from_template" ? (intent.template_mode ?? null) : isVariantMode(intent.mode) ? intent.mode : null);

  /* svelte-ignore state_referenced_locally */
  let nSeeds = $state(String(intent.n_seeds ?? 1));
  /* svelte-ignore state_referenced_locally */
  let seedsText = $state(intent.seeds?.join(", ") ?? "");
  /* svelte-ignore state_referenced_locally */
  let title = $state(intent.title ?? "");
  /* svelte-ignore state_referenced_locally */
  let style = $state(intent.patch?.style ?? "");
  /* svelte-ignore state_referenced_locally */
  let lyrics = $state(intent.patch?.lyrics ?? "");
  /* svelte-ignore state_referenced_locally */
  let templateMode = $state<VariantMode>(intent.template_mode ?? "style_edit");
  /* svelte-ignore state_referenced_locally */
  let recipe = $state<DraftForm>(intent.recipe ? { ...emptyForm(), id: String(intent.recipe.id ?? ""), style: String(intent.recipe.style ?? ""), lyrics: String(intent.recipe.lyrics ?? ""), cot: String(intent.recipe.cot ?? ""), seed: String(intent.recipe.seed ?? "0") } : emptyForm());
  let lint = $state<Lint | null>(null);
  let lintBusy = $state(false);
  let error = $state("");
  let seq = 0;

  const needsPatch = $derived(intent.mode === "from_template" ? templateMode : editMode);
  const runLint = debounced(async () => {
    const mine = ++seq;
    lintBusy = true;
    try {
      const r = await lintForm(api, recipe);
      if (mine === seq && r) lint = r;
    } catch {
      /* lint is advisory */
    } finally {
      if (mine === seq) lintBusy = false;
    }
  });
  onDestroy(() => runLint.cancel());

  function submit(e: Event) {
    e.preventDefault();
    error = "";
    const seeds = parseSeeds(seedsText);
    if (seeds === null) return void (error = "Seeds must be distinct whole numbers.");
    const n = Number(nSeeds);
    if (!Number.isInteger(n) || n < 1) return void (error = "Seeds per track must be a whole number of at least 1.");
    const mode = needsPatch;
    const seedPart = mode === "same_seed_edit" ? {} : seeds.length ? { seeds } : { n_seeds: n };
    const patch = { ...(style.trim() ? { style: style.trim() } : {}), ...(lyrics.trim() ? { lyrics } : {}) };
    if (mode === "style_edit" && !patch.style) return void (error = "Enter the new style.");
    if (mode === "lyric_edit" && !patch.lyrics) return void (error = "Enter the new lyrics.");
    if (mode === "same_seed_edit" && !patch.style && !patch.lyrics) return void (error = "Change the style or the lyrics.");
    const t = title.trim() ? { title: title.trim() } : {};
    if (intent.mode === "from_template") onsubmit({ ...intent, template_mode: templateMode, ...seedPart, patch });
    else if (intent.mode === "compose_new") onsubmit({ ...intent, recipe: toRequest(recipe), ...t, ...seedPart });
    else if (isVariantMode(intent.mode)) onsubmit({ ...intent, ...seedPart, patch, ...t });
    else onsubmit({ ...intent, ...seedPart });
  }
</script>

<form onsubmit={submit} data-testid="mode-form">
  <h2>Options</h2>
  {#if intent.mode === "from_template"}
    <label>Edit applied to every track
      <select bind:value={templateMode} data-testid="template-mode">
        <option value="new_seed">New seed</option>
        <option value="same_seed_edit">Same seed, edit</option>
        <option value="lyric_edit">Lyric edit</option>
        <option value="style_edit">Style edit</option>
      </select>
    </label>
  {/if}
  {#if intent.mode === "compose_new"}
    <label>Id <input type="text" bind:value={recipe.id} oninput={runLint} /></label>
    <label>Style <textarea bind:value={recipe.style} rows="2" oninput={runLint}></textarea></label>
    <label>Lyrics <textarea bind:value={recipe.lyrics} rows="8" oninput={runLint}></textarea></label>
    <label>Chain of thought <input type="text" bind:value={recipe.cot} oninput={runLint} /></label>
    <label>Seed <input type="text" inputmode="numeric" bind:value={recipe.seed} oninput={runLint} /></label>
    <LintPanel {lint} busy={lintBusy} />
  {/if}
  {#if needsPatch === "style_edit" || needsPatch === "same_seed_edit"}
    <label>New style <textarea bind:value={style} rows="2" data-testid="patch-style"></textarea></label>
  {/if}
  {#if needsPatch === "lyric_edit" || needsPatch === "same_seed_edit"}
    <label>New lyrics <textarea bind:value={lyrics} rows="8" data-testid="patch-lyrics"></textarea></label>
  {/if}
  <label>Title (optional) <input type="text" bind:value={title} maxlength="200" data-testid="title" /></label>
  {#if needsPatch !== "same_seed_edit"}
    <label>Seeds per track <input type="number" min="1" max="50" bind:value={nSeeds} data-testid="n-seeds" /></label>
    <label>Or explicit seeds <input type="text" bind:value={seedsText} placeholder="1, 2, 3" data-testid="seeds" /></label>
  {/if}
  {#if error}<p role="alert">{error}</p>{/if}
  <p><button type="submit" class="primary" data-testid="estimate-button">Estimate</button></p>
</form>

<style>
  label {
    display: grid;
    gap: 0.2rem;
    margin: 0.5rem 0;
    max-width: 40rem;
  }
</style>
