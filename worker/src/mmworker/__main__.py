"""python -m mmworker: MODE=serverless | selftest | agent."""

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
        from . import agent  # noqa: PLC0415

        return agent.main()
    print(f"mmworker: unknown MODE {mode!r} (serverless | selftest | agent)", file=sys.stderr)
    return 2


if __name__ == "__main__":
    sys.exit(run(os.environ.get("MODE", "")))
