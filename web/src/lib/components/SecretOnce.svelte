<script lang="ts">
  import { pushToast } from "../judge.svelte";
  import type { Secrets } from "../secretOnce";

  interface Props {
    secrets: Secrets;
    serverUrl: string;
    username: string;
    ondismiss: () => void;
  }
  let { secrets, serverUrl, username, ondismiss }: Props = $props();

  let copied = $state("");
  async function copy(which: string, text: string) {
    try {
      await navigator.clipboard.writeText(text);
      copied = which;
    } catch {
      pushToast("Copy failed; select the text and copy manually");
    }
  }
</script>

<section aria-label="New credentials" data-testid="subsonic-reveal">
  <p><strong>Copy these now; they are shown only once.</strong></p>
  <p>Server URL <code data-testid="subsonic-server">{serverUrl}</code> · Username <code data-testid="subsonic-username">{username}</code></p>
  {#if secrets.apiKey}
    <p>
      API key <code data-testid="subsonic-apikey">{secrets.apiKey}</code>
      <button type="button" data-testid="subsonic-copy-apikey" onclick={() => copy("apikey", secrets.apiKey ?? "")}>{copied === "apikey" ? "Copied" : "Copy API key"}</button>
    </p>
  {/if}
  {#if secrets.password}
    <p>
      Password <code data-testid="subsonic-password">{secrets.password}</code>
      <button type="button" data-testid="subsonic-copy-password" onclick={() => copy("password", secrets.password ?? "")}>{copied === "password" ? "Copied" : "Copy password"}</button>
    </p>
  {/if}
  <h3>App setup</h3>
  <ul>
    <li>Enter the server URL and the username above.</li>
    <li>Prefer the API key when the app supports it; otherwise use the password (token authentication).</li>
    <li>Set the stream format to Raw / Original to play the FLAC files unchanged.</li>
  </ul>
  <button type="button" data-testid="subsonic-dismiss" onclick={ondismiss}>Done</button>
</section>
