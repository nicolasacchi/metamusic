import { describe, expect, test } from "bun:test";
import { CHUNK, assemble, chunkCount, chunksFor } from "./chunks";

const MiB = 1024 * 1024;

describe("chunksFor", () => {
  test("a range inside one chunk", () => {
    expect(chunksFor(0, 10)).toEqual([0]);
  });
  test("0..4 MiB-1 spans two chunks", () => {
    expect(chunksFor(0, 4 * MiB - 1)).toEqual([0, 1]);
  });
  test("a range across the boundary", () => {
    expect(chunksFor(2 * MiB - 1, 2 * MiB)).toEqual([0, 1]);
  });
  test("chunk size is 2 MiB", () => {
    expect(CHUNK).toBe(2 * MiB);
  });
});

describe("chunkCount", () => {
  test("rounds up", () => {
    expect(chunkCount(1)).toBe(1);
    expect(chunkCount(2 * MiB)).toBe(1);
    expect(chunkCount(2 * MiB + 1)).toBe(2);
    expect(chunkCount(0)).toBe(0);
  });
});

describe("assemble", () => {
  const size = 5 * MiB + 123;
  const source = new Uint8Array(size).map((_, i) => (i * 31 + 7) & 0xff);
  const chunk = (n: number) => source.slice(n * CHUNK, Math.min(size, (n + 1) * CHUNK));
  test("across a chunk boundary equals the source slice", () => {
    const start = 2 * MiB - 100;
    const end = 2 * MiB + 99;
    const parts = chunksFor(start, end).map(chunk);
    expect(assemble(parts, start, end)).toEqual(source.slice(start, end + 1));
  });
  test("across three chunks, ending in the short last chunk", () => {
    const start = 3 * MiB;
    const end = size - 1;
    const parts = chunksFor(start, end).map(chunk);
    expect(assemble(parts, start, end)).toEqual(source.slice(start, end + 1));
  });
  test("single chunk", () => {
    expect(assemble([chunk(0)], 5, 9)).toEqual(source.slice(5, 10));
  });
});
