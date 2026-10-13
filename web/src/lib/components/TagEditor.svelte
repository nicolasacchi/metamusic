<script lang="ts">
  import type { components } from "../../api/schema";
  import { normalizeTag } from "../judge.svelte";

  type TagRef = components["schemas"]["TagRef"];
  interface Props {
    tags: readonly TagRef[];
    /** Known tag names (one `list_tags` call per page, fetched by the page). */
    suggestions?: readonly string[];
    onadd: (name: string) => void;
    onremove: (name: string) => void;
  }
  let { tags, suggestions = [], onadd, onremove }: Props = $props();

  let draft = $state("");
  const listId = `tag-suggest-${Math.random().toString(36).slice(2, 8)}`;
  const options = $derived(suggestions.filter((s) => !tags.some((t) => t.name === s)).slice(0, 50));

  function add() {
    const name = normalizeTag(draft);
    if (!name) return;
    draft = "";
    onadd(name);
  }
  function badge(t: TagRef): string {
    return t.sources.join("+");
  }
</script>

<div class="tags" data-testid="tag-editor">
  <ul>
    {#each tags as t (t.name)}
      <li class="chip" title="source: {badge(t)}">
        {t.name}<span class="src">{badge(t)}</span>
        <button type="button" aria-label="Remove tag {t.name}" onclick={() => onremove(t.name)}>×</button>
      </li>
    {/each}
  </ul>
  <form
    onsubmit={(e) => {
      e.preventDefault();
      add();
    }}
  >
    <input type="text" list={listId} bind:value={draft} placeholder="add tag" aria-label="Add tag" maxlength="64" />
    <datalist id={listId}>
      {#each options as o (o)}<option value={o}></option>{/each}
    </datalist>
    <button type="submit" disabled={normalizeTag(draft) === ""}>Add</button>
  </form>
</div>

<style>
  ul {
    display: flex;
    flex-wrap: wrap;
    gap: 0.3rem;
    list-style: none;
    margin: 0 0 0.4rem;
    padding: 0;
  }
  .chip {
    background: #3a322b;
    border-radius: 0.8rem;
    padding: 0 0.2rem 0 0.6rem;
    font-size: 0.85rem;
    display: inline-flex;
    align-items: center;
    gap: 0.3rem;
  }
  .src {
    font-size: 0.65rem;
    opacity: 0.6;
    text-transform: uppercase;
  }
  .chip button {
    background: none;
    border: none;
    color: inherit;
    cursor: pointer;
    min-width: 1.6rem;
    min-height: 1.6rem;
  }
  form {
    display: flex;
    gap: 0.4rem;
  }
</style>
