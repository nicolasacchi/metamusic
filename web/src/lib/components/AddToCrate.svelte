<script lang="ts">
  import { api } from "../../api/client";
  import { addMembers, createCrate, itemsText, listCrates, type CrateSummary } from "../crateApi";
  import type { Subject } from "../crateFilter";

  interface Props {
    subject: Subject;
    /** Song ids (subject song) or Take ids (subject take). */
    ids: readonly string[];
    /** Crates the item is already in (Song page: `get_song.crates`). */
    member?: readonly { id: string; name: string }[];
    heading?: string;
    /** Test seam: skip the fetch. */
    initialCrates?: CrateSummary[];
    onadded?: (crate: { id: string; name: string }) => void;
  }
  let { subject, ids, member = [], heading = "Crates", initialCrates, onadded }: Props = $props();

  // svelte-ignore state_referenced_locally
  let crates = $state<CrateSummary[]>(initialCrates ?? []);
  // svelte-ignore state_referenced_locally
  let loaded = $state(initialCrates !== undefined);
  let chosen = $state("");
  let newName = $state("");
  let busy = $state(false);
  let message = $state("");
  let failed = $state(false);
  let added = $state<{ id: string; name: string }[]>([]);
  const inCrates = $derived([...member, ...added.filter((a) => !member.some((m) => m.id === a.id))]);
  const manual = $derived(crates.filter((c) => c.kind === "manual" && !c.hidden));

  $effect(() => {
    if (loaded) return;
    loaded = true;
    void listCrates(api, { subject, kind: "manual", limit: 100 }).then((r) => {
      if (r.ok) {
        crates = r.value.items;
        if (!chosen && manual[0]) chosen = manual[0].id;
      } else loaded = false;
    });
  });
  $effect(() => {
    if (!chosen && manual[0]) chosen = manual[0].id;
  });

  function done(c: { id: string; name: string }, text: string) {
    failed = false;
    message = text;
    if (!added.some((a) => a.id === c.id)) added = [...added, { id: c.id, name: c.name }];
    onadded?.(c);
  }
  async function add() {
    const c = manual.find((x) => x.id === chosen);
    if (!c || busy || ids.length === 0) return;
    busy = true;
    message = "";
    const r = await addMembers(api, c.id, subject, ids);
    busy = false;
    if (r.ok) done(c, itemsText(r.value, c.name));
    else {
      failed = true;
      message = r.message;
    }
  }
  async function create() {
    const name = newName.trim();
    if (!name || busy || ids.length === 0) return;
    busy = true;
    message = "";
    const first = ids.slice(0, 200).map((id) => (subject === "song" ? { song_id: id } : { track_id: id }));
    const r = await createCrate(api, { name, kind: "manual", subject, items: first })();
    if (r.ok) {
      const c = r.value.crate;
      let text = `Created ${c.name}.`;
      if (ids.length > 200) {
        const more = await addMembers(api, c.id, subject, ids.slice(200));
        if (!more.ok) text += " " + more.message;
      }
      crates = [...crates, { ...c, count: r.value.count }];
      chosen = c.id;
      newName = "";
      done(c, text);
    } else {
      failed = true;
      message = r.message;
    }
    busy = false;
  }
</script>

<section class="add" data-testid="add-to-crate">
  <h2>{heading}</h2>
  {#if inCrates.length > 0}
    <p data-testid="in-crates">
      In:
      {#each inCrates as c, i (c.id)}{i > 0 ? ", " : ""}<a href="/crates/{encodeURIComponent(c.id)}">{c.name}</a>{/each}
    </p>
  {/if}
  {#if manual.length > 0}
    <div class="row">
      <label>
        <span class="sr">Crate</span>
        <select bind:value={chosen} aria-label="Crate" data-testid="crate-select">
          {#each manual as c (c.id)}<option value={c.id}>{c.name}</option>{/each}
        </select>
      </label>
      <button type="button" onclick={add} disabled={busy || !chosen || ids.length === 0} data-testid="crate-add">Add to Crate{ids.length > 1 ? ` (${ids.length})` : ""}</button>
    </div>
  {/if}
  <form
    class="row"
    onsubmit={(e) => {
      e.preventDefault();
      void create();
    }}
  >
    <input bind:value={newName} placeholder="New Crate name" aria-label="New Crate name" maxlength="80" data-testid="crate-new-name" />
    <button type="submit" disabled={busy || !newName.trim() || ids.length === 0} data-testid="crate-create-add">Create and add</button>
  </form>
  {#if message}<p role={failed ? "alert" : "status"} data-testid="crate-add-note">{message}</p>{/if}
</section>

<style>
  .add {
    display: grid;
    gap: var(--sp-2);
  }
  h2 {
    margin: 0;
    font-size: var(--fs-md);
  }
  .row {
    display: flex;
    flex-wrap: wrap;
    gap: var(--sp-2);
    align-items: center;
  }
  .sr {
    position: absolute;
    left: -9999px;
  }
</style>
