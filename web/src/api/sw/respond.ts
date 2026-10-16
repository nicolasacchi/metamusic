// Builds the service worker's responses for downloaded media (C115.1). Pure over a ChunkStore.
import { assemble, chunksFor } from "./chunks";
import { MAX_RANGE, clampToCap, parseRange } from "./range";
import { mediaKey } from "./routes";
import type { ChunkStore } from "./store";

/** null: not a complete local file (or a chunk is missing) — the caller passes the request to the network. */
export async function serveMedia(req: Request, store: ChunkStore): Promise<Response | null> {
  const key = mediaKey(new URL(req.url).pathname);
  if (!key) return null;
  const meta = await store.meta(key.sha, key.ext);
  if (!meta || !meta.complete) return null;
  const size = meta.bytes;
  const base: Record<string, string> = {
    "Content-Type": meta.content_type,
    "Accept-Ranges": "bytes",
    ETag: `"${meta.sha}"`,
  };
  const range = parseRange(req.headers.get("Range"), size);
  if (range === "unsatisfiable") {
    return new Response(null, { status: 416, headers: { ...base, "Content-Range": `bytes */${size}` } });
  }
  if (range === null) {
    if (!(await store.getChunk(key.sha, key.ext, 0)) && size > 0) return null;
    let next = 0;
    const body = new ReadableStream<Uint8Array>({
      async pull(ctrl) {
        if (next >= meta.chunks) return ctrl.close();
        const c = await store.getChunk(key.sha, key.ext, next++);
        if (!c) return ctrl.error(new Error("offline chunk missing"));
        ctrl.enqueue(c);
      },
    });
    return new Response(body, { status: 200, headers: { ...base, "Content-Length": String(size) } });
  }
  const { start, end } = clampToCap(range, MAX_RANGE);
  const parts: Uint8Array[] = [];
  for (const n of chunksFor(start, end)) {
    const c = await store.getChunk(key.sha, key.ext, n);
    if (!c) return null;
    parts.push(c);
  }
  const body = assemble(parts, start, end);
  return new Response(body.slice().buffer as ArrayBuffer, {
    status: 206,
    headers: { ...base, "Content-Range": `bytes ${start}-${end}/${size}`, "Content-Length": String(body.length) },
  });
}
