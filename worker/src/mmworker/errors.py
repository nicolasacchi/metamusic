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
    # E2 (C153.1): fetched inputs and task availability.
    "INPUT_FAILED",
    "INPUT_MISMATCH",
    "TASK_UNAVAILABLE",
)

# Why a worker could not initialise (C154.5). Only ever seen in the one stderr line and the reconciler event,
# never as a job result: a worker that cannot load never accepts a job.
INIT_CODES = (
    "MODELS_MISSING",
    "NO_GPU",
    "NO_MEMORY",
    "LOAD_DEADLINE",
    "LOAD_ERROR",
    "ENV_INVALID",
)


class InitError(Exception):
    """A load-time failure with one of the INIT_CODES; serverless.main turns it into exit 1."""

    def __init__(self, code: str, message: str = "") -> None:
        assert code in INIT_CODES, code
        super().__init__(f"{code}: {message}")
        self.code = code
        self.message = message


class WorkerError(Exception):
    """A failure with one of the C73.5 error codes."""

    def __init__(self, code: str, message: str = "") -> None:
        assert code in CODES, code
        super().__init__(f"{code}: {message}")
        self.code = code
        self.message = message
