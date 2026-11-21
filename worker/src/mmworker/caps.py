"""Worker capabilities (C322.1): the frozen vocabulary and what this image advertises.

`v2` is always present. A stage capability (`ears`, `analyze`, `plan`) exists only while its stage is registered
in `mmworker.stages`, so a cap is advertised only when its code and models are in the image (C322.2). `attach` stays
reserved (no consumer in this program's first release), `agent` and `fallback` are transport capabilities with no
stage: the transport that implements them passes them in explicitly.
"""

from __future__ import annotations

from collections.abc import Iterable

from . import stages

V2 = "v2"
STAGE_CAPS = ("ears", "analyze", "plan")
TRANSPORT_CAPS = ("fallback", "agent")
VOCABULARY = frozenset({V2, "attach", *STAGE_CAPS, *TRANSPORT_CAPS})


def compose(stage_caps: Iterable[str], transport: Iterable[str] = ()) -> list[str]:
    """The sorted, de-duplicated capability list. Unknown names are a programming error, never silently dropped."""
    caps = {V2, *stage_caps, *transport}
    unknown = caps - VOCABULARY
    if unknown:
        raise ValueError(f"unknown capability {sorted(unknown)[0]!r}")
    stage_unknown = set(stage_caps) - set(STAGE_CAPS)
    if stage_unknown:
        raise ValueError(f"{sorted(stage_unknown)[0]!r} is not a stage capability")
    bad_transport = set(transport) - set(TRANSPORT_CAPS)
    if bad_transport:
        raise ValueError(f"{sorted(bad_transport)[0]!r} is not a transport capability")
    return sorted(caps)


def image_caps(transport: Iterable[str] = ()) -> list[str]:
    """What this image advertises right now: v2, one cap per registered stage, plus the given transport caps."""
    return compose(stages.caps(), transport)


def label(caps: Iterable[str]) -> str:
    """The text of `LABEL io.metamusic.caps` and of the selftest caps line."""
    return " ".join(caps)
