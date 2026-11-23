import { describe, expect, test } from "bun:test";
import { ROUTE_OPS, routeAllowed } from "./nav";
import type { components } from "../api/schema";
import {
  POD_MAX_TAKES,
  POD_MIN_TAKES_FLOOR,
  bulkFailureText,
  bulkOffer,
  bulkOptionLine,
  shouldPollPods,
  statusAskedAfter,
  durationText,
  isPodEstimate,
  livePods,
  podAlerts,
  podBackendOf,
  podCostText,
  podErrorCodes,
  podLine,
  podTone,
  podsOff,
  type StatusLike,
} from "./bulkView";

type Pod = components["schemas"]["PodSummary"];

function pod(over: Partial<Pod> = {}): Pod {
  return {
    id: "p1",
    name: "mm-0123456789ab",
    kind: "batch",
    batch_id: "b1",
    state: "ready",
    gpu_try: 1,
    created_at: "2026-11-22T10:00:00Z",
    booted_at: "2026-11-22T10:05:00Z",
    first_poll_at: "2026-11-22T10:06:00Z",
    last_poll_at: "2026-11-22T10:20:00Z",
    deadline_at: "2026-11-22T18:00:00Z",
    terminated_at: null,
    lifetime_s: null,
    error_code: null,
    cost: { reserved_micro_usd: 1_500_000, reserved_usd: "1.50", settled_micro_usd: null, settled_usd: null },
    ...over,
  };
}

function status(over: Partial<StatusLike["backends"][number]> = {}, withPods = true): StatusLike {
  return {
    backends: [
      { backend: "runpod", generation_enabled: true, armed: true },
      { backend: "runpod-pod", generation_enabled: true, armed: true, ...(withPods ? { pods: podsBlock() } : {}), ...over },
    ],
  };
}
function podsBlock(over: Partial<NonNullable<StatusLike["backends"][number]["pods"]>> = {}) {
  return { key_present: true, probe: "ok" as const, safety: "verified" as const, safety_digest_matches: true, live: [], window: { armed_at: "2026-11-22T09:00:00Z", auto_disarm_at: "2026-11-22T17:00:00Z" }, alerts: [], ...over };
}

describe("podBackendOf", () => {
  test("the backend that carries a pods block, else null", () => {
    expect(podBackendOf(status())?.backend).toBe("runpod-pod");
    expect(podBackendOf(status({}, false))).toBeNull();
    expect(podBackendOf(null)).toBeNull();
    expect(podBackendOf({ backends: [] })).toBeNull();
  });
});

describe("bulkOffer", () => {
  test("nothing for a caller who may not read the provider status, or for a small batch", () => {
    expect(bulkOffer(status(), 30, false).kind).toBe("hidden");
    expect(bulkOffer(status(), POD_MIN_TAKES_FLOOR - 1, true).kind).toBe("hidden");
    expect(bulkOffer(null, 30, true).kind).toBe("hidden");
  });
  test("without a pod backend the offer says pods are off until the operator sets them up", () => {
    const o = bulkOffer(status({}, false), 30, true);
    expect(o.kind).toBe("off");
    expect(o.text).toContain("off");
    expect(o.text).toContain("operator");
    expect(o.backend).toBe("");
  });
  test("a disabled or disarmed pod backend is named, and not offered", () => {
    expect(bulkOffer(status({ generation_enabled: false }), 30, true).kind).toBe("disabled");
    const d = bulkOffer(status({ armed: false }), 30, true);
    expect(d.kind).toBe("disarmed");
    expect(d.text).toContain("arm");
  });
  test("an armed pod backend is offered with its name, and the safety state is a note", () => {
    const ok = bulkOffer(status(), 30, true);
    expect(ok.kind).toBe("ready");
    expect(ok.backend).toBe("runpod-pod");
    expect(ok.note).toBe("");
    const unsafe = bulkOffer(status({ pods: podsBlock({ safety: "unverified" }) }), 30, true);
    expect(unsafe.kind).toBe("ready");
    expect(unsafe.note).toContain("self-termination");
  });
  test("the take bounds are the schema bounds of MM_POD_MIN_TAKES and MM_POD_MAX_TAKES", () => {
    expect(POD_MIN_TAKES_FLOOR).toBe(10);
    expect(POD_MAX_TAKES).toBe(100);
    expect(bulkOffer(status(), POD_MIN_TAKES_FLOOR, true).kind).toBe("ready");
    expect(bulkOffer(status(), POD_MAX_TAKES + 1, true).kind).toBe("toomany");
  });
});

describe("estimate and failure text", () => {
  test("a pod estimate carries a pod block", () => {
    expect(isPodEstimate({ pod: { boot_s: 1200, deadline_s: 7200 } })).toBe(true);
    expect(isPodEstimate({})).toBe(false);
  });
  test("durations read in minutes and hours", () => {
    expect(durationText(45)).toBe("45 s");
    expect(durationText(1200)).toBe("20 min");
    expect(durationText(7200)).toBe("2 h");
    expect(durationText(5400)).toBe("1 h 30 min");
  });
  test("the refusals of a pod estimate are said in words", () => {
    expect(bulkFailureText("BULK_TOO_SMALL", { takes: 10, minimum: 30 })).toContain("at least 30");
    expect(bulkFailureText("BATCH_TOO_LARGE", { takes: 120, limit: 100 })).toContain("at most 100");
    expect(bulkFailureText("BATCH_TOO_LARGE", { reason: "pod_runtime", max_runtime_s: 21600 })).toContain("6 h");
    expect(bulkFailureText("GENERATION_DISABLED", { reason: "pod_safety_unverified" })).toContain("self-termination");
    expect(bulkFailureText("BACKEND_UNAVAILABLE", {})).toContain("off");
    expect(bulkFailureText("OWNER_ONLY", { reason: "bulk" })).toContain("Owner");
    expect(bulkFailureText("SOMETHING_ELSE", {})).toBeNull();
  });
  test("the money of a pod is integer micro-USD shown as dollars", () => {
    expect(podCostText(pod())).toBe("$1.50 reserved");
    expect(podCostText(pod({ cost: { reserved_micro_usd: 1_500_000, reserved_usd: "1.50", settled_micro_usd: 1_204_000, settled_usd: "1.20" } }))).toBe("$1.20 settled of $1.50 reserved");
  });
});

describe("pods", () => {
  test("live pods are the ones not yet terminated, and a terminated pod is not live", () => {
    const rows = [pod({ id: "a" }), pod({ id: "b", state: "terminated" }), pod({ id: "c", state: "terminating" })];
    expect(livePods(rows).map((p) => p.id)).toEqual(["a", "c"]);
  });
  test("tones", () => {
    expect(podTone(pod({ state: "ready" }))).toBe("ok");
    expect(podTone(pod({ state: "orphaned" }))).toBe("bad");
    expect(podTone(pod({ error_code: "POD_DEADMAN" }))).toBe("bad");
    expect(podTone(pod({ state: "terminated" }))).toBe("idle");
    expect(podTone(pod({ state: "booting" }))).toBe("busy");
  });
  test("a pod line names state, last poll and deadline", () => {
    const l = podLine(pod(), new Date("2026-11-22T10:25:00Z"));
    expect(l).toContain("ready");
    expect(l).toContain("last poll 5 min ago");
    expect(l).toContain("deadline");
    expect(podLine(pod({ last_poll_at: null }), new Date("2026-11-22T10:25:00Z"))).toContain("no poll yet");
  });
  test("the error codes of a batch's pods are distinct", () => {
    expect(podErrorCodes([pod(), pod({ error_code: "POD_DEADMAN" }), pod({ error_code: "POD_DEADMAN" }), pod({ error_code: "NO_CAPACITY" })])).toEqual(["POD_DEADMAN", "NO_CAPACITY"]);
    expect(podErrorCodes([pod()])).toEqual([]);
  });
  test("alerts and the off notice", () => {
    expect(podAlerts(status({ pods: podsBlock({ alerts: ["pod_deadman"] }) }))).toEqual(["pod_deadman"]);
    expect(podAlerts(null)).toEqual([]);
    expect(podsOff(status({}, false))).toBe(true);
    expect(podsOff(status())).toBe(false);
    expect(podsOff(null)).toBe(false);
  });
});

describe("route gating of the pod blocks", () => {
  test("Jobs and Batch stay Editor pages: list_pods is Owner-only and gates the blocks, not the routes", () => {
    expect(ROUTE_OPS.jobs).toBe("list_jobs");
    expect(ROUTE_OPS.batch).toBe("get_batch");
    const editor = (op: string) => op === "list_jobs" || op === "get_batch";
    expect(routeAllowed("jobs", editor)).toBe(true);
    expect(routeAllowed("batch", editor)).toBe(true);
  });
});

describe("shouldPollPods", () => {
  const base = { canList: true, answered: true };
  const cases: { name: string; in: Parameters<typeof shouldPollPods>[0]; want: boolean }[] = [
    { name: "queued/active + empty answer keeps polling (the pod row may not exist yet)", in: { ...base, batchState: "active" }, want: true },
    { name: "awaiting_confirmation keeps polling", in: { ...base, batchState: "awaiting_confirmation" }, want: true },
    { name: "active + pods keeps polling", in: { ...base, batchState: "active", podBackend: true }, want: true },
    { name: "finished stops once answered", in: { ...base, batchState: "finished" }, want: false },
    { name: "rejected stops once answered", in: { ...base, batchState: "rejected" }, want: false },
    { name: "expired stops once answered", in: { ...base, batchState: "expired" }, want: false },
    { name: "a terminal batch never asked is asked once", in: { canList: true, answered: false, batchState: "finished" }, want: true },
    { name: "non-pod backend never polls", in: { ...base, batchState: "active", podBackend: false }, want: false },
    { name: "non-pod backend never polls, even unasked", in: { canList: true, answered: false, batchState: "active", podBackend: false }, want: false },
    { name: "a viewer who cannot list pods never asks", in: { canList: false, answered: false, batchState: "active" }, want: false },
    { name: "no batch loaded yet: no ask", in: { ...base, batchState: undefined }, want: false },
  ];
  for (const c of cases) test(c.name, () => expect(shouldPollPods(c.in)).toBe(c.want));
});

describe("statusAskedAfter", () => {
  test("a real answer stays asked; failed or empty resets so the next try asks again", () => {
    expect(statusAskedAfter({ backends: [] })).toBe(true);
    expect(statusAskedAfter(undefined)).toBe(false);
    expect(statusAskedAfter(null)).toBe(false);
  });
});

describe("bulkOptionLine", () => {
  const hidden = bulkOffer(null, 0, false);
  test("hidden offer with the choice on says the price needs the status, never empty text", () => {
    expect(bulkOptionLine(hidden, true)).toContain("needs the provider status");
  });
  test("hidden offer with the choice off has no line", () => {
    expect(bulkOptionLine(hidden, false)).toBe("");
  });
  test("an offer with text shows its own text", () => {
    const off = bulkOffer({ backends: [] }, 30, true);
    expect(bulkOptionLine(off, true)).toBe(off.text);
    expect(off.text).not.toBe("");
  });
});
