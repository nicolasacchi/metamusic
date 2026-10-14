"""python -m mmworker: MODE=serverless | selftest; agent arrives in M6."""

from __future__ import annotations

import os
import sys


def run(mode: str) -> int:
    if mode == "serverless":
        from . import serverless  # noqa: PLC0415

        return serverless.main()
    if mode == "selftest":
        from . import selftest  # noqa: PLC0415

        return selftest.main()
    if mode == "agent":
        print("mmworker: MODE=agent arrives in M6", file=sys.stderr)
        return 2
    print(f"mmworker: unknown MODE {mode!r} (serverless | selftest)", file=sys.stderr)
    return 2


if __name__ == "__main__":
    sys.exit(run(os.environ.get("MODE", "")))
