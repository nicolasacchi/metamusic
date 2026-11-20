# mmears

Tier B "ears" stage (M5b, contract `mm.ears-b/1`): Demucs vocal separation, faster-whisper word timings
and torchcrepe pitch contour, emitted as raw outputs only (`analysis.json`). Everything else is computed
in Go on the server.

```
python -m mmears --audio FILE --lang it|auto --stages separate,asr[,transcribe] --models DIR --out FILE
python -m mmears --self-check --models DIR     # verify models.lock.json against DIR
```

Exit 0 whenever a document was written (also `status: error`). The heavy imports are lazy; the tests use
fakes only and need no GPU and no model download:

```
cd worker/ears && uv sync --frozen --group dev && uv run pytest
```

The model lock format is `mm.ears-models/1` (see `models.py`); the pins are filled in by the image lane.
Only MIT, Apache-2.0 and BSD-3-Clause models are accepted.
