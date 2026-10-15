<script lang="ts">
  import { api } from "../../api/client";
  import type { components } from "../../api/schema";
  import { updateSession } from "../museApi";

  type Session = components["schemas"]["SessionSummary"];
  interface Props {
    sessions: Session[];
    /** A session changed on the server: replace it in the list. */
    onupdate: (s: Session) => void;
  }
  let { sessions, onupdate }: Props = $props();
  let editing = $state<string | null>(null);
  let draft = $state("");
  let message = $state("");

  async function save(s: Session, patch: { name?: string; hidden?: boolean }) {
    message = "";
    const r = await updateSession(api, s.id, { revision: s.revision, ...patch });
    if (r.kind === "ok") {
      onupdate(r.value);
      editing = null;
    } else if (r.kind === "failed" && r.code === "REVISION_CONFLICT") {
      message = "That session changed elsewhere: reload the Board and try again.";
    } else {
      message = r.kind === "failed" ? r.message || "Could not update the session." : "Network error.";
    }
  }
</script>

<details class="sessions" data-testid="session-panel">
  <summary>Sessions ({sessions.length})</summary>
  <ul>
    {#each sessions as s (s.id)}
      <li>
        {#if editing === s.id}
          <form
            onsubmit={(e) => {
              e.preventDefault();
              void save(s, { name: draft.trim() });
            }}
          >
            <input bind:value={draft} aria-label="Session name" maxlength="200" required />
            <button type="submit">Save</button>
            <button type="button" onclick={() => (editing = null)}>Cancel</button>
          </form>
        {:else}
          <span>{s.name}</span> <span class="muted">{s.kind} · {s.songs} songs</span>
          <button
            type="button"
            onclick={() => {
              editing = s.id;
              draft = s.name;
            }}>Rename</button>
          <button type="button" onclick={() => save(s, { hidden: !s.hidden })}>{s.hidden ? "Show" : "Hide"}</button>
        {/if}
      </li>
    {/each}
  </ul>
  {#if message}<p role="alert">{message}</p>{/if}
</details>

<style>
  ul {
    list-style: none;
    padding: 0;
    margin: 0.25rem 0;
    display: grid;
    gap: 0.25rem;
  }
</style>
