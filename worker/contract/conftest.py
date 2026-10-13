"""Worker-contract job fixtures (C83.4). Runs against the pinned yue2 with CPU torch; no weights, no GPU."""

from __future__ import annotations

import socket

import pytest


@pytest.fixture
def socket_guard(monkeypatch: pytest.MonkeyPatch) -> list[tuple]:
    """Any attempt to open a connection fails the test and is recorded."""
    attempts: list[tuple] = []

    def refuse(*args, **kwargs):  # noqa: ANN002, ANN003
        attempts.append(args)
        raise AssertionError("network access attempted: " + repr(args[:1]))

    monkeypatch.setattr(socket.socket, "connect", refuse)
    monkeypatch.setattr(socket.socket, "connect_ex", refuse)
    monkeypatch.setattr(socket, "create_connection", refuse)
    return attempts
