// The one place `fetch` is allowed for non-registry URLs (D-M2b-13): clip bytes from /media/.
export class ClipLinkExpired extends Error {
  constructor() {
    super("Clip link expired");
    this.name = "ClipLinkExpired";
  }
}
export class ClipGone extends Error {
  constructor() {
    super("Clip is no longer available");
    this.name = "ClipGone";
  }
}

export interface FetchClipOptions {
  fetch?: (url: string, init?: RequestInit) => Promise<Response>;
  /** Defaults to location.origin. */
  origin?: string;
}

export async function fetchClip(url: string, signal?: AbortSignal, opts: FetchClipOptions = {}): Promise<ArrayBuffer> {
  const origin = opts.origin ?? location.origin;
  let u: URL;
  try {
    u = new URL(url, origin);
  } catch {
    throw new Error("Invalid clip URL");
  }
  if (u.origin !== origin || !u.pathname.startsWith("/media/") || u.pathname.includes("..")) {
    throw new Error("Refusing to fetch a non-media URL");
  }
  const send = opts.fetch ?? ((x: string, init?: RequestInit) => globalThis.fetch(x, init));
  const res = await send(u.href, { cache: "force-cache", signal });
  if (res.status === 403) throw new ClipLinkExpired();
  if (res.status === 404) throw new ClipGone();
  if (!res.ok) throw new Error(`Clip fetch failed (${res.status})`);
  return res.arrayBuffer();
}
