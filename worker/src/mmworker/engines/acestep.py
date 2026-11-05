"""The ACE-Step 1.5 engine (engine id acestep15) behind the adapter protocol (C154.4, E2a + E2b).

Importing this module needs neither torch nor ACE-Step: the real ACE-Step API is imported lazily inside
`load_backend`, and everything else talks to a `Backend` (the real one, or `FakeAceStep` in tests and selftest).

Facts this module encodes (E2-P0a, pinned ACE-Step v0.1.8):
  * the adapter, not ACE-Step, supplies the instruction string of every task and enforces which DiT class may run
    which task (turbo: text2music, repaint, extend; base: complete, extract);
  * `initialize_service` overwrites the `.py` files of `checkpoints/<dit>/`, so each DiT directory is a real
    directory: weights symlinked in, the pinned code's `.py` files copied in (nothing is written afterwards);
  * ACE-Step does not clamp the duration: MM_ACESTEP_MAX_SECONDS is the guard;
  * audio is encoded here from the float tensor as 24-bit FLAC (the upstream saver would write 16-bit);
  * a silent reference clip would be dropped silently upstream, a clip that is not exactly 30.000 s at 48 kHz would be
    re-sampled with the unseeded global RNG: both are refused before any render;
  * at v0.1.8 `complete` does not skip the LM, so every non-text2music task renders with thinking and all
    use_cot_* false.
"""

from __future__ import annotations

import json
import math
import os
import re
import shutil
import sys
import time
from collections.abc import Callable, Mapping, Sequence
from dataclasses import dataclass, field
from importlib import metadata, resources
from pathlib import Path
from typing import TYPE_CHECKING, Any, Protocol

from .. import env as envmod
from .. import models, redact
from ..errors import InitError, WorkerError

if TYPE_CHECKING:
    from ..contract import TakeInput

ENGINE = "acestep15"
REPO_MAIN = "ACE-Step/Ace-Step1.5"
REPO_BASE = "ACE-Step/acestep-v15-base"
DECODER = REPO_MAIN + ":vae"

ENV_TASKS = "MM_ACESTEP_TASKS"
ENV_MAIN_REV = "MM_ACESTEP_MAIN_REVISION"
ENV_BASE_REV = "MM_ACESTEP_BASE_REVISION"
ENV_MAX_SECONDS = "MM_ACESTEP_MAX_SECONDS"
ENV_MODELS_DIR = "MM_ACESTEP_MODELS_DIR"
ENV_ROOT = "MM_ACESTEP_ROOT"
ENV_CODE_DIR = "MM_ACESTEP_MODELS_CODE_DIR"
DEFAULT_ROOT = "/tmp/acestep"  # noqa: S108 - a per-process scratch tree, rebuilt at every start
DEFAULT_MAX_SECONDS = 480  # 20..24 GB tier; 600 only on larger cards (preflight enforces)
SAMPLE_RATE = 48000
REF_FRAMES = 30 * SAMPLE_RATE  # a reference clip passes upstream untouched only at exactly this length
LRC_SEED = 42  # the seed the upstream UI uses for the alignment pass
LRC_MAX_BYTES = 1 << 20
_LRC_LINE = re.compile(r"^\[\d{2,}:\d{2}\.\d{2,3}\]")
_REV = re.compile(r"[0-9a-f]{40}")

# task -> DiT class that renders it. `extend` is a repaint over silence appended to the source (turbo).
TASK_CLASS = {"text2music": "turbo", "repaint": "turbo", "extend": "turbo", "complete": "base", "extract": "base"}
# Tasks this build renders, and whether it runs the LRC alignment pass. E2-PW2 shipped text2music and repaint only.
ENABLED_TASKS = frozenset(TASK_CLASS)
LRC_ENABLED = True
# task -> ACE-Step task_type
UPSTREAM_TASK = {"text2music": "text2music", "repaint": "repaint", "extend": "repaint", "complete": "complete",
                 "extract": "extract"}
INSTRUCTIONS = {  # acestep/constants.py TASK_INSTRUCTIONS at the pinned commit (the contract job compares them)
    "text2music": "Fill the audio semantic mask based on the given conditions:",
    "repaint": "Repaint the mask area based on the given conditions:",
    "extract": "Extract the {TRACK_NAME} track from the audio:",
    "complete": "Complete the input track with {TRACK_CLASSES}:",
}
TRACK_NAMES = ("backing_vocals", "bass", "brass", "drums", "fx", "guitar", "keyboard", "percussion", "strings",
               "synth", "vocals", "woodwinds")

# engine_params.json key -> GenerationParams field. Every key of the pinned version must be listed in exactly one
# of these tables (or in LOAD_KEYS): an unknown key is an error, never silently dropped.
DIT_FIELDS = {k: k for k in (
    "shift", "use_adg", "cfg_interval_start", "cfg_interval_end", "timesteps", "chunk_mask_mode", "infer_method",
    "sampler_mode", "velocity_norm_threshold", "velocity_ema_factor", "dcw_enabled", "dcw_mode", "dcw_scaler",
    "dcw_high_scaler", "dcw_wavelet", "enable_normalization", "normalization_db", "fade_in_duration",
    "fade_out_duration", "latent_shift", "latent_rescale")}
CONFIG_FIELDS = {"batch_size": "batch_size"}  # GenerationConfig
LM_FIELDS = {"temperature": "lm_temperature", "top_p": "lm_top_p", "top_k": "lm_top_k", "cfg_scale": "lm_cfg_scale",
             "negative_prompt": "lm_negative_prompt", "use_cot_metas": "use_cot_metas",
             "use_cot_caption": "use_cot_caption", "use_cot_language": "use_cot_language",
             "constrained_decoding": "use_constrained_decoding"}
LOAD_KEYS = {"lm": ("backend", "model", "gpu_memory_utilization")}  # recorded, passed at load / not passable
META_KEYS = ("acestep_commit",)
COT_KEYS = ("use_cot_metas", "use_cot_caption", "use_cot_language")


class Cancelled(Exception):
    """Raised from the progress callback when the handler's cancelled() turns true (deadline, 410)."""


# ---------------------------------------------------------------------------------------------------------------
# package data

def _data(name: str) -> Any:
    return json.loads(resources.files(__package__).joinpath(name).read_text())


def layout() -> dict[str, Any]:
    return _data("acestep_layout.json")  # type: ignore[no-any-return]


def memory_budget() -> dict[str, Any]:
    return _data("acestep_memory_budget.json")  # type: ignore[no-any-return]


def params_file() -> dict[str, Any]:
    """engine_params.json of the Go side (internal/engine/acestep), copied byte for byte (a test pins equality)."""
    return _data("acestep_params.json")  # type: ignore[no-any-return]


def params_version(version: str) -> dict[str, Any]:
    versions = params_file()["versions"]
    if version not in versions:
        raise ValueError(f"engine_params_version {version!r} is not known to this worker")
    return versions[version]  # type: ignore[no-any-return]


def check_params(p: Mapping[str, Any]) -> None:
    """Every key of an engine_params version is mapped (nothing is left to an upstream default)."""
    known = {"dit": set(DIT_FIELDS) | set(CONFIG_FIELDS), "lm": set(LM_FIELDS) | set(LOAD_KEYS["lm"]),
             "classes": {"turbo", "base"}}
    for section, keys in known.items():
        extra = set(p.get(section, {})) - keys
        if extra:
            raise ValueError(f"engine_params {section}.{sorted(extra)[0]} has no mapping in the worker")
        missing = keys - set(p.get(section, {}))
        if missing:
            raise ValueError(f"engine_params {section}.{sorted(missing)[0]} is missing")
    unknown = set(p) - set(known) - set(META_KEYS)
    if unknown:
        raise ValueError(f"engine_params has an unmapped section {sorted(unknown)[0]}")


def _pick(value: Any, *keys: str) -> Any:
    """A per-class / per-task object ({"turbo": .., "base": ..} or {"default": .., "repaint": ..}) resolves to the
    first of `keys` it has, else its "default"; any other value is returned as is."""
    if isinstance(value, dict):
        for k in keys:
            if k in value:
                return value[k]
        if "default" in value:
            return value["default"]
        raise ValueError(f"no value for {keys} in {sorted(value)}")
    return value


# ---------------------------------------------------------------------------------------------------------------
# configuration

def tasks_of(env: Mapping[str, str]) -> tuple[str, ...]:
    raw = env.get(ENV_TASKS, "turbo") or "turbo"
    parts = sorted({p.strip() for p in raw.split(",")})
    if parts == ["turbo"]:
        return ("turbo",)
    if parts == ["base", "turbo"]:
        return ("turbo", "base")
    raise InitError("ENV_INVALID", f"{ENV_TASKS}={raw!r} must be turbo or turbo,base")


def max_seconds_of(env: Mapping[str, str]) -> int:
    raw = env.get(ENV_MAX_SECONDS)
    if raw is None or raw == "":
        return DEFAULT_MAX_SECONDS
    try:
        v = int(raw)
    except ValueError:
        v = 0
    if not 30 <= v <= 600:
        raise InitError("ENV_INVALID", f"{ENV_MAX_SECONDS} must be an integer in 30..600")
    return v


def specs_for(classes: Sequence[str], env: Mapping[str, str]) -> list[models.ModelSpec]:
    lay = layout()["manifests"]
    specs = []
    for cls, repo, var in (("turbo", REPO_MAIN, ENV_MAIN_REV), ("base", REPO_BASE, ENV_BASE_REV)):
        if cls != "turbo" and cls not in classes:
            continue
        rev = env.get(var, "")
        if not _REV.fullmatch(rev):
            raise InitError("ENV_INVALID", f"{var} must be a 40-hex revision")
        specs.append(models.ModelSpec(repo, rev, tuple(lay["main" if cls == "turbo" else "base"]),
                                      env_var=ENV_MODELS_DIR, label="ACE-Step model"))
    return specs


def _cuda_info() -> tuple[str, float, str]:  # pragma: no cover - needs torch and a GPU
    """(cuda version, total VRAM in GiB, device name); InitError NO_GPU when unusable."""
    try:
        import torch  # noqa: PLC0415
    except ImportError as e:
        raise InitError("NO_GPU", f"torch is not importable: {e}") from None
    if not torch.cuda.is_available():
        raise InitError("NO_GPU", "no CUDA device is visible")
    cuda = str(torch.version.cuda or "")
    props = torch.cuda.get_device_properties(0)
    return cuda, props.total_memory / (1 << 30), str(props.name)


def _cuda_ok(version: str) -> bool:
    try:
        major, minor = (int(x) for x in version.split(".")[:2])
    except ValueError:
        return False
    return (major, minor) >= (12, 8)


def budget_gib(budget: Mapping[str, Any], classes: Sequence[str], max_seconds: int) -> float:
    """Σ of the measured memory terms for the loaded classes at the maximum duration (D-E2-4)."""
    st = budget["static_gib"]
    total = sum(float(v) for k, v in st.items() if k != "base_dit")
    if "base" in classes:
        total += float(st["base_dit"])
    return total + float(budget["activation_gib_per_s"]) * max_seconds


def preflight(env: Mapping[str, str]) -> None:
    """Cheap checks in seconds, before any model I/O (C154.5): configuration, CUDA >= 12.8, memory budget."""
    classes = tasks_of(env)
    specs_for(classes, env)  # revisions present and well-formed
    max_s = max_seconds_of(env)
    check_params(params_version(params_file()["current"]))
    cuda, vram_gib, _ = _cuda_info()
    if not _cuda_ok(cuda):
        raise InitError("NO_GPU", f"CUDA {cuda or 'unknown'} is older than 12.8")
    if max_s > DEFAULT_MAX_SECONDS and vram_gib <= 24.0:
        raise InitError("NO_MEMORY", f"{ENV_MAX_SECONDS}={max_s} needs a card above 24 GB (this one has "
                        f"{vram_gib:.1f} GiB); 480 is the cap for 20-24 GB cards")
    budget = memory_budget()
    if not budget.get("measured"):
        print("mmworker: acestep memory budget is not measured yet; the NO_MEMORY check is skipped "
              f"(visible VRAM {vram_gib:.1f} GiB, tasks {','.join(classes)}, max {max_s} s)", file=sys.stderr)
        return
    need = budget_gib(budget, classes, max_s)
    limit = float(budget["vram_fraction"]) * vram_gib
    if need > limit:
        raise InitError("NO_MEMORY", f"the memory budget {need:.1f} GiB exceeds {budget['vram_fraction']:.2f} x "
                        f"{vram_gib:.1f} GiB at {max_s} s with {','.join(classes)}")


# ---------------------------------------------------------------------------------------------------------------
# checkpoints tree

def build_checkpoints(root: Path, snaps: Mapping[str, Path], classes: Sequence[str], code_dir: Path) -> list[str]:
    """Build `root` (the checkpoints directory ACE-Step reads) from the resolved snapshots.

    Large read-only directories (vae, the Qwen3 encoder, the LM) are plain symlinks. Each DiT directory is a REAL
    directory: its weights are symlinked and the pinned code's `.py` files are copied in, because
    `initialize_service` syncs those files and would otherwise write into a read-only (or blob-backed) mount."""
    lay = layout()
    main = snaps[REPO_MAIN]
    shutil.rmtree(root, ignore_errors=True)
    root.mkdir(parents=True)
    made: list[str] = []
    for name in lay["main_links"]:
        _link(root / name, main / name)
        made.append(name)
    for cls in classes:
        spec = lay["dits"][cls]
        snap = snaps[REPO_MAIN if spec["repo"] == "main" else REPO_BASE]
        d = root / spec["dir"]
        d.mkdir()
        for f in spec["files"]:
            _link(d / f, snap / spec["src"] / f if spec["src"] else snap / f)
        sources = sorted(p for p in (code_dir / spec["variant"]).glob("*.py") if p.name != "__init__.py")
        if not sources:
            raise RuntimeError(f"no model code for variant {spec['variant']} under the pinned ACE-Step source")
        for p in sources:
            shutil.copy2(p, d / p.name)
        made.append(spec["dir"])
    return made


def _link(dst: Path, target: Path) -> None:
    if not target.exists():
        raise FileNotFoundError(f"checkpoint layout target is missing: {target}")
    dst.symlink_to(target)


def code_models_dir(env: Mapping[str, str]) -> Path:  # pragma: no cover - needs the installed ACE-Step
    if env.get(ENV_CODE_DIR):
        return Path(env[ENV_CODE_DIR])
    import importlib.util  # noqa: PLC0415

    spec = importlib.util.find_spec("acestep")
    if spec is None or not spec.submodule_search_locations:
        raise RuntimeError("the acestep package is not installed")
    return Path(next(iter(spec.submodule_search_locations))) / "models"


# ---------------------------------------------------------------------------------------------------------------
# the backend seam

class Backend(Protocol):
    dits: Mapping[str, Any]  # class -> DiT handler (has get_lyric_timestamp)
    llm: Any

    def make_params(self, **kw: Any) -> Any: ...

    def make_config(self, **kw: Any) -> Any: ...

    def generate(self, dit: Any, llm: Any, params: Any, config: Any, progress: Callable[..., Any]) -> Any:
        """acestep.inference.generate_music with save_dir=None (no file is written by ACE-Step)."""


def load_backend(env: Mapping[str, str], classes: Sequence[str], root: Path) -> Backend:  # pragma: no cover
    """Import the real ACE-Step API and initialise the DiT handler(s) and the LM, in the upstream order (DiTs first,
    the LM last: its vllm reservation is derived from the free VRAM). Needs torch, a GPU and the weights."""
    from acestep.handler import AceStepHandler  # noqa: PLC0415
    from acestep.inference import GenerationConfig, GenerationParams, generate_music  # noqa: PLC0415
    from acestep.llm_inference import LLMHandler  # noqa: PLC0415

    lay = layout()
    lm = _data("acestep_params.json")["versions"][params_file()["current"]]["lm"]
    dits = {cls: AceStepHandler() for cls in classes}
    return _Real(dits, LLMHandler(), GenerationParams, GenerationConfig, generate_music, root, lay, lm)


class _Real:  # pragma: no cover - exercised by the contract job and the canary
    def __init__(self, dits: Mapping[str, Any], llm: Any, params_cls: Any, config_cls: Any, gen: Any, root: Path,
                 lay: Mapping[str, Any], lm: Mapping[str, Any]) -> None:
        self.dits, self.llm = dits, llm
        self._p, self._c, self._gen = params_cls, config_cls, gen
        os.environ["ACESTEP_CHECKPOINTS_DIR"] = str(root / "checkpoints")
        for cls, handler in dits.items():
            status, ok = handler.initialize_service(
                project_root=str(root), config_path=lay["dits"][cls]["dir"], device="cuda",
                use_flash_attention=True, compile_model=False, offload_to_cpu=False, offload_dit_to_cpu=False,
                quantization=None)
            _log(f"dit {cls}: {status}")
            if not ok:
                raise RuntimeError(f"DiT {cls} failed to initialise: {status}")
        status, ok = llm.initialize(checkpoint_dir=str(root / "checkpoints"), lm_model_path=lm["model"],
                                    backend=lm["backend"], device="cuda", offload_to_cpu=False)
        _log(f"lm: {status}")
        if not ok:
            raise RuntimeError(f"the LM failed to initialise: {status}")
        self.lm_gpu_memory_utilization_milli = lm_utilization_milli(status)

    def make_params(self, **kw: Any) -> Any:
        return self._p(**kw)

    def make_config(self, **kw: Any) -> Any:
        return self._c(**kw)

    def generate(self, dit: Any, llm: Any, params: Any, config: Any, progress: Callable[..., Any]) -> Any:
        return self._gen(dit, llm, params, config, save_dir=None, progress=progress)


def lm_utilization_milli(status: str) -> int | None:
    """nano-vllm derives gpu_memory_utilization from the free VRAM at LM init and reports it in the status string
    ("GPU Memory Utilization: 0.412"); it is not a caller parameter, so the worker only records it (P0a item 8)."""
    m = re.search(r"GPU Memory Utilization:\s*([0-9]*\.?[0-9]+)", status or "")
    return round(float(m.group(1)) * 1000) if m else None


def _log(line: str) -> None:
    print("mmworker: " + " ".join(redact.text(line).split())[:400], file=sys.stderr, flush=True)


def load(env: Mapping[str, str]) -> Adapter:  # pragma: no cover - needs torch, ACE-Step, a GPU and weights
    classes = tasks_of(env)
    snaps = models.resolve_all(specs_for(classes, env), env)
    root = Path(env.get(ENV_ROOT) or DEFAULT_ROOT)
    build_checkpoints(root / "checkpoints", snaps, classes, code_models_dir(env))
    backend = load_backend(env, classes, root)
    weights = {REPO_MAIN: env[ENV_MAIN_REV], **({REPO_BASE: env[ENV_BASE_REV]} if "base" in classes else {})}
    return Adapter(backend, weights=weights, classes=frozenset(classes), max_seconds=max_seconds_of(env),
                   gpu=_TorchGpu())


# ---------------------------------------------------------------------------------------------------------------
# validation

_COMMON = {"id", "task", "caption", "lyrics", "instrumental", "vocal_language", "seed", "thinking",
           "engine_params_version", "inference_steps", "bpm", "keyscale", "timesignature", "lrc"}
_REQUIRED = _COMMON - {"bpm", "keyscale", "timesignature", "lrc"}
_TASK_KEYS = {  # (required extras, optional extras)
    "text2music": ({"duration_ds"}, {"ref"}),
    "repaint": ({"src", "repaint_from_ds", "repaint_to_ds"}, {"ref"}),
    "extend": ({"src", "extend_ds"}, set()),
    "complete": ({"src", "tracks", "guidance_milli"}, set()),
    "extract": ({"src", "tracks", "guidance_milli"}, set()),
}
_KEY = re.compile(r"[A-G][#b]? (Major|Minor)")
_LANG = re.compile(r"[a-z]{2,8}")
_ID = re.compile(r"[A-Za-z0-9][A-Za-z0-9_.-]{0,179}")


def _int(r: Mapping[str, Any], key: str, lo: int, hi: int) -> int:
    v = r[key]
    if isinstance(v, bool) or not isinstance(v, int) or not lo <= v <= hi:
        raise ValueError(f"{key} must be an integer in {lo}..{hi}")
    return v


def _obj(r: Mapping[str, Any], key: str, fields: Mapping[str, Any]) -> dict[str, Any]:
    v = r[key]
    if not isinstance(v, dict) or set(v) != set(fields):
        raise ValueError(f"{key} must be an object with exactly {sorted(fields)}")
    return v


@dataclass(frozen=True)
class Plan:
    """What validate() returns: the validated request plus everything render needs, fully explicit."""

    task: str
    cls: str
    request: dict[str, Any] = field(repr=False)
    gen: dict[str, Any]  # GenerationParams kwargs without src_audio / reference_audio (set at render)
    config: dict[str, Any]  # GenerationConfig kwargs
    seconds: float  # expected output length
    src_s: float | None = None
    pad_s: float | None = None  # extend: seconds of silence appended to the source
    ref: bool = False
    lrc: bool = False
    resolved: dict[str, Any] = field(default_factory=dict)

    @property
    def canonical(self) -> bytes:
        return json.dumps(self.request, sort_keys=True, separators=(",", ":"), ensure_ascii=False).encode()


def resolve_params(version: str, cls: str, upstream_task: str, *, thinking: bool) -> tuple[dict[str, Any], dict[str, Any], dict[str, Any]]:
    """(GenerationParams kwargs, GenerationConfig kwargs, resolved record) from an engine_params version. Every key
    is passed. Tasks other than text2music render with thinking and all use_cot_* false (v0.1.8 does not skip the
    LM for complete)."""
    p = params_version(version)
    check_params(p)
    gen: dict[str, Any] = {}
    for key, name in DIT_FIELDS.items():
        gen[name] = _pick(p["dit"][key], upstream_task, cls)
    for key, name in LM_FIELDS.items():
        gen[name] = p["lm"][key]
    if upstream_task != "text2music":
        gen.update({k: False for k in COT_KEYS})
    gen["thinking"] = thinking
    config = {name: p["dit"][key] for key, name in CONFIG_FIELDS.items()}
    resolved = {"engine_params_version": version, "acestep_commit": p.get("acestep_commit"), "class": cls,
                "dit": {k: gen[k] for k in DIT_FIELDS.values()}, "lm": {k: gen[k] for k in LM_FIELDS.values()},
                "thinking": thinking, "batch_size": config["batch_size"],
                "load": {k: p["lm"][k] for k in LOAD_KEYS["lm"]}}
    return gen, config, resolved


def build_plan(request: Mapping[str, Any], classes: frozenset[str], max_seconds: int) -> Plan:
    """The adapter's validation (the schema's request body is checked here, C153.2). ValueError means BAD_INPUT;
    TASK_UNAVAILABLE is a WorkerError."""
    if not isinstance(request, dict):
        raise ValueError("request must be an object")
    task = request.get("task")
    if task not in TASK_CLASS:
        raise ValueError(f"unknown task {task!r}")
    cls = TASK_CLASS[task]
    if task not in ENABLED_TASKS:
        raise WorkerError("TASK_UNAVAILABLE", f"task {task} is not supported by this worker build")
    if cls not in classes:  # upstream never refuses a task for a DiT: this is the only guard
        raise WorkerError("TASK_UNAVAILABLE", f"task {task} needs the {cls} model, which this worker did not load")
    req, opt = _TASK_KEYS[task]
    extra = set(request) - _COMMON - req - opt
    if extra:
        raise ValueError(f"unknown or forbidden key {sorted(extra)[0]} for task {task}")
    absent = (_REQUIRED | req) - set(request)
    if absent:
        raise ValueError(f"missing {sorted(absent)[0]}")
    if not isinstance(request["id"], str) or not _ID.fullmatch(request["id"]):
        raise ValueError("id is invalid")
    for k in ("caption", "lyrics", "vocal_language", "engine_params_version"):
        if not isinstance(request[k], str):
            raise ValueError(f"{k} must be a string")
    if not 1 <= len(request["caption"]) <= 1000:
        raise ValueError("caption must be 1..1000 characters")
    if len(request["lyrics"].encode()) > 65536:
        raise ValueError("lyrics are too long")
    if not _LANG.fullmatch(request["vocal_language"]):
        raise ValueError("vocal_language is invalid")
    for k in ("instrumental", "thinking"):
        if not isinstance(request[k], bool):
            raise ValueError(f"{k} must be a boolean")
    if request.get("lrc", False) is not False and not LRC_ENABLED:
        raise WorkerError("TASK_UNAVAILABLE", "lrc is not supported by this worker build")
    lrc = request.get("lrc", False)
    if not isinstance(lrc, bool):
        raise ValueError("lrc must be a boolean")
    if request["instrumental"] and request["lyrics"]:
        raise ValueError("instrumental requests carry no lyrics")
    if request["thinking"] and task != "text2music":
        raise ValueError("thinking is a text2music option")
    if lrc and (task != "text2music" or request["instrumental"] or not request["lyrics"]):
        raise ValueError("lrc needs a text2music request with lyrics")
    _int(request, "seed", 0, 9007199254740991)
    if "bpm" in request:
        _int(request, "bpm", 30, 300)
    if "keyscale" in request and not (isinstance(request["keyscale"], str) and _KEY.fullmatch(request["keyscale"])):
        raise ValueError("keyscale is invalid")
    if "timesignature" in request and request["timesignature"] not in ("2", "3", "4", "6"):
        raise ValueError("timesignature must be 2, 3, 4 or 6")
    steps = _int(request, "inference_steps", 8, 64)
    if cls == "turbo" and steps != 8:
        raise ValueError("turbo renders in exactly 8 steps")
    if cls == "base":
        if not 32 <= steps <= 64:
            raise ValueError("base steps must be 32..64")
        guidance = _int(request, "guidance_milli", 1000, 15000)
    elif "guidance_milli" in request:
        raise ValueError("guidance_milli is a base-class parameter")

    src_s = pad_s = None
    if "src" in request:
        src = _obj(request, "src", {"track_id": 0, "master_sha256": 0, "duration_ds": 0})
        src_s = _int(src, "duration_ds", 1, 6000) / 10
    ref = "ref" in request
    if ref:
        _obj(request, "ref", {"track_id": 0, "clip_sha256": 0, "from_ds": 0, "to_ds": 0})
    start = end = None
    if task == "text2music":
        seconds = _int(request, "duration_ds", 100, 6000) / 10
    elif task == "repaint":
        a, b = _int(request, "repaint_from_ds", 0, 6000), _int(request, "repaint_to_ds", 1, 6000)
        if not a < b <= int(src_s * 10):
            raise ValueError("the repaint range must be inside the source")
        start, end, seconds = a / 10, b / 10, src_s
    elif task == "extend":
        pad_s = _int(request, "extend_ds", 30, 900) / 10
        start, end, seconds = src_s, src_s + pad_s, src_s + pad_s
    else:
        tracks = request["tracks"]
        limit = 1 if task == "extract" else 4
        if (not isinstance(tracks, list) or not 1 <= len(tracks) <= limit or len(set(map(str, tracks))) != len(tracks)
                or any(t not in TRACK_NAMES for t in tracks)):
            raise ValueError(f"tracks must be 1..{limit} distinct names from the ACE-Step track list")
        seconds = src_s
    if seconds > max_seconds:
        raise ValueError(f"the render would be {seconds:g} s, over this worker's {max_seconds} s cap")

    gen, config, resolved = resolve_params(request["engine_params_version"], cls, UPSTREAM_TASK[task],
                                           thinking=request["thinking"])
    gen.update(
        task_type=UPSTREAM_TASK[task], instruction=_instruction(task, request.get("tracks", [])),
        caption=request["caption"], lyrics="[Instrumental]" if request["instrumental"] else request["lyrics"],
        instrumental=request["instrumental"], vocal_language=request["vocal_language"], seed=request["seed"],
        inference_steps=steps, guidance_scale=guidance / 1000 if cls == "base" else 1.0)
    for k, name in (("bpm", "bpm"), ("keyscale", "keyscale"), ("timesignature", "timesignature")):
        if k in request:
            gen[name] = request[k]
    if task == "text2music":
        gen["duration"] = seconds
    elif task == "complete":
        gen["duration"] = src_s  # complete still reads `duration`; the source length is what it renders
    if start is not None:
        gen.update(repainting_start=start, repainting_end=end)
    config.update(use_random_seed=False, seeds=[request["seed"]], audio_format="flac")
    resolved.update(task_type=gen["task_type"], instruction=gen["instruction"], inference_steps=steps,
                    guidance_scale=gen["guidance_scale"], seed=request["seed"])
    return Plan(task, cls, dict(request), gen, config, seconds, src_s, pad_s, ref, lrc, resolved)


def _instruction(task: str, tracks: Sequence[str]) -> str:
    if task == "extract":
        return INSTRUCTIONS["extract"].format(TRACK_NAME=tracks[0].upper())
    if task == "complete":
        return INSTRUCTIONS["complete"].format(TRACK_CLASSES=" | ".join(t.upper() for t in tracks))
    return INSTRUCTIONS["repaint" if task in ("repaint", "extend") else "text2music"]


# ---------------------------------------------------------------------------------------------------------------
# audio

def _sf() -> Any:
    import soundfile  # noqa: PLC0415

    return soundfile


def _np(x: Any) -> Any:
    import numpy as np  # noqa: PLC0415

    if hasattr(x, "detach"):
        x = x.detach().cpu().float().numpy()
    return np.asarray(x, dtype=np.float32)


def _mismatch(msg: str) -> WorkerError:
    return WorkerError("INPUT_MISMATCH", msg)


def check_source(path: Path, expect_s: float, tmp: Path, pad_s: float | None) -> Path:
    """The fetched source as a `.flac` path ACE-Step can load (the fetched file has no extension); for extend the
    source padded with `pad_s` of digital silence (same rate and channels, 24-bit). Its length must match the request."""
    sf = _sf()
    try:
        info = sf.info(str(path))
    except Exception:
        raise _mismatch("src_audio is not readable audio") from None
    seconds = info.frames / info.samplerate if info.samplerate else 0.0
    if abs(seconds - expect_s) > 0.15:
        raise _mismatch(f"src_audio is {seconds:.2f} s, the request says {expect_s:.1f} s")
    out = tmp / ("src_padded.flac" if pad_s else "src.flac")
    if not pad_s:
        return _alias(path, out)
    import numpy as np  # noqa: PLC0415

    data, rate = sf.read(str(path), dtype="float32", always_2d=True)
    silence = np.zeros((int(round(pad_s * rate)), data.shape[1]), dtype=np.float32)
    sf.write(str(out), np.concatenate([data, silence]), rate, format="FLAC", subtype="PCM_24")
    return out


def _alias(path: Path, out: Path) -> Path:
    """`out` is the same bytes as `path` (ACE-Step picks its loader by file extension, the fetched files have none)."""
    out.unlink(missing_ok=True)
    try:
        os.link(path, out)
    except OSError:
        shutil.copyfile(path, out)
    return out


def check_reference(path: Path, tmp: Path) -> Path:
    """The reference clip must be exactly 30.000 s at 48 kHz (anything else is re-sampled with the unseeded global
    RNG upstream) and must not be silent (upstream drops a silent clip without an error)."""
    sf = _sf()
    try:
        info = sf.info(str(path))
    except Exception:
        raise _mismatch("reference_audio is not readable audio") from None
    if info.samplerate != SAMPLE_RATE or info.frames != REF_FRAMES or info.channels not in (1, 2):
        raise _mismatch(f"reference_audio must be exactly 30.000 s at 48 kHz (got {info.frames} frames at "
                        f"{info.samplerate} Hz)")
    data, _ = sf.read(str(path), dtype="float32", always_2d=True)
    if not float(abs(data).max()) >= 1e-6:
        raise _mismatch("reference_audio is silent")
    return _alias(path, tmp / "reference.flac")


def valid_lrc(text: Any) -> str | None:
    """The LRC text normalised (no blank lines, trailing newline) or None when it must be dropped."""
    if not isinstance(text, str):
        return None
    lines = [ln.rstrip() for ln in text.splitlines() if ln.strip()]
    if not lines or not all(_LRC_LINE.match(ln) for ln in lines):
        return None
    out = "\n".join(lines) + "\n"
    return out if len(out.encode()) <= LRC_MAX_BYTES else None


def _jsonable(v: Any) -> Any:
    if isinstance(v, bool) or v is None or isinstance(v, (str, int)):
        return v
    if isinstance(v, float):
        return v if math.isfinite(v) else None
    if isinstance(v, Mapping):
        return {str(k): _jsonable(x) for k, x in v.items()}
    if isinstance(v, (list, tuple)):
        return [_jsonable(x) for x in v]
    return str(v)


class AceResult:
    """Everything render produced; save_artifacts() writes the allowlisted files (audio is encoded here)."""

    truncated = None  # not reported by ACE-Step: the output schema requires null

    def __init__(self, audio: Any, rate: int, plan: Plan, info: dict[str, Any], lrc: str | None,
                 timings: Mapping[str, float] | None = None) -> None:
        self.audio, self.rate, self.plan, self.info, self.lrc = audio, rate, plan, info, lrc
        self.timings: dict[str, float] = dict(timings or {})  # merged into mm-result `timings` by the handler

    def save_artifacts(self, directory: str | Path) -> None:
        d = Path(directory)
        sf = _sf()
        audio_path = d / "audio.flac"
        sf.write(str(audio_path), self.audio, self.rate, format="FLAC", subtype="PCM_24")
        got = sf.info(str(audio_path))
        if got.samplerate != SAMPLE_RATE or got.format != "FLAC" or got.subtype != "PCM_24":
            raise RuntimeError(f"audio.flac is {got.format}/{got.subtype} at {got.samplerate} Hz, "
                               "expected FLAC/PCM_24 at 48000 Hz")
        (d / "request.json").write_bytes(self.plan.canonical)
        result = {**self.info, "audio": {"channels": got.channels, "frames": got.frames, "subtype": got.subtype,
                                         "sample_rate": got.samplerate}}
        (d / "result.json").write_text(json.dumps(_jsonable(result), sort_keys=True, separators=(",", ":")))
        if self.lrc is not None:
            (d / "lyrics.lrc").write_text(self.lrc, encoding="utf-8")


# ---------------------------------------------------------------------------------------------------------------
# GPU memory per component (E2-P0b folded into the first canary)

class Gpu(Protocol):
    def reset_peak(self) -> None: ...

    def peak_bytes(self) -> int: ...

    def allocated_bytes(self) -> int: ...


class _TorchGpu:  # pragma: no cover - needs torch and a GPU
    def __init__(self) -> None:
        import torch  # noqa: PLC0415

        self._t = torch.cuda

    def reset_peak(self) -> None:
        self._t.reset_peak_memory_stats()

    def peak_bytes(self) -> int:
        return int(self._t.max_memory_allocated())

    def allocated_bytes(self) -> int:
        return int(self._t.memory_allocated())


# generate_music reports progress at the start of each phase: the LM below 0.51, input preparation (text encoder,
# source / reference encode) at 0.51, the DiT denoising in 0.52..0.8 (a background thread keeps reporting), the VAE
# decode from 0.8, and 0.99 once the audio is prepared (acestep/core/generation/handler/*, llm_inference.py).
PHASES = ((0.99, None), (0.8, "vae"), (0.52, "dit"), (0.51, "encoder"), (0.0, "lm"))


def phase_of(value: float) -> str | None:
    for floor, name in PHASES:
        if value >= floor:
            return name
    return "lm"


class PhaseMeter:
    """torch.cuda peak allocation per component, switched by the progress callback. The peak counters are reset at
    every phase change; a component is reported only if its phase was entered. Values are absolute (resident
    weights included): `resident_mb` is what was allocated before the render started."""

    def __init__(self, gpu: Gpu) -> None:
        self._gpu = gpu
        self.peaks: dict[str, int] = {}
        self.resident = gpu.allocated_bytes()
        self._phase: str | None = None
        gpu.reset_peak()

    def on_progress(self, value: float) -> None:
        phase = phase_of(value)
        if phase == self._phase:
            return
        if self._phase is not None:
            self._close()
        elif phase != "lm":
            self._gpu.reset_peak()  # nothing ran before the first milestone: do not blame its phase for it
        self._phase = phase

    def _close(self) -> None:
        assert self._phase is not None
        self.peaks[self._phase] = max(self.peaks.get(self._phase, 0), self._gpu.peak_bytes())
        self._gpu.reset_peak()

    def finish(self) -> dict[str, float]:
        if self._phase is not None:
            self._close()
            self._phase = None
        out = {f"peak_vram_mb_{k}": round(v / (1 << 20), 1) for k, v in self.peaks.items()}
        out["vram_resident_mb"] = round(self.resident / (1 << 20), 1)
        return out


_COST_KEYS = {"lm_s": "lm_total_time", "dit_s": "dit_total_time_cost", "decode_s": "dit_vae_decode_time_cost"}


def cost_timings(costs: Mapping[str, Any]) -> dict[str, float]:
    """lm_s / dit_s / decode_s from ACE-Step's unified time_costs (dit_s excludes the VAE decode)."""
    num = {k: float(v) for k, v in costs.items() if isinstance(v, (int, float)) and not isinstance(v, bool)}
    out = {name: round(num[key], 3) for name, key in _COST_KEYS.items() if key in num}
    if "dit_s" in out and "decode_s" in out:
        out["dit_s"] = round(max(0.0, out["dit_s"] - out["decode_s"]), 3)
    return out


# ---------------------------------------------------------------------------------------------------------------
# the adapter

class Adapter:
    engine = ENGINE

    def __init__(self, backend: Backend, *, weights: Mapping[str, str], classes: frozenset[str],
                 max_seconds: int = DEFAULT_MAX_SECONDS, clock: Callable[[], float] = time.monotonic,
                 gpu: Gpu | None = None) -> None:
        self._b = backend
        self._gpu = gpu
        self.weights = dict(weights)
        self.classes = frozenset(classes)
        self.max_seconds = max_seconds
        self._clock = clock

    def validate(self, request: dict[str, Any]) -> Plan:
        return build_plan(request, self.classes, self.max_seconds)

    def render(self, take: TakeInput, inputs: Mapping[str, Path], cancelled: Callable[[], bool], tmp: Path, *,
               params: Plan) -> AceResult:
        plan = params
        gen = dict(plan.gen)
        if plan.src_s is not None:
            gen["src_audio"] = str(check_source(inputs["src_audio"], plan.src_s, tmp, plan.pad_s))
        if plan.ref:
            gen["reference_audio"] = str(check_reference(inputs["reference_audio"], tmp))

        meter = PhaseMeter(self._gpu) if self._gpu is not None else None

        def progress(*a: Any, **_k: Any) -> None:
            # ACE-Step runs the DiT progress reporter on a thread that swallows exceptions, so a cancellation
            # raised here stops the LM phase and the phase boundaries; cancelled() is checked again after the render.
            if meter is not None and a and isinstance(a[0], (int, float)):
                meter.on_progress(float(a[0]))
            if cancelled():
                raise Cancelled()

        b = self._b
        t0 = self._clock()
        try:
            res = b.generate(b.dits[plan.cls], b.llm, b.make_params(**gen), b.make_config(**plan.config), progress)
        finally:
            vram = meter.finish() if meter is not None else {}
        gen_s = self._clock() - t0
        if cancelled():  # generate_music swallows exceptions raised by the callback into success=False
            raise Cancelled()
        if not getattr(res, "success", False) or not getattr(res, "audios", None):
            raise RuntimeError("ACE-Step failed: " + redact.message(getattr(res, "error", None) or "no audio"))
        audio = res.audios[0]
        rate = int(audio.get("sample_rate") or 0)
        if rate != SAMPLE_RATE:
            raise RuntimeError(f"ACE-Step returned {rate} Hz audio, expected {SAMPLE_RATE}")
        pcm = _np(audio["tensor"])
        if pcm.ndim != 2 or pcm.shape[0] not in (1, 2) or pcm.shape[1] < 1:
            raise RuntimeError("ACE-Step returned audio that is not [channels, samples]")
        pcm = pcm.T.copy()  # soundfile wants [frames, channels]
        extra = getattr(res, "extra_outputs", None) or {}
        lrc, lrc_s = None, None
        if plan.lrc:  # must run right after the render, on the same handler: it needs extra_outputs
            if cancelled():
                raise Cancelled()
            t1 = self._clock()
            lrc = self._lrc(b.dits[plan.cls], plan, extra, pcm.shape[0] / rate)
            lrc_s = self._clock() - t1
        meta = extra.get("lm_metadata") or {}
        info = {
            "success": True, "error": None, "status_message": redact.message(getattr(res, "status_message", "")),
            "task": plan.task, "seed": plan.request["seed"], "sample_rate": rate,
            "time_costs": {**{str(k): v for k, v in (extra.get("time_costs") or {}).items()
                              if isinstance(v, (int, float))}, "generate_s": gen_s, **({"lrc_s": lrc_s} if lrc_s is not None else {})},
            "lm_metadata": {k: meta.get(k) for k in ("bpm", "keyscale", "duration", "vocal_language")},
            "models": {"dit": f"{REPO_BASE if plan.cls == 'base' else REPO_MAIN}:{layout()['dits'][plan.cls]['dir']}",
                       "lm": f"{REPO_MAIN}:{params_version(plan.request['engine_params_version'])['lm']['model']}"},
            "params": plan.resolved, "lrc": {"requested": plan.lrc, "produced": lrc is not None},
        }
        timings: dict[str, float] = {**cost_timings(extra.get("time_costs") or {}), **vram}
        if lrc_s is not None:
            timings["lrc_s"] = round(lrc_s, 3)
        util = getattr(b, "lm_gpu_memory_utilization_milli", None)
        if isinstance(util, int) and not isinstance(util, bool):
            timings["lm_gpu_memory_utilization_milli"] = util
        return AceResult(pcm, rate, plan, info, lrc, timings)

    def _lrc(self, dit: Any, plan: Plan, extra: Mapping[str, Any], seconds: float) -> str | None:
        """The alignment call (never fabricated: any failure or malformed text drops the lyrics.lrc file)."""
        keys = ("pred_latents", "encoder_hidden_states", "encoder_attention_mask", "context_latents",
                "lyric_token_idss")
        if any(extra.get(k) is None for k in keys):
            return None
        lang = plan.request["vocal_language"]
        try:
            out = dit.get_lyric_timestamp(
                pred_latent=extra["pred_latents"][0:1], encoder_hidden_states=extra["encoder_hidden_states"][0:1],
                encoder_attention_mask=extra["encoder_attention_mask"][0:1],
                context_latents=extra["context_latents"][0:1], lyric_token_ids=extra["lyric_token_idss"][0:1],
                total_duration_seconds=float(seconds), vocal_language="en" if lang == "unknown" else lang,
                inference_steps=plan.request["inference_steps"], seed=LRC_SEED)
        except Exception as e:
            _log(f"lrc alignment failed: {type(e).__name__}")
            return None
        return valid_lrc(out.get("lrc_text")) if isinstance(out, dict) and out.get("success") else None

    def env_fingerprint(self, environ: Mapping[str, str]) -> dict[str, object]:
        fp = envmod.fingerprint(self.weights, environ)
        try:
            pkg = metadata.version("ace-step")
        except metadata.PackageNotFoundError:
            pkg = "unknown"
        fp.update(engine=ENGINE, pkg_version=pkg, code_commit=environ.get("MM_ACESTEP_COMMIT", "unknown"),
                  decoder=DECODER)
        return fp


# ---------------------------------------------------------------------------------------------------------------
# test / selftest double

class FakeAceStep:
    """A Backend double (tests and MODE=selftest): records every GenerationParams / GenerationConfig kwargs set,
    returns 48 kHz stereo float audio of the requested length (capped by `max_audio_s`) with the extra_outputs the
    LRC pass needs. No torch, no GPU."""

    def __init__(self, classes: Sequence[str] = ("turbo", "base"), *, max_audio_s: float | None = None,
                 success: bool = True, error: str | None = None, rate: int = SAMPLE_RATE,
                 lrc_text: str | None = "[00:00.00]la la\n[00:04.00]la\n", lrc_error: bool = False,
                 on_generate: Callable[[Callable[..., Any]], None] | None = None, tensor_kind: str = "numpy",
                 progress_script: Sequence[float] | None = None, lm_util_milli: int | None = None) -> None:
        self.dits = {c: FakeDit() for c in classes}
        for d in self.dits.values():
            d.lrc_text, d.lrc_error = lrc_text, lrc_error
        self.llm = object()
        self.max_audio_s, self.success, self.error, self.rate = max_audio_s, success, error, rate
        self.on_generate, self.tensor_kind = on_generate, tensor_kind
        self.progress_script = progress_script  # progress values reported during generate (the upstream milestones)
        self.lm_gpu_memory_utilization_milli = lm_util_milli
        self.params: list[dict[str, Any]] = []
        self.configs: list[dict[str, Any]] = []
        self.calls: list[Any] = []

    def make_params(self, **kw: Any) -> dict[str, Any]:
        self.params.append(kw)
        return kw

    def make_config(self, **kw: Any) -> dict[str, Any]:
        self.configs.append(kw)
        return kw

    def generate(self, dit: Any, llm: Any, params: dict[str, Any], config: dict[str, Any],
                 progress: Callable[..., Any]) -> Any:
        import numpy as np  # noqa: PLC0415

        self.calls.append((dit, params, config))
        for v in self.progress_script or ():
            progress(v, desc="step")
        if self.on_generate is not None:
            self.on_generate(progress)
        if not self.success:
            return _FakeResult([], self.error or "boom", {}, False)
        seconds = params.get("duration") or 0
        if params.get("src_audio"):
            sf = _sf()
            info = sf.info(params["src_audio"])
            seconds = info.frames / info.samplerate
        seconds = float(seconds if seconds and seconds > 0 else 1.0)
        if self.max_audio_s is not None:
            seconds = min(seconds, self.max_audio_s)
        n = max(1, int(seconds * self.rate))
        t = np.arange(n, dtype=np.float32) / self.rate
        wave = (0.1 * np.sin(2 * np.pi * 220.0 * t)).astype(np.float32)
        tensor = np.stack([wave, wave])
        extra: dict[str, Any] = {
            "time_costs": {"lm_total_time": 0.1, "dit_total_time_cost": 0.2, "pipeline_total_time": 0.3},
            "lm_metadata": {"bpm": params.get("bpm") or 120, "keyscale": params.get("keyscale") or "C Major",
                            "duration": seconds, "vocal_language": params.get("vocal_language", "en")},
            "pred_latents": [[0.0]], "encoder_hidden_states": [[0.0]], "encoder_attention_mask": [[1]],
            "context_latents": [[0.0]], "lyric_token_idss": [[1]],
        }
        return _FakeResult([{"path": "", "tensor": tensor, "sample_rate": self.rate}], "", extra, True)


class FakeDit:
    lrc_text: str | None = None
    lrc_error = False

    def __init__(self) -> None:
        self.lrc_calls: list[dict[str, Any]] = []

    def get_lyric_timestamp(self, **kw: Any) -> dict[str, Any]:
        self.lrc_calls.append(kw)
        if self.lrc_error:
            raise RuntimeError("alignment failed")
        return {"lrc_text": self.lrc_text, "success": self.lrc_text is not None, "error": None}


@dataclass
class _FakeResult:
    audios: list[dict[str, Any]]
    error: str
    extra_outputs: dict[str, Any]
    success: bool
    status_message: str = "ok"
