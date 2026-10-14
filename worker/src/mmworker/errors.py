"""Error type shared by the handler, uploader and start client."""

from __future__ import annotations

CODES = (
    "BAD_INPUT",
    "CONTRACT_VERSION",
    "WEIGHTS_MISMATCH",
    "PIPELINE_ERROR",
    "TRUNCATED_NOT_DICT",
    "ARTIFACT_MISSING",
    "ARTIFACT_TOO_LARGE",
    "UPLOAD_FAILED",
    "UPLOAD_CONFLICT",
    "UPLOAD_CLOSED",
    "ATTEMPT_CLOSED",
    "START_FAILED",
    "DEADLINE",
)


class WorkerError(Exception):
    """A failure with one of the C73.5 error codes."""

    def __init__(self, code: str, message: str = "") -> None:
        assert code in CODES, code
        super().__init__(f"{code}: {message}")
        self.code = code
        self.message = message
