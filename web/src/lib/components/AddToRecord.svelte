<script lang="ts">
  import { api } from "../../api/client";
  import { canEdit, type Viewer } from "../recordView";
  import { MAX_RECORD_SONGS, addSongsToRecord, capForRecord, createRecord, listRecordsForPicker, type RecordSummary } from "../crateApi";

  interface Props {
    songIds: readonly string[];
    /** Records the Song is already in (Song page: `get_song.records`). */
    member?: readonly { id: string; title: string; status: string }[];
    /** Track page: offer to pin this Take in the new slot (one Song only). */
    pinTake?: string;
    /** The caller; published Records are offered only when `canEdit` (recordView.ts, the single rule) allows it. */
    viewer?: Viewer | null;
    heading?: string;
    /** Test seam: skip the fetch. */
    initialRecords?: RecordSummary[];
    onadded?: (record: { id: string; title: string }) => void;
  }
  let { songIds, member = [], pinTake, viewer = null, heading = "Records", initialRecords, onadded }: Props = $props();

  // svelte-ignore state_referenced_locally
  let records = $state<RecordSummary[]>(initialRecords ?? []);
  // svelte-ignore state_referenced_locally
  let loaded = $state(initialRecords !== undefined);
  let chosen = $state("");
  let newTitle = $state("");
  let pin = $state(false);
  let busy = $state(false);
  let message = $state("");
  let failed = $state(false);
  let opened = $state<{ id: string; title: string }[]>([]);
  const inRecords = $derived([...member, ...opened.filter((a) => !member.some((m) => m.id === a.id)).map((a) => ({ ...a, status: "" }))]);
  const open = $derived(records.filter((r) => !r.hidden && (r.status !== "published" || canEdit(viewer, r))));
  const capped = $derived(capForRecord(songIds));

  $effect(() => {
    if (loaded) return;
    loaded = true;
    void listRecordsForPicker(api).then((r) => {
      if (r.ok) records = r.value;
      else loaded = false;
    });
  });
  $effect(() => {
    if (!chosen && open[0]) chosen = open[0].id;
  });

  function ok(r: { id: string; title: string }, text: string) {
    failed = false;
    message = text;
    if (!opened.some((o) => o.id === r.id)) opened = [...opened, r];
    onadded?.(r);
  }
  async function add() {
    const rec = open.find((x) => x.id === chosen);
    if (!rec || busy || songIds.length === 0) return;
    busy = true;
    message = "";
    const r = await addSongsToRecord(api, rec, songIds, pin && pinTake ? pinTake : undefined);
    busy = false;
    if (r.ok) {
      rec.revision = r.value.revision;
      ok(rec, `Added ${r.value.added} ${r.value.added === 1 ? "Song" : "Songs"} to ${rec.title}.`);
    } else {
      failed = true;
      message = r.message;
    }
  }
  async function create() {
    const title = newTitle.trim();
    if (!title || busy) return;
    busy = true;
    message = "";
    const r = await createRecord(api, title, capped.ids)();
    busy = false;
    if (r.ok) {
      const rec = r.value.record;
      newTitle = "";
      ok(rec, `Created ${rec.title}.${capped.note ? " " + capped.note : ""}`);
    } else {
      failed = true;
      message = r.message;
    }
  }
</script>

<section class="add" data-testid="add-to-record">
  <h2>{heading}</h2>
  {#if inRecords.length > 0}
    <p data-testid="in-records">
      In Records:
      {#each inRecords as r, i (r.id)}{i > 0 ? ", " : ""}<a href="/records/{encodeURIComponent(r.id)}">{r.title}</a>{#if r.status} <span class="muted">({r.status})</span>{/if}{/each}
    </p>
  {/if}
  {#if open.length > 0}
    <div class="row">
      <label>
        <span class="sr">Record</span>
        <select bind:value={chosen} aria-label="Record" data-testid="record-select">
          {#each open as r (r.id)}<option value={r.id}>{r.title}{r.status === "draft" ? "" : ` (${r.status})`}</option>{/each}
        </select>
      </label>
      <button type="button" onclick={add} disabled={busy || !chosen || songIds.length === 0} data-testid="record-add">Add to Record{songIds.length > 1 ? ` (${songIds.length})` : ""}</button>
    </div>
    {#if pinTake && songIds.length === 1}
      <label class="check"><input type="checkbox" bind:checked={pin} data-testid="record-pin-take" /> Pin this Take in the slot</label>
    {/if}
  {/if}
  <form
    class="row"
    onsubmit={(e) => {
      e.preventDefault();
      void create();
    }}
  >
    <input bind:value={newTitle} placeholder="New Record title" aria-label="New Record title" maxlength="200" data-testid="record-new-title" />
    <button type="submit" disabled={busy || !newTitle.trim()} data-testid="record-create-add">{songIds.length > 0 ? "Create Record with" : "Create Record"}{songIds.length > 1 ? ` ${Math.min(songIds.length, MAX_RECORD_SONGS)}` : songIds.length === 1 ? " this Song" : ""}</button>
  </form>
  {#if songIds.length > MAX_RECORD_SONGS}<p class="muted" data-testid="record-cap-note">{capped.note}</p>{/if}
  {#if message}<p role={failed ? "alert" : "status"} data-testid="record-add-note">{message}</p>{/if}
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
  .check {
    display: flex;
    gap: var(--sp-2);
    align-items: center;
    min-height: var(--hit);
  }
  .sr {
    position: absolute;
    left: -9999px;
  }
</style>
