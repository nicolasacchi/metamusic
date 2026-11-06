import { describe, expect, test } from "bun:test";
import { render } from "svelte/server";
import VoiceRecorder from "./components/VoiceRecorder.svelte";
import SmartRound from "./components/SmartRound.svelte";
import ReceiptsTable from "./components/ReceiptsTable.svelte";
import CallbackCard from "./components/CallbackCard.svelte";

const html = (c: unknown, props: Record<string, unknown>) => render(c as never, { props } as never).body;

describe("W2 components render without a browser", () => {
  test("the recorder says why it cannot record and offers no upload", () => {
    const h = html(VoiceRecorder, { trackId: "t1", atSecond: 3 });
    expect(h).toContain('data-testid="voice-recorder"');
    expect(h).toContain('data-testid="voice-unsupported"');
    expect(h).not.toContain("voice-save");
  });
  test("the smart round starts blind with the section picker and no pairs", () => {
    const h = html(SmartRound, { navigate: () => {} });
    expect(h).toContain('data-testid="smart-section"');
    expect(h).toContain('data-testid="smart-blind"');
    expect(h).toMatch(/data-testid="smart-blind"[^>]*checked|checked[^>]*data-testid="smart-blind"/);
    expect(h).not.toContain("smart-pairs");
  });
  test("receipts and the callback card start in their loading states", () => {
    expect(html(ReceiptsTable, {})).toContain('aria-busy="true"');
    expect(html(CallbackCard, { id: "c1", navigate: () => {} })).toContain('data-callback="c1"');
  });
});
