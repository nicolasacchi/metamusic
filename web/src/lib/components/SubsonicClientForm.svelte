<script lang="ts">
  import { api } from "../../api/client";
  import { buildClientBody, canSubmit, effectiveUsername, emptyClientForm } from "../subsonicForm";
  import { onceSubmit } from "../secretOnce";

  interface Props {
    oncreated: (r: { secrets: { apiKey: string | null; password: string | null }; username: string; serverUrl: string }) => void;
  }
  let { oncreated }: Props = $props();

  let f = $state(emptyClientForm());
  let busy = $state(false);
  let error = $state("");
  // One action = one key; kept across retries of an identical submission, dropped on success.
  let send: (() => Promise<Awaited<ReturnType<typeof post>>>) | null = null;
  let sentBody = "";

  function post(key: string, body: ReturnType<typeof buildClientBody>) {
    return api.POST("/api/subsonic-clients", { params: { header: { "Idempotency-Key": key } }, body });
  }

  async function submit(e: SubmitEvent) {
    e.preventDefault();
    if (busy || !canSubmit(f)) return;
    busy = true;
    error = "";
    try {
      const body = buildClientBody(f);
      const sig = JSON.stringify(body);
      if (!send || sig !== sentBody) {
        sentBody = sig;
        send = onceSubmit((key) => post(key, body));
      }
      const r = await send();
      if (r.data) {
        send = null;
        oncreated({ secrets: { apiKey: r.data.api_key, password: r.data.password }, username: r.data.username, serverUrl: r.data.server_url });
        f = emptyClientForm();
      } else error = r.error?.message ?? "Could not create the client";
    } catch {
      error = "Could not create the client";
    } finally {
      busy = false;
    }
  }
</script>

<form onsubmit={submit} aria-label="New Subsonic client" data-testid="subsonic-form">
  <label>Name <input bind:value={f.name} maxlength="80" required data-testid="subsonic-name" /></label>
  <label>Username
    <input
      value={effectiveUsername(f)}
      oninput={(e) => {
        f.username = e.currentTarget.value;
        f.usernameEdited = true;
      }}
      maxlength="40"
      data-testid="subsonic-username-input"
    />
  </label>
  <fieldset>
    <legend>Credentials</legend>
    <label><input type="checkbox" bind:checked={f.apiKey} data-testid="subsonic-opt-apikey" /> API key</label>
    <label><input type="checkbox" bind:checked={f.password} data-testid="subsonic-opt-password" /> Password</label>
  </fieldset>
  <label><input type="checkbox" bind:checked={f.write} /> May rate, star and bookmark</label>
  <label>Default stream format
    <select bind:value={f.defaultFormat}>
      <option value="raw">Raw / original</option>
      <option value="mp3">MP3</option>
    </select>
  </label>
  <button type="submit" disabled={busy || !canSubmit(f)} data-testid="subsonic-create">Create client</button>
  {#if error}<p role="alert">{error}</p>{/if}
</form>
