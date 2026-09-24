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

        def click(x: int, y: int, pause: float = 0.2) -> None:
            # SGR coordinates are one-based; our shared UI layout is zero-based.
            send(f"\x1b[<0;{x + 1};{y + 1}M\x1b[<0;{x + 1};{y + 1}m".encode(), pause)

        def require(text: str) -> None:
            if text.encode() not in capture:
                raise AssertionError(f"Terminal output did not contain {text!r}")

        def wait_for(text: str, timeout: float = 6.0) -> None:
            deadline = time.monotonic() + timeout
            while text.encode() not in capture and time.monotonic() < deadline:
                read_for(0.05)
            require(text)

        def alive() -> None:
            if child.poll() is not None:
                raise AssertionError(f"Dashboard exited unexpectedly: {child.returncode}")

        try:
            wait_for("alpha")
            require("ownership pending")
            if b"\x1b[?1002h" not in capture:
                raise AssertionError("Mouse cell-motion reporting was not enabled")
            click(26, 7)  # Select the second item using actual SGR mouse input.
            click(26, 6)
            click(24, 6)  # A separate checkbox marks a package without opening details.
            click(24, 6)  # Toggle it back off.
            send(b"\x04\x15\x06\x02")  # Vim half/full-page aliases remain navigation.
            alive()
            click(40, 23)  # Open ordered provider picker through its footer button.
            require("Ordered provider")
            click(8, 6)  # Load the 'all' group; it changes only the draft.
            if receipt.exists():
                raise AssertionError("Changing a provider draft performed a mutation")
            click(3, 23, 0.6)  # Apply the ordered scope once.
            send(b"s")
            require("Manager setup")
            click(6, 5)  # Setup checkbox on/off; never submit setup.
            click(6, 5)
            send(b"\x1b")
            send(b"/Mjkhqliuxa?")
            alive()  # Printable shortcuts remain text.
            send(b"\x1b")
            send(b"?")
            require("Keyboard")
            send(b"\x1b")
            send(b"\t")
            send(b"j")
            send(b"\r")  # Apply the manager filter.
            click(18, 1)  # Discover tab.
            send(b"/alpha\r", 0.7)  # The fake query deliberately takes 300 ms.
            click(44, 22, 0.3)  # Install button uses the same plan path as i.
            require("Review changes")
            send(b"\r")
            if receipt.exists() or b"Native prompt:" in capture:
                raise AssertionError("Enter approved a plan without explicit confirmation")
            click(3, 23)  # Cancel the review.
            if receipt.exists():
                raise AssertionError("Cancelling a plan caused a mutation")
            send(b"i")
            send(b"\x1b[<0;14;24M\x1b[<0;61;24m")  # Release off the Execute button.
            if receipt.exists() or b"Native prompt:" in capture:
                raise AssertionError("Dragging off Execute still approved the plan")
            click(13, 23, 0.5)  # Explicit mouse approval, after the review is visible.
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
            send(b"4", 0.4)
            send(b"R", 0.3)
            require("Resolve alpha")
            send(b"K")  # Retained instance is a separate explicit choice.
            click(6, 6)  # Select just the second installation.
            send(b"\r")
            require("Remove one fixture installation")
            require("KEEP: brew")
            send(b"y", 0.4)
            send(b"continue\r", 0.4)
            if json.loads(receipt.read_text())["calls"] != 2:
                raise AssertionError("Resolution did not execute exactly one reviewed target")
            send(b"\r", 0.5)
            require("reassessing")
            send(b"\x1b")
            send(b"5", 0.3)
            send(b"U", 0.3)
            require("Manager maintenance")
            send(b"\r")
            require("Update fixture manager")
            send(b"s")  # Skip the reviewed update and inspect guidance.
            require("skipped")
            send(b"\r")
            if json.loads(receipt.read_text())["calls"] != 2:
                raise AssertionError("Guidance/skip executed a maintenance job")
            send(b"g\r")  # Explicitly return to review this one fixture job.
            send(b"y", 0.4)
            send(b"continue\r", 0.4)
            if json.loads(receipt.read_text())["calls"] != 3:
                raise AssertionError("Maintenance did not execute exactly one reviewed job")
            send(b"\r", 0.5)
            require("Rechecking")
            send(b"p", 0.3)
            require("Prompt preview")
            require("Fixture prompt")
            send(b"e")
            export = scratch / "handoff.md"
            send(b"\x01\x0b" + str(export).encode() + b"\r", 0.4)
            if export.read_text() != "# Fixture prompt\n\nCurrent context only.\n":
                raise AssertionError("Export differs from the previewed prompt")
            send(b"\x1b")  # Return from preview to queue.
            send(b"\x1b")  # Stop the queue; never execute remaining jobs.
            send(b"1", 0.6)
            send(b"g")
            send(b"g")
            send(b" j ")  # Select alpha and bravo, then hide bravo with a filter.
            send(b"/alpha\r")
            send(b"u", 0.3)
            require("Review package upgrades")
            require("1 selected targets are hidden")
            send(b"\r")
            if json.loads(receipt.read_text())["calls"] != 3:
                raise AssertionError("Enter approved a package batch")
            send(b"y", 0.3)  # One explicit confirmation starts both reviewed jobs.
            send(b"continue\r", 0.3)
            if json.loads(receipt.read_text())["calls"] != 4:
                raise AssertionError("First batch item did not execute alone")
            send(b"continue\r", 0.3)
            require("Fixture batch paused")
            send(b"\r", 0.4)
            require("Package upgrade results")
            if json.loads(receipt.read_text())["calls"] != 5:
                raise AssertionError("Batch continued after its fake failure")
            send(b"r", 0.3)  # Recheck remaining target; always another overview.
            send(b"\r")
            if json.loads(receipt.read_text())["calls"] != 5:
                raise AssertionError("Recheck implicitly approved the remaining target")
            send(b"y", 0.3)
            send(b"continue\r", 0.3)
            require("Fixture batch completed")
            send(b"\r", 0.4)
            send(b"\x1b")
            send(b"U", 0.3)  # Filtered-batch shortcut is distinct from marked batch.
            send(b"\x1b")
            if json.loads(receipt.read_text())["calls"] != 6:
                raise AssertionError("Unexpected retry or cancelled filtered upgrade")
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
            if json.loads(receipt.read_text())["calls"] != 6:
                raise AssertionError("Unexpected extra fake mutation")
            print(
                "PTY PASS: SGR mouse tabs/rows/buttons/checkboxes, ordered provider picker, "
                "streamed base before enrichment, text/paste isolation, manager filter, search, "
                "review/cancel, explicit approval, native prompt, result acknowledgement, "
                "per-target resolution, maintenance skip/recheck, exact prompt export, "
                "hidden multi-selection, aggregate approval, batch pause/recheck, Vim paging, "
                "dashboard return, 48x16 resize, clean exit."
            )
            print("Terminal ECHO/ICANON, cursor and alternate screen restored; fake mutations: 6 (3 single jobs, 2 batch attempts, 1 explicitly reviewed retry).")
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
