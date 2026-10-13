// Pure helpers for the /human pages (kept out of the .svelte files so they are unit-testable).

/** Copyable MCP setup commands. They reference ${MM_TOKEN} and never contain a token. */
export function mcpTemplates(publicUrl: string): { claude: string; stdio: string } {
  const base = publicUrl.replace(/\/+$/, "");
  return {
    claude: `claude mcp add --transport http metamusic ${base}/mcp --header "Authorization: Bearer \${MM_TOKEN}"`,
    stdio: `claude mcp add metamusic --env MM_TOKEN="\${MM_TOKEN}" -- metamusic mcp-stdio`,
  };
}

/** Holds a freshly minted token in memory for display; clear() on navigation or dismissal. */
export function createReveal() {
  const r = {
    token: "",
    show(t: string) {
      r.token = t;
    },
    clear() {
      r.token = "";
    },
  };
  return r;
}

export function formatCountdown(remainingMs: number): string {
  const s = Math.max(0, Math.ceil(remainingMs / 1000));
  return `${Math.floor(s / 60)}:${String(s % 60).padStart(2, "0")}`;
}

export function createCountdown(totalMs: number, now: () => number) {
  const end = now() + totalMs;
  return {
    label: (t: number) => formatCountdown(end - t),
    expired: (t: number) => t >= end,
  };
}
