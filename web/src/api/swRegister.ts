// Service worker registration (C115.1). Only in a secure context with service worker support; a refused or
// failed registration (plain HTTP, Playwright's serviceWorkers: "block") is logged and ignored.
export interface SwEnv {
  isSecureContext: boolean;
  navigator: { serviceWorker?: Pick<ServiceWorkerContainer, "register"> };
  warn(message: string, err?: unknown): void;
}

function defaultEnv(): SwEnv {
  return {
    isSecureContext: typeof isSecureContext !== "undefined" && isSecureContext,
    navigator: typeof navigator !== "undefined" ? navigator : {},
    warn: (m, e) => console.warn(m, e),
  };
}

/** Everything offline listening needs: secure context, service workers, Cache Storage and WebCrypto. */
export function offlineAvailable(env: { isSecureContext: boolean; hasServiceWorker: boolean; hasCaches: boolean; hasSubtle: boolean }): boolean {
  return env.isSecureContext && env.hasServiceWorker && env.hasCaches && env.hasSubtle;
}

export function detectOfflineAvailable(): boolean {
  return offlineAvailable({
    isSecureContext: typeof isSecureContext !== "undefined" && isSecureContext,
    hasServiceWorker: typeof navigator !== "undefined" && "serviceWorker" in navigator,
    hasCaches: typeof caches !== "undefined",
    hasSubtle: typeof crypto !== "undefined" && !!crypto.subtle,
  });
}

export async function registerWorker(env: SwEnv = defaultEnv()): Promise<ServiceWorkerRegistration | null> {
  if (!env.isSecureContext || !env.navigator.serviceWorker) return null;
  try {
    return await env.navigator.serviceWorker.register("/sw.js", { scope: "/" });
  } catch (e) {
    env.warn("service worker registration failed", e);
    return null;
  }
}

/** Calls `onWaiting` when an updated worker is installed and waiting behind a controlling one. */
export function watchForUpdate(reg: ServiceWorkerRegistration, onWaiting: (reg: ServiceWorkerRegistration) => void): void {
  const check = () => {
    if (reg.waiting && navigator.serviceWorker.controller) onWaiting(reg);
  };
  check();
  reg.addEventListener("updatefound", () => {
    const w = reg.installing;
    w?.addEventListener("statechange", () => {
      if (w.state === "installed") check();
    });
  });
}

/** Asks the waiting worker to take over; the page reloads when the controller changes. */
export function applyUpdate(reg: ServiceWorkerRegistration): void {
  navigator.serviceWorker.addEventListener("controllerchange", () => location.reload(), { once: true });
  reg.waiting?.postMessage({ type: "skip-waiting" });
}
