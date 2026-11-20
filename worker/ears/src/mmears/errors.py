"""Stage failures, carrying a contract error code (C332.2)."""

from __future__ import annotations

CODES = frozenset(
    {
        "MODELS_MISSING",
        "INPUT_DECODE",
        "SEPARATION_FAILED",
        "ASR_FAILED",
        "DEADLINE",
        "OOM",
        "SUBPROCESS_FAILED",
    }
)


class EarsError(Exception):
    """A failure of one stage; `code` is one of CODES."""

    def __init__(self, code: str, message: str) -> None:
        super().__init__(message)
        assert code in CODES, code
        self.code = code
        self.message = message


def is_oom(exc: BaseException) -> bool:
    """True for a CUDA/host out-of-memory error, without importing torch."""
    if isinstance(exc, MemoryError):
        return True
    name = type(exc).__name__
    text = str(exc).lower()
    return name == "OutOfMemoryError" or "out of memory" in text


def classify(exc: BaseException, default: str) -> EarsError:
    """Turn any exception into an EarsError; an OOM wins over the stage's default code."""
    if isinstance(exc, EarsError):
        return exc
    code = "OOM" if is_oom(exc) else default
    return EarsError(code, f"{type(exc).__name__}: {exc}")
