// Show-once secrets (Subsonic API key, app password): memory only, never web storage.
import { idempotentAction } from "../api/idem";

export interface Secrets {
  apiKey?: string;
  password?: string;
}

export function createSecretOnce() {
  const s = {
    secrets: {} as Secrets,
    show(v: { apiKey: string | null; password: string | null }) {
      const next: Secrets = {};
      if (v.apiKey) next.apiKey = v.apiKey;
      if (v.password) next.password = v.password;
      s.secrets = next;
    },
    has(): boolean {
      return Object.keys(s.secrets).length > 0;
    },
    clear() {
      s.secrets = {};
    },
    /** Clears on navigation away; returns the detach function. */
    attach(target: EventTarget): () => void {
      const f = () => s.clear();
      target.addEventListener("popstate", f);
      target.addEventListener("pagehide", f);
      return () => {
        target.removeEventListener("popstate", f);
        target.removeEventListener("pagehide", f);
      };
    },
  };
  return s;
}

/** One user action: every call (a retry) reuses the same Idempotency-Key. */
export function onceSubmit<T>(send: (key: string) => Promise<T>): () => Promise<T> {
  return idempotentAction(send);
}
