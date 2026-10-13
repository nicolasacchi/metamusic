// Minimal ZIP and ID3v2 readers for the export specs (no dependencies).
import { inflateRawSync } from "node:zlib";

export interface ZipEntry {
  name: string;
  data: Buffer;
}

/** Reads a ZIP through its central directory; stored (0) and deflated (8) entries. */
export function readZip(buf: Buffer): ZipEntry[] {
  let eocd = -1;
  for (let i = buf.length - 22; i >= Math.max(0, buf.length - 22 - 65535); i--) {
    if (buf.readUInt32LE(i) === 0x06054b50) {
      eocd = i;
      break;
    }
  }
  if (eocd < 0) throw new Error("not a zip: no end-of-central-directory record");
  const count = buf.readUInt16LE(eocd + 10);
  let p = buf.readUInt32LE(eocd + 16);
  const out: ZipEntry[] = [];
  for (let n = 0; n < count; n++) {
    if (buf.readUInt32LE(p) !== 0x02014b50) throw new Error("bad central directory entry");
    const method = buf.readUInt16LE(p + 10);
    const csize = buf.readUInt32LE(p + 20);
    const nameLen = buf.readUInt16LE(p + 28);
    const extraLen = buf.readUInt16LE(p + 30);
    const commentLen = buf.readUInt16LE(p + 32);
    const local = buf.readUInt32LE(p + 42);
    const name = buf.subarray(p + 46, p + 46 + nameLen).toString("utf8");
    if (buf.readUInt32LE(local) !== 0x04034b50) throw new Error("bad local header");
    const start = local + 30 + buf.readUInt16LE(local + 26) + buf.readUInt16LE(local + 28);
    const raw = buf.subarray(start, start + csize);
    let data: Buffer;
    if (method === 0) data = Buffer.from(raw);
    else if (method === 8) data = inflateRawSync(raw);
    else throw new Error(`unsupported zip method ${method}`);
    out.push({ name, data });
    p += 46 + nameLen + extraLen + commentLen;
  }
  return out;
}

const syncsafe = (b: Buffer, o: number) => ((b[o] ?? 0) << 21) | ((b[o + 1] ?? 0) << 14) | ((b[o + 2] ?? 0) << 7) | (b[o + 3] ?? 0);

/** All text frames of an ID3v2.4 tag at the start of `mp3` (frame id -> text); throws on any other tag. */
export function id3Frames(mp3: Buffer): Record<string, string> {
  if (mp3.subarray(0, 3).toString("latin1") !== "ID3") throw new Error("no ID3 tag");
  if (mp3[3] !== 4) throw new Error(`ID3v2.${mp3[3]} found, v2.4 expected`);
  const end = 10 + syncsafe(mp3, 6);
  const frames: Record<string, string> = {};
  let p = 10;
  while (p + 10 <= end && mp3[p] !== 0) {
    const id = mp3.subarray(p, p + 4).toString("latin1");
    const size = syncsafe(mp3, p + 4);
    const body = mp3.subarray(p + 10, p + 10 + size);
    if (id.startsWith("T") && body.length > 0) {
      const enc = body[0];
      const text = body.subarray(1);
      frames[id] = (enc === 3 ? text.toString("utf8") : enc === 0 ? text.toString("latin1") : enc === 1 ? text.toString("utf16le") : text.swap16().toString("utf16le")).replace(/\0+$/, "");
    }
    p += 10 + size;
  }
  return frames;
}
