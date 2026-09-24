#!/usr/bin/env python3
"""Exercise lazypkg's real terminal UI against fake providers only.

Run from any directory: python3 scripts/pty_smoke.py
Requires Go and a POSIX PTY (macOS/Linux). The test binary, operation receipt,
and terminal capture exist only in a temporary directory that is cleaned up.
No real mpm, package manager, setup installer, or package mutation is invoked.
"""

from __future__ import annotations

import json
import os
from pathlib import Path
import select
import signal
import struct
import subprocess
import sys
import tempfile
import time


def main() -> None:
    if os.name != "posix":
        raise SystemExit("This smoke test requires a POSIX PTY (macOS or Linux).")

    import fcntl
    import pty
    import termios

    root = Path(__file__).resolve().parents[1]
    with tempfile.TemporaryDirectory(prefix="lazypkg-pty-") as directory:
        scratch = Path(directory)
        binary = scratch / "tui.test"
        receipt = scratch / "receipt.json"
        subprocess.run(
            ["go", "test", "-c", "-o", str(binary), "./internal/tui"],
            cwd=root,
            check=True,
        )
        master, slave = pty.openpty()
        original = termios.tcgetattr(slave)
        fcntl.ioctl(slave, termios.TIOCSWINSZ, struct.pack("HHHH", 24, 80, 0, 0))
        environment = os.environ.copy()
        environment.update(
            LAZYPKG_TUI_TEST_HELPER="1",
            LAZYPKG_TUI_TEST_RECEIPT=str(receipt),
            TERM="xterm-256color",
            NO_COLOR="1",
        )
        child = subprocess.Popen(
            [str(binary), "-test.run", "^TestPTYHelper$", "-test.v"],
            cwd=root,
            stdin=slave,
            stdout=slave,
            stderr=slave,
            env=environment,
        )
        capture = bytearray()

        def read_for(seconds: float) -> None:
            deadline = time.monotonic() + seconds
            while time.monotonic() < deadline:
                timeout = min(0.05, max(0, deadline - time.monotonic()))
                if not select.select([master], [], [], timeout)[0]:
                    continue
                try:
                    chunk = os.read(master, 65536)
                except OSError:
                    break
                if not chunk:
                    break
                capture.extend(chunk)
                # Satisfy the terminal's cursor-position probe when requested.
                if b"\x1b[6n" in chunk:
                    os.write(master, b"\x1b[1;1R")

        def send(data: bytes, pause: float = 0.2) -> None:
            os.write(master, data)
            read_for(pause)

        def require(text: str) -> None:
            if text.encode() not in capture:
                raise AssertionError(f"Terminal output did not contain {text!r}")

        def alive() -> None:
            if child.poll() is not None:
                raise AssertionError(f"Dashboard exited unexpectedly: {child.returncode}")

        try:
            read_for(1.3)
            require("alpha")
            send(b"/jkhqliuxa?")
            alive()  # Printable shortcuts remain text.
            send(b"\x1b")
            send(b"?")
            require("Keyboard")
            send(b"\x1b")
            send(b"\t")
            send(b"j")
            send(b"\r")  # Apply the manager filter.
            send(b"2")
            send(b"/alpha\r", 0.7)  # The fake query deliberately takes 300 ms.
            send(b"i", 0.3)
            require("Review changes")
            send(b"\r")
            if receipt.exists() or b"Native prompt:" in capture:
                raise AssertionError("Enter approved a plan without explicit confirmation")
            send(b"\x1b")
            if receipt.exists():
                raise AssertionError("Cancelling a plan caused a mutation")
            send(b"i")
            send(b"y", 0.5)
            require("Native prompt: type continue:")
            send(b"continue\r", 0.4)
            require("Fixture operation completed")
            require("Press Enter to return")
            receipt_data = json.loads(receipt.read_text())
            if receipt_data["calls"] != 1 or receipt_data["request"] != {
                "operation": "install",
                "manager": "brew",
                "package": "alpha",
            }:
                raise AssertionError(f"Unexpected fake operation receipt: {receipt_data}")
            acknowledgement_start = len(capture)
            read_for(0.2)
            if b"\x1b[?1049h" in capture[acknowledgement_start:]:
                raise AssertionError("UI reacquired the terminal before acknowledgement")
            send(b"\r", 0.7)
            send(b"v")
            require("Last operation")
            send(b"\x1b")
            fcntl.ioctl(slave, termios.TIOCSWINSZ, struct.pack("HHHH", 16, 48, 0, 0))
            child.send_signal(signal.SIGWINCH)
            read_for(0.3)
            send(b"/\x1b[200~jkhq\ny\x1b[201~")
            alive()  # Bracketed paste cannot approve or quit.
            send(b"\x1b")
            send(b"q", 0.5)
            child.wait(timeout=3)
            read_for(0.1)
            require("TUI_EXIT_OK")
            if child.returncode != 0:
                raise AssertionError(f"Test process exited {child.returncode}")
            restored = termios.tcgetattr(slave)
            for flag in (termios.ECHO, termios.ICANON):
                if bool(original[3] & flag) != bool(restored[3] & flag):
                    raise AssertionError("Terminal ECHO/ICANON was not restored")
            if b"\x1b[?25h" not in capture or b"\x1b[?1049l" not in capture:
                raise AssertionError("Missing cursor/alternate-screen restoration")
            if json.loads(receipt.read_text())["calls"] != 1:
                raise AssertionError("Unexpected extra fake mutation")
            print(
                "PTY PASS: delayed reads, text/paste isolation, manager filter, search, "
                "review/cancel, explicit approval, native prompt, result acknowledgement, "
                "dashboard return, 48x16 resize, clean exit."
            )
            print("Terminal ECHO/ICANON, cursor and alternate screen restored; fake mutations: 1.")
        except BaseException:
            # repr keeps untrusted control bytes from reaching the invoking terminal.
            print(f"Terminal capture tail: {bytes(capture[-5000:])!r}", file=sys.stderr)
            raise
        finally:
            if child.poll() is None:
                child.kill()
                child.wait()
            os.close(master)
            os.close(slave)


if __name__ == "__main__":
    main()
