<script lang="ts">
  import { tick } from "svelte";
  import { api } from "../api/client";
  import type { components } from "../api/schema";
  import { editSlots, getChanges, getRecord, proposeRecord, songTakes, updateRecord, freezeAll, type Result } from "../lib/curationApi";
  import { pushToast } from "../lib/judge.svelte";
  import { player } from "../lib/player.svelte";
  import { chapterNames, enqueueRecord, playAsTape, playRecord, takeQueue } from "../lib/recordPlayer";
  import { canEdit, canPublish, createReadyOnce, moveOp, recordWarningRows, reorderOp, slotModels, type SlotOp, type Viewer } from "../lib/recordView";
  import { routeReady } from "../lib/scrollRestore";
  import { pendingDelay } from "../lib/songView";
  import { fmt } from "../lib/time";
  import { fetchViewer } from "../lib/viewer";
  import ChangeLog from "../lib/components/ChangeLog.svelte";
  import EnergyCurve from "../lib/components/EnergyCurve.svelte";
  import LinerPreview from "../lib/components/LinerPreview.svelte";
  import RecordCover from "../lib/components/RecordCover.svelte";
  import RecordStatusChip from "../lib/components/RecordStatus.svelte";
  import SlotRow from "../lib/components/SlotRow.svelte";
  import SongPicker from "../lib/components/SongPicker.svelte";
  import TapePanel from "../lib/components/TapePanel.svelte";

  type View = components["schemas"]["RecordView"];
  type Change = components["schemas"]["ChangeView"];
  interface Props {
    id: string;
    /** Pre-loaded view (unit tests; the page otherwise fetches it). */
    initial?: View;
    viewer?: Viewer | null;
  }
  let { id, initial, viewer = null }: Props = $props();

  const TABS = ["slots", "changelog", "liner", "energy", "tapes"] as const;
  const TAB_LABEL = { slots: "Slots", changelog: "Changelog", liner: "Liner notes", energy: "Energy", tapes: "Tapes" } as const;
  const PAGE = 50;

  // svelte-ignore state_referenced_locally
  let me = $state<Viewer | null>(viewer);
  $effect(() => {
    if (viewer) return;
    void fetchViewer().then((v) => (me = v));
  });
  // svelte-ignore state_referenced_locally
  let view = $state<View | null>(initial ?? null);
  let error = $state("");
  let stale = $state(false);
  let busy = $state(false);
  let tab = $state<(typeof TABS)[number]>("slots");
  let selectedSlot = $state<string | null>(null);
  let olderChanges = $state<Change[]>([]);
  let moreChanges = $state(true);
  let editing = $state(false);
  let draft = $state({ title: "", subtitle: "", note: "" });
  let proposing = $state(false);
  let rationale = $state("");
  let attempts = 0;
  const ready = createReadyOnce(() => void routeReady());
  // svelte-ignore state_referenced_locally
  if (initial) ready.loaded(true);

  const rec = $derived(view?.record ?? null);
  const editable = $derived(rec !== null && canEdit(me, rec));
  const models = $derived(view ? slotModels(view) : []);
  const ids = $derived(view?.slots.map((s) => s.id) ?? []);
  const warnings = $derived(view ? recordWarningRows(view) : []);
  const titles = $derived(new Map((view?.slots ?? []).map((s) => [s.id, s.take?.title ?? s.song.title])));
  const changes = $derived([...(view?.changes ?? []), ...olderChanges]);
  const allPinned = $derived(!!view && view.slots.length > 0 && view.slots.every((s) => s.mode === "pinned" || s.state === "empty"));

  async function load(silent = false) {
    const mine = id;
    const r = await getRecord(api, mine, ["liner", "energy", "changes", "tapes"], PAGE);
    if (mine !== id) return;
    if (r.kind === "ok") {
      view = r.value;
      olderChanges = [];
      moreChanges = (r.value.changes?.length ?? 0) >= PAGE;
      stale = false;
      error = "";
      schedulePending(r.value);
    } else if (!silent || !view) {
      error = r.kind === "failed" && r.status === 404 ? "Record not found" : r.kind === "unavailable" ? "Records are not available yet." : "Failed to load the record";
    }
    ready.loaded(true);
  }
  $effect(() => {
    if (initial) return;
    void load();
  });

  // The changelog may lag by one sync pass: refetch after 1.5 s, at most three times.
  let pendingTimer: ReturnType<typeof setTimeout> | undefined;
  function schedulePending(v: View) {
    clearTimeout(pendingTimer);
    const wait = pendingDelay(v.pending, attempts);
    if (wait === null) {
      if (!v.pending) attempts = 0;
      return;
    }
    attempts++;
    pendingTimer = setTimeout(() => void load(true), wait);
  }
  $effect(() => () => clearTimeout(pendingTimer));

  /** Run one edit. The mutating ops answer without the extras, so the response is merged and then refetched. */
  async function mutate(run: () => Promise<Result<View>>): Promise<boolean> {
    if (busy) return false;
    busy = true;
    const r = await run();
    busy = false;
    if (r.kind === "ok") {
      view = { ...r.value, liner: view?.liner, energy: view?.energy, changes: view?.changes, tapes: view?.tapes };
      attempts = 0;
      void load(true);
      return true;
    }
    if (r.kind === "stale") stale = true;
    else if (r.kind === "unavailable") pushToast("Not available yet");
    else if (r.kind === "failed") pushToast(r.message || r.code || "The change was refused");
    else pushToast("Could not reach the server");
    return false;
  }
  const slotEdit = (ops: SlotOp[]) => (view ? mutate(() => editSlots(api, id, view!.record.revision, ops)) : Promise.resolve(false));

  async function reorder(op: SlotOp | null, focusId?: string, focusTid?: string) {
    if (!op) return;
    if (await slotEdit([op]) && focusId) {
      await tick();
      const row = document.querySelector(`[data-slot-id="${focusId}"]`);
      const b = row?.querySelector<HTMLButtonElement>(`[data-testid="${focusTid}"]:not(:disabled)`) ?? row?.querySelector<HTMLButtonElement>('[data-testid="slot-up"]:not(:disabled), [data-testid="slot-down"]:not(:disabled)');
      b?.focus();
    }
  }
  const move = (i: number, d: -1 | 1) => reorder(moveOp(ids, i, d), ids[i], d < 0 ? "slot-up" : "slot-down");

  // Drag (desktop pointer): the handle follows the pointer; the drop makes one reorder op.
  let dragFrom = $state<number | null>(null);
  function startDrag(e: PointerEvent, from: number) {
    if (!editable || busy) return;
    e.preventDefault();
    dragFrom = from;
    const list = (e.currentTarget as HTMLElement).closest("ol");
    const target = (y: number) => {
      const rows = [...(list?.querySelectorAll<HTMLElement>("[data-slot-id]") ?? [])];
      let t = rows.length - 1;
      for (let i = 0; i < rows.length; i++) {
        const b = rows[i]!.getBoundingClientRect();
        if (y < b.top + b.height / 2) {
          t = i;
          break;
        }
      }
      return t;
    };
    const up = (ev: PointerEvent) => {
      window.removeEventListener("pointerup", up);
      window.removeEventListener("pointercancel", cancel);
      const op = reorderOp(ids, from, target(ev.clientY));
      dragFrom = null;
      void reorder(op);
    };
    const cancel = () => {
      window.removeEventListener("pointerup", up);
      window.removeEventListener("pointercancel", cancel);
      dragFrom = null;
    };
    window.addEventListener("pointerup", up);
    window.addEventListener("pointercancel", cancel);
  }

  const pin = (slotId: string, takeId: string) => slotEdit([{ op: "pin", slot_id: slotId, take_id: takeId }]);
  const unpin = (slotId: string) => slotEdit([{ op: "unpin", slot_id: slotId }]);
  const setSide = (slotId: string, side: "A" | "B" | "C" | "D" | null) => slotEdit([{ op: "set_side", slot_id: slotId, side }]);
  const remove = (slotId: string) => slotEdit([{ op: "remove", slot_id: slotId }]);
  const addSong = (songId: string) => slotEdit([{ op: "add", song_id: songId }]);
  const toggleFreeze = () => (allPinned ? slotEdit([{ op: "unpin_all" }]) : view ? mutate(() => freezeAll(api, id, view!.record.revision)) : Promise.resolve(false));

  // `?add=<song ids>` (from the Board): add what is not in the Record yet, once.
  let addedFromQuery = false;
  $effect(() => {
    if (addedFromQuery || !view || !editable || typeof location === "undefined") return;
    addedFromQuery = true;
    const want = (new URLSearchParams(location.search).get("add") ?? "").split(",").filter(Boolean);
    const have = new Set(view.slots.map((s) => s.song.id));
    const ops: SlotOp[] = [...new Set(want)].filter((s) => !have.has(s)).slice(0, 50).map((song_id) => ({ op: "add", song_id }));
    if (ops.length) void slotEdit(ops);
  });

  function startEdit() {
    if (!rec) return;
    draft = { title: rec.title, subtitle: rec.subtitle, note: rec.liner_note };
    editing = true;
  }
  async function saveEdit() {
    if (!rec) return;
    const body: components["schemas"]["UpdateRecordInBody"] = { revision: rec.revision };
    if (draft.title.trim() !== rec.title) body.title = draft.title.trim();
    if (draft.subtitle.trim() !== rec.subtitle) body.subtitle = draft.subtitle.trim();
    if (draft.note !== rec.liner_note) body.liner_note = draft.note;
    if (Object.keys(body).length === 1) {
      editing = false;
      return;
    }
    if (await mutate(() => updateRecord(api, id, body))) editing = false;
  }
  const patch = (b: Omit<components["schemas"]["UpdateRecordInBody"], "revision">) => (rec ? mutate(() => updateRecord(api, id, { revision: rec.revision, ...b })) : Promise.resolve(false));

  async function propose() {
    if (!rec || !rationale.trim()) return;
    if (await mutate(() => proposeRecord(api, id, rec.revision, rationale.trim()))) {
      proposing = false;
      rationale = "";
    }
  }

  async function olderPage() {
    const last = changes[changes.length - 1];
    if (!last || busy) return;
    const r = await getChanges(api, id, { cursor: last.seq, limit: PAGE });
    if (r.kind === "ok") {
      olderChanges = [...olderChanges, ...r.value.items];
      moreChanges = r.value.next_cursor !== null && r.value.items.length > 0;
    }
  }

  const playable = $derived(view ? takeQueue(view) : []);
  const play = () => void (view && playRecord(view));
  const queueRecord = () => void (view && enqueueRecord(view));
  let tapeBusy = $state(false);
  async function playTapeNow() {
    if (!view || !rec || tapeBusy) return;
    tapeBusy = true;
    try {
      const r = await playAsTape(rec.id, rec.revision, { title: rec.title, tapes: view.tapes ?? [], names: chapterNames(view), wait: false });
      if (r.kind === "failed") pushToast(r.code === "NOT_IMPLEMENTED" ? "Tape export is not available yet." : r.message);
      else if (r.kind === "pending") pushToast("The tape is being made. Open the Tapes tab to follow it.");
    } finally {
      tapeBusy = false;
    }
  }
</script>

{#if error}
  <p role="alert">{error}</p>
  <p><a href="/records">Back to records</a></p>
{:else if !view || !rec}
  <p class="muted">Loading…</p>
{:else}
  <article data-testid="record-studio" data-id={rec.id}>
    {#if stale}
      <div class="stale" role="alert" data-testid="record-stale">Changed elsewhere. <button type="button" onclick={() => load()}>Reload</button></div>
    {/if}

    <header>
      <RecordCover cover={view.cover} size={120} alt="Cover of {rec.title}" />
      <div class="head">
        {#if editing}
          <form onsubmit={(e) => { e.preventDefault(); void saveEdit(); }}>
            <label>Title <input bind:value={draft.title} maxlength="200" required /></label>
            <label>Subtitle <input bind:value={draft.subtitle} maxlength="200" /></label>
            <label>Notes <textarea bind:value={draft.note} rows="3"></textarea></label>
            <div class="actions"><button type="submit" class="primary" disabled={busy}>Save</button><button type="button" onclick={() => (editing = false)}>Cancel</button></div>
          </form>
        {:else}
          <h1 data-testid="record-title">{rec.title}</h1>
          {#if rec.subtitle}<p class="muted">{rec.subtitle}</p>{/if}
          {#if rec.liner_note}<p class="note">{rec.liner_note}</p>{/if}
          <p class="meta">
            <RecordStatusChip status={rec.status} hidden={rec.hidden} />
            <span class="muted num">{view.totals.playable}/{view.totals.slots} tracks · {fmt(view.totals.duration_s)}{#if view.totals.lossy > 0} · {view.totals.lossy} lossy{/if}</span>
          </p>
          {#if rec.proposal}<p class="muted">Proposed: {rec.proposal.note}</p>{/if}
          {#if editable}
            <div class="actions">
              <button type="button" onclick={startEdit}>Edit details</button>
              <button type="button" disabled={busy} onclick={() => patch({ cover_reroll: true })}>Re-roll cover</button>
              <button type="button" disabled={busy} aria-pressed={rec.hidden} onclick={() => patch({ hidden: !rec!.hidden })}>{rec.hidden ? "Unhide" : "Hide"}</button>
            </div>
          {/if}
        {/if}
      </div>
    </header>

    <div class="actions bar">
      <button type="button" class="primary" disabled={playable.length === 0} onclick={play} data-testid="record-play">Play</button>
      <button type="button" disabled={playable.length === 0} onclick={queueRecord}>Add to queue</button>
      {#if rec.status === "published"}
        <button type="button" disabled={tapeBusy} onclick={() => void playTapeNow()} data-testid="record-play-tape">Play as tape</button>
      {/if}
      {#if editable}
        <button type="button" disabled={busy || view.slots.length === 0} onclick={toggleFreeze} data-testid="record-freeze">{allPinned ? "Unfreeze" : "Freeze"}</button>
        {#if rec.status === "draft"}<button type="button" onclick={() => (proposing = !proposing)} aria-expanded={proposing}>Propose</button>{/if}
      {/if}
      {#if canPublish(me) && rec.status !== "published"}
        <a class="tap" href="/human/records/{rec.id}/publish" data-testid="record-publish">Publish…</a>
      {:else if canPublish(me)}
        <a class="tap" href="/human/records/{rec.id}/publish" data-testid="record-publish">Unpublish…</a>
      {/if}
    </div>

    {#if proposing}
      <form class="propose" onsubmit={(e) => { e.preventDefault(); void propose(); }}>
        <label>Why is it ready? <textarea bind:value={rationale} rows="3" maxlength="500" required></textarea></label>
        <button type="submit" class="primary" disabled={busy || !rationale.trim()}>Send proposal</button>
      </form>
    {/if}

    {#if warnings.length > 0}
      <ul class="warnings" data-testid="record-warnings">
        {#each warnings as w, i (i)}<li>{w.text}</li>{/each}
      </ul>
    {/if}
    {#if view.totals.undecided > 0}
      <p class="muted" data-testid="record-undecided">{view.totals.undecided} {view.totals.undecided === 1 ? "pick is" : "picks are"} undecided</p>
    {/if}

    <div class="tabs" role="tablist" aria-label="Record sections">
      {#each TABS as t (t)}
        <button type="button" role="tab" aria-selected={tab === t} class:on={tab === t} onclick={() => (tab = t)} data-testid="record-tab-{t}">{TAB_LABEL[t]}</button>
      {/each}
    </div>

    {#if tab === "slots"}
      {#if models.length === 0}<p class="muted">No songs yet. Add one below.</p>{/if}
      <ol class="slots" data-testid="record-slots">
        {#each models as m (m.slot.id)}
          <SlotRow
            model={m}
            {editable}
            {busy}
            dragging={dragFrom === m.index}
            selected={selectedSlot === m.slot.id}
            onup={() => move(m.index, -1)}
            ondown={() => move(m.index, 1)}
            onhandle={(e) => startDrag(e, m.index)}
            onpin={(t) => pin(m.slot.id, t)}
            onunpin={() => unpin(m.slot.id)}
            onside={(s) => setSide(m.slot.id, s)}
            onremove={() => remove(m.slot.id)}
            onselect={() => {
              selectedSlot = selectedSlot === m.slot.id ? null : m.slot.id;
              if (selectedSlot) tab = "changelog";
            }}
            onplay={(t) => player.play(t, [t], { title: m.slot.take?.title ?? "" })}
            loadTakes={(sid) => songTakes(api, sid)}
          />
        {/each}
      </ol>
      {#if editable}<SongPicker existing={view.slots.map((s) => s.song.id)} disabled={busy} onadd={addSong} />{/if}
    {:else if tab === "changelog"}
      <ChangeLog {changes} slotId={selectedSlot} hasMore={moreChanges} {busy} pending={view.pending} onmore={olderPage} onclearfilter={() => (selectedSlot = null)} />
    {:else if tab === "liner"}
      <LinerPreview liner={view.liner} />
    {:else if tab === "energy"}
      <EnergyCurve energy={view.energy} />
    {:else}
      <TapePanel recordId={rec.id} recordTitle={rec.title} revision={rec.revision} status={rec.status} briefs={view.tapes ?? []} {titles} warningCount={view.totals.warnings} viewer={me} onstale={() => (stale = true)} />
    {/if}
  </article>
{/if}

<style>
  header {
    display: flex;
    gap: var(--sp-4);
    align-items: flex-start;
    flex-wrap: wrap;
  }
  .head {
    flex: 1;
    min-width: 16rem;
  }
  h1 {
    margin: 0;
  }
  .note {
    white-space: pre-wrap;
  }
  .meta {
    display: flex;
    flex-wrap: wrap;
    gap: var(--sp-2);
    align-items: center;
  }
  form {
    display: grid;
    gap: var(--sp-2);
  }
  label {
    display: grid;
    gap: var(--sp-1);
  }
  .actions {
    display: flex;
    flex-wrap: wrap;
    gap: var(--sp-2);
    align-items: center;
  }
  .bar {
    margin: var(--sp-3) 0;
  }
  .stale {
    background: var(--warn-soft);
    border: 1px solid var(--warn);
    border-radius: var(--r-md);
    padding: var(--sp-2) var(--sp-3);
    margin-bottom: var(--sp-3);
  }
  .warnings {
    color: var(--warn);
    padding-left: var(--sp-5);
  }
  .tabs {
    display: flex;
    gap: var(--sp-1);
    overflow-x: auto;
    margin: var(--sp-3) 0;
  }
  .tabs button {
    min-height: var(--hit);
    white-space: nowrap;
  }
  .tabs button.on {
    background: var(--accent-soft);
    border-color: var(--accent);
    color: var(--accent);
  }
  .slots {
    margin: 0 0 var(--sp-4);
    padding: 0;
    display: grid;
    gap: var(--sp-2);
  }
  .propose {
    margin-bottom: var(--sp-3);
  }
</style>
