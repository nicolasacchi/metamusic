// Synthetic transform fixtures for bun tests (no real titles, ids or lyrics).
import type { components } from "../../../api/schema";

type S = components["schemas"];
// The generated type of `default` is a placeholder object; on the wire it is a number or a string.
type PO = Omit<Partial<S["TransformParamCap"]>, "default"> & { default?: number | string };
const p = (name: string, type: string, o: PO = {}): S["TransformParamCap"] => ({ name, type, required: false, ...o, default: o.default as never });

const kind = (k: string, params: S["TransformParamCap"][], inputs = 1): S["TransformKindCaps"] => ({
  kind: k,
  summary: `${k} summary`,
  inputs,
  params,
  blind_spots: [`${k} blind spot`],
  example: {},
});

export const capsKinds: S["TransformKindCaps"][] = [
  kind(
    "morph",
    [
      p("track_a_id", "string", { required: true }),
      p("track_b_id", "string", { required: true }),
      p("bars", "integer", { min: 1, max: 32, default: 8 }),
      p("a_at", "object"),
      p("b_at", "object"),
      p("bpm_a", "number", { min: 40, max: 240 }),
      p("bpm_b", "number", { min: 40, max: 240 }),
      p("beats_per_bar", "integer", { min: 2, max: 7, default: 4 }),
      p("downbeat_a_s", "number", { min: 0 }),
      p("downbeat_b_s", "number", { min: 0 }),
      p("key_policy", "string", { enum: ["to_a", "none"], default: "to_a" }),
      p("semitones_b", "number", { min: -12, max: 12 }),
    ],
    2,
  ),
  kind("glacier", [
    p("source_id", "string", { required: true }),
    p("factor", "number", { min: 10, max: 50, default: 20 }),
    p("start_s", "number", { min: 0 }),
    p("length_s", "number", { min: 0 }),
    p("window_s", "number", { min: 0.1, max: 2, default: 0.5 }),
    p("fade_in_s", "number", { min: 0, max: 30, default: 4 }),
    p("fade_out_s", "number", { min: 0, max: 30, default: 4 }),
  ]),
  kind("ensemble", [
    p("source_id", "string", { required: true }),
    p("space", "string", { enum: ["cathedral", "cistern", "hangar", "canyon"], default: "cathedral" }),
    p("wet_db", "number", { min: -40, max: 0, default: -12 }),
    p("dry_db", "number", { min: -40, max: 0 }),
    p("tail_s", "number", { min: 0, max: 40, default: 8 }),
  ]),
  kind("master", [
    p("source_id", "string", { required: true }),
    p("preset", "string", { enum: ["neutral", "warm", "bright", "punch"], default: "neutral" }),
    p("tilt_db", "number", { min: -3, max: 3 }),
    p("comp_amount", "number", { min: 0, max: 1 }),
    p("low_cut_hz", "integer", { min: 0, max: 40 }),
    p("target_lufs", "number", { min: -30, max: -12, default: -14 }),
    p("ceiling_dbtp", "number", { min: -3, max: -1, default: -1 }),
  ]),
  kind("widen", [p("source_id", "string", { required: true }), p("width", "number", { min: 1, max: 2 }), p("decorrelate", "number", { min: 0, max: 0.5 }), p("bass_mono_hz", "integer", { min: 0, max: 250 })]),
  { ...kind("binaural", [p("source_id", "string", { required: true }), p("model", "string", { enum: ["hrtf", "parametric"] }), p("elevation_deg", "integer", { min: 0, max: 30 })]), listening: "headphones", dataset: "Synthetic HRTF credit" },
  kind("shepard", [p("source_id", "string", { required: true }), p("direction", "string", { enum: ["up", "down"] }), p("layers", "integer", { min: 3, max: 7, default: 5 })]),
  kind("polytempo", [p("track_a_id", "string", { required: true }), p("track_b_id", "string", { required: true }), p("downbeat_a_s", "number", { min: 0 }), p("downbeat_b_s", "number", { min: 0 })], 2),
];

export const quota: S["TransformQuota"] = { cap_ms: 600000, used_ms: 120000, reserved_ms: 30000, principal_kind: "human", resets_at: "2026-11-04T00:00:00Z" };

export const caps: S["TransformCapabilities"] = {
  enabled: true,
  kinds: capsKinds,
  limits: { concurrent_renders: 1, max_cpu_s: 300, max_input_s: 600, max_inputs: 2, max_mem_mb: 2048, max_output_s: 3600 },
  quota,
  queue: { running: 0, queued: 0, max: 8 },
  soundfonts: [],
  tools: { ffmpeg: true, flac: true, fluidsynth: { available: false, version: null } },
  dsp_version: "mm-dsp/1",
};

export const estimate: S["TransformEstimate"] = {
  kind: "glacier",
  spec_hash: "0123456789abcdef0123",
  title: "Glacier of a take",
  cost: { micro_usd: 0, usd: "0.000000" },
  cpu: { expected_ms: 14000, max_ms: 33000, budget_ms: 33000, model: "cpu/1" },
  wall_expected_s: 14,
  output: { seconds: 200, sample_rate: 48000, bits: 24, channels: 2, bytes_est: 57600000 },
  memory_mb: 180,
  inputs: [
    {
      role: "source",
      track_id: "t1",
      title: "Source take",
      duration_s: 10,
      beats_per_bar: 4,
      bpm: { value: 92, source: "declared", octave_fold: 0, confidence: null },
      key: { camelot: "8A", pc: 9, minor: true, source: "measured" },
    },
  ],
  resolved: {},
  warnings: [{ code: "beat_phase_pending", field: "", message: "The local beat grid is measured while rendering." }],
  quota,
  queue: { running: 0, queued: 1, position_if_created: 2 },
  existing: null,
  tools: { ffmpeg: true, flac: true, fluidsynth: false, soundfont: null },
};

export const view = (state: S["TransformView"]["state"], o: Partial<S["TransformView"]> = {}): S["TransformView"] => ({
  id: "x1",
  take_id: "x1",
  kind: "glacier",
  state,
  phase: state === "running" ? "render" : null,
  progress_permille: state === "done" ? 1000 : state === "running" ? 420 : 0,
  title: "Glacier of a take",
  spec_hash: "0123456789abcdef0123",
  reused: false,
  principal: { id: "human", kind: "human", name: "human" },
  rationale: null,
  inputs: [{ role: "source", track_id: "t1", title: "Source take" }],
  cost: { micro_usd: 0, usd: "0.000000" },
  cpu: { expected_ms: 14000, max_ms: 33000, used_ms: state === "done" ? 11000 : null },
  output_seconds_est: 200,
  restored: false,
  warnings: [],
  error: null,
  cancel_requested_at: null,
  created_at: "2026-11-03T10:00:00Z",
  started_at: null,
  finished_at: null,
  ...o,
});
