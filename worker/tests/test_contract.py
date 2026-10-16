from __future__ import annotations

import copy
import json
from importlib import resources
from pathlib import Path
from typing import Any

import jsonschema
import pytest

from mmworker import contract
from mmworker.errors import WorkerError

from .conftest import make_raw

BASE = "https://mm.example.com"


def schema(name: str) -> dict[str, Any]:
    return json.loads(resources.files("mmworker").joinpath(f"schema/{name}").read_text())


def good() -> dict[str, Any]:
    return make_raw(BASE)


def test_parse_input_ok() -> None:
    t = contract.parse_input(good())
    assert t.take_id == "take-1" and t.timeout_s == 186
    assert t.uploads["audio.flac"].max_bytes == 167772160
    assert "sig" not in repr(t) and "sig" not in repr(t.uploads["audio.flac"])


def test_parse_input_contract_version() -> None:
    raw = good()
    raw["contract"] = "mm.worker/2"
    with pytest.raises(WorkerError) as e:
        contract.parse_input(raw)
    assert e.value.code == "CONTRACT_VERSION"


def test_parse_input_unknown_artifact() -> None:
    raw = good()
    raw["uploads"]["latent.npy"] = {"url": f"{BASE}/x", "max_bytes": 5}
    with pytest.raises(WorkerError) as e:
        contract.parse_input(raw)
    assert e.value.code == "BAD_INPUT"


def test_parse_input_missing_mm_result() -> None:
    raw = good()
    del raw["uploads"]["mm-result.json"]
    with pytest.raises(WorkerError) as e:
        contract.parse_input(raw)
    assert e.value.code == "BAD_INPUT"


def test_parse_input_missing_start() -> None:
    raw = good()
    del raw["start"]
    with pytest.raises(WorkerError):
        contract.parse_input(raw)


def test_parse_input_not_object() -> None:
    with pytest.raises(WorkerError):
        contract.parse_input([1])


def _mut(path: list[str | int], value: Any) -> dict[str, Any]:
    raw = copy.deepcopy(good())
    cur: Any = raw
    for p in path[:-1]:
        cur = cur[p]
    if value is KeyError:
        del cur[path[-1]]
    else:
        cur[path[-1]] = value
    return raw


CASES: list[tuple[str, dict[str, Any]]] = [
    ("good", good()),
    ("extra top", {**good(), "x": 1}),
    ("take_id bad chars", _mut(["take_id"], "a b")),
    ("take_id long", _mut(["take_id"], "a" * 65)),
    ("take_id 64", _mut(["take_id"], "a" * 64)),
    ("take_id int", _mut(["take_id"], 5)),
    ("attempt empty", _mut(["attempt_id"], "")),
    ("engine upper", _mut(["engine"], "YUE2")),
    ("engine long", _mut(["engine"], "a" * 33)),
    ("request empty", _mut(["request"], {})),
    ("request list", _mut(["request"], [1])),
    ("timeout 0", _mut(["timeout_s"], 0)),
    ("timeout 2700", _mut(["timeout_s"], 2700)),
    ("timeout 2701", _mut(["timeout_s"], 2701)),
    ("timeout bool", _mut(["timeout_s"], True)),
    ("timeout float", _mut(["timeout_s"], 5.5)),
    ("budget 0", _mut(["upload_budget_s"], 0)),
    ("deadline 0", _mut(["deadline_unix"], 0)),
    ("deadline str", _mut(["deadline_unix"], "5")),
    ("start extra", _mut(["start", "x"], 1)),
    ("start url ftp", _mut(["start", "url"], "ftp://x/y")),
    ("start url space", _mut(["start", "url"], "https://x/y z")),
    ("start url long", _mut(["start", "url"], "https://x/" + "a" * 2048)),
    ("start missing url", _mut(["start", "url"], KeyError)),
    ("upload missing audio", _mut(["uploads", "audio.flac"], KeyError)),
    ("upload extra key", _mut(["uploads", "audio.flac", "z"], 1)),
    ("upload max too big", _mut(["uploads", "audio.flac", "max_bytes"], 167772161)),
    ("upload max exact", _mut(["uploads", "audio.flac", "max_bytes"], 167772160)),
    ("upload max zero", _mut(["uploads", "request.json", "max_bytes"], 0)),
    ("upload result 64k+1", _mut(["uploads", "mm-result.json", "max_bytes"], 65537)),
    ("upload url bad", _mut(["uploads", "config.json", "url"], "nope")),
    ("upload not obj", _mut(["uploads", "config.json"], 3)),
    ("upload optional ok", _mut(["uploads", "score.abc"], {"url": f"{BASE}/s", "max_bytes": 10})),
    ("uploads list", _mut(["uploads"], [])),
    ("expect no weights", _mut(["expect"], {})),
    ("expect empty weights", _mut(["expect", "weights"], {})),
    ("expect short rev", _mut(["expect", "weights", "m-a-p/YuE2-3B"], "abc")),
    ("expect upper rev", _mut(["expect", "weights", "m-a-p/YuE2-3B"], "A" * 40)),
    ("expect extra", _mut(["expect", "y"], 1)),
    ("contract missing", _mut(["contract"], KeyError)),
    ("contract wrong", _mut(["contract"], "nope")),
    ("min_audio_s 200", _mut(["min_audio_s"], 200)),
    ("min_audio_s 0", _mut(["min_audio_s"], 0)),
    ("min_audio_s 360", _mut(["min_audio_s"], 360)),
    ("min_audio_s 361", _mut(["min_audio_s"], 361)),
    ("min_audio_s -1", _mut(["min_audio_s"], -1)),
    ("min_audio_s str", _mut(["min_audio_s"], "200")),
    ("min_audio_s float", _mut(["min_audio_s"], 200.5)),
    ("min_audio_s bool", _mut(["min_audio_s"], True)),
    ("min_audio_s null", _mut(["min_audio_s"], None)),
]


def test_parse_input_min_audio_s() -> None:
    assert contract.parse_input(good()).min_audio_s == 0
    assert contract.parse_input({**good(), "min_audio_s": 252}).min_audio_s == 252


@pytest.mark.parametrize(("name", "raw"), CASES, ids=[c[0] for c in CASES])
def test_parse_input_matches_schema(name: str, raw: dict[str, Any]) -> None:
    validator = jsonschema.Draft202012Validator(schema("mm-worker-1.input.schema.json"))
    schema_ok = not list(validator.iter_errors(raw))
    try:
        contract.parse_input(raw)
        parsed_ok = True
    except WorkerError:
        parsed_ok = False
    assert parsed_ok == schema_ok, name


def test_artifacts_match_go() -> None:
    s = schema("mm-worker-1.input.schema.json")
    props = s["properties"]["uploads"]["properties"]
    assert set(props) == set(contract.ARTIFACTS)
    for name, p in props.items():
        assert p["properties"]["max_bytes"]["maximum"] == contract.MAX_BYTES[name]
    out = schema("mm-worker-1.output.schema.json")
    out_names = set(out["properties"]["artifacts"]["properties"])
    assert out_names == set(contract.ARTIFACTS) - {"mm-result.json"}
    assert "latent.npy" not in contract.ARTIFACTS
    assert contract.ARTIFACTS[-1] == "mm-result.json"


def test_required() -> None:
    for cot in ("full", "melody", "off"):
        assert contract.REQUIRED(cot) == {"request.json", "config.json", "result.json", "audio.flac"}


def test_uv_lock_has_no_torch() -> None:
    lock = (Path(__file__).parents[1] / "uv.lock").read_text().lower()
    for pkg in ("torch", "yue2"):
        assert f'name = "{pkg}' not in lock
    assert "yue2-infer" not in lock
