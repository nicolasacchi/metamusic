"""Image smoke (worker-image.yml): an MP3 and a FLAC fixture decode with mmears.audio (C333.4). Run inside the image:

    docker run --rm --entrypoint python -e PYTHONPATH=/opt/ears-site -v worker/tools/smoke_ears_decode.py:/tmp/smoke.py:ro IMAGE /tmp/smoke.py

The fixtures are one second of a synthetic stereo sine, generated here; no audio file is ever committed.
"""

from __future__ import annotations

import sys
import tempfile
from pathlib import Path

import numpy as np
import soundfile as sf
from mmears import audio


def main() -> int:
    t = np.linspace(0, 1, 44100, endpoint=False, dtype="float32")
    wave = np.stack([0.3 * np.sin(2 * np.pi * 220 * t)] * 2, axis=1)
    tmp = Path(tempfile.mkdtemp())
    for ext, fmt in (("flac", "FLAC"), ("mp3", "MP3")):
        p = tmp / f"fixture.{ext}"
        sf.write(str(p), wave, 44100, format=fmt)
        d = audio.decode(str(p))
        if d.codec != ext or d.channels != 2 or abs(d.duration_s - 1.0) > 0.1:
            print(f"{ext}: decoded as {d.codec}, {d.channels} channels, {d.duration_s:.2f} s", file=sys.stderr)
            return 1
    print("flac and mp3 decode")
    return 0


if __name__ == "__main__":
    sys.exit(main())
