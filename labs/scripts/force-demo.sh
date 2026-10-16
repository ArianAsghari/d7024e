#!/usr/bin/env bash
# Fresh, isolated three-node --force demonstration; Go and Python 3 are required.
set -euo pipefail
LABS_DIR="$(cd "$(dirname "$0")/.." && pwd)"
exec python3 - "$LABS_DIR" "$@" <<'PY'
import os
from pathlib import Path
import select
import socket
import subprocess
import sys
import tempfile
import time

labs = Path(sys.argv[1])
args = sys.argv[2:]
if (len(args) != len(set(args)) or any(arg not in ("--interactive", "--keyless") for arg in args)
        or ("--keyless" in args and "--interactive" not in args)):
    sys.exit("usage: scripts/force-demo.sh [--interactive [--keyless]]")
interactive = "--interactive" in args
keyless = "--keyless" in args
state = Path(tempfile.mkdtemp(prefix="kademlia-force-"))
transcript = (state / "transcript.txt").open("w")
nodes = []
logs = []


def emit(message=""):
    print(message, flush=True)
    transcript.write(message + "\n")
    transcript.flush()


def free_ports(count):
    # Reserve distinct UDP ports until all have been selected.
    sockets = []
    try:
        for _ in range(count):
            sock = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
            sock.bind(("127.0.0.1", 0))
            sockets.append(sock)
        return [sock.getsockname()[1] for sock in sockets]
    finally:
        for sock in sockets:
            sock.close()


def private_key_snapshot():
    snapshot = {}
    for node in nodes:
        directory = state / ("keys-" + node.name)
        snapshot[node.name] = (None if not directory.exists() else
                               {str(path.relative_to(directory)): path.read_bytes()
                                for path in directory.rglob("*") if path.is_file()})
    return snapshot


class Node:
    def __init__(self, name, port, bootstrap=None):
        self.name = name
        env = os.environ.copy()
        # Ignore unrelated node settings in the user's shell.
        env.pop("HEADLESS", None)
        env.pop("BOOTSTRAP", None)
        env.update(BIND_IP="127.0.0.1", PORT=str(port), RPC_TIMEOUT="2s",
                   REPLICATION_INTERVAL="1h", REFRESH_INTERVAL="1h", ALPHA="3",
                   OWNERS_FILE=str(state / "owners.json"),
                   KEYS_DIR=str(state / ("keys-" + name)))
        if bootstrap:
            env["BOOTSTRAP"] = "127.0.0.1:" + str(bootstrap)
        log = (state / (name + ".stderr.log")).open("wb")
        logs.append(log)
        self.process = subprocess.Popen([str(state / "kadlab")], cwd=state,
                                        env=env, stdin=subprocess.PIPE,
                                        stdout=subprocess.PIPE, stderr=log)
        nodes.append(self)
        self.startup = self.read_prompt()

    def read_prompt(self):
        output = bytearray()
        deadline = time.monotonic() + 60
        while not output.endswith(b"> "):
            remaining = deadline - time.monotonic()
            if remaining <= 0:
                raise RuntimeError(self.name + " did not return a prompt; inspect " + str(state))
            ready, _, _ = select.select([self.process.stdout], [], [], remaining)
            if not ready:
                continue
            chunk = os.read(self.process.stdout.fileno(), 65536)
            if not chunk:
                raise RuntimeError(self.name + " exited; inspect " + str(state / (self.name + ".stderr.log")))
            output.extend(chunk)
        return output[:-2].decode("utf-8", errors="replace").rstrip()

    def command(self, command, *expected, echo=True):
        if echo:
            emit(self.name + "> " + command)
        else:
            transcript.write(self.name + "> " + command + "\n")
            transcript.flush()
        self.process.stdin.write((command + "\n").encode())
        self.process.stdin.flush()
        output = self.read_prompt()
        emit(output)
        for fragment in expected:
            if fragment not in output:
                raise RuntimeError("Expected " + repr(fragment) + " in the output above")
        return output


try:
    emit("Three-node UDP --force demo, built from the current source")
    emit("Files, keys, logs and transcript: " + str(state))
    emit("Building Go node...")
    build_env = os.environ.copy()
    build_env["GOCACHE"] = str(Path(tempfile.gettempdir()) / "kademlia-force-build-cache")
    subprocess.run(["go", "build", "-o", str(state / "kadlab"), "."],
                   cwd=labs, env=build_env, check=True)
    (state / "v1.txt").write_text("Release 1: original package\n")
    (state / "v2.txt").write_text("Release 2: accepted latest package\n")
    (state / "v3.txt").write_text("Different bytes used for invalid update attempts\n")
    (state / "v4.txt").write_text("Unauthorized package from a node without the owner key\n")
    first, second, publisher_port = free_ports(3)
    peer1 = Node("replica-1", first)
    peer2 = Node("replica-2", second, first)
    publisher = Node("publisher", publisher_port, first)
    emit("Started publisher and two replicas on 127.0.0.1 UDP ports " +
         ", ".join(map(str, (publisher_port, first, second))))

    emit("\n1. Valid updates succeed with --force")
    publisher.command("keygen example.com", "registered example.com:")
    publisher.command("publish --force example.com:tool:1.0.0 v1.txt", "published example.com:tool:1.0.0")
    publisher.command("publish --force --prev=1.0.0 example.com:tool:2.0.0 v2.txt", "published example.com:tool:2.0.0")

    if interactive:
        emit("\nThe accepted 1.0.0 -> 2.0.0 history is ready.")
        active = peer1 if keyless else publisher
        if keyless:
            emit("You are on replica-1, which has no private key for example.com.")
            emit("Type the following at this node's prompt, one line at a time:")
            emit("publish example.com:tool:3.0.0 v4.txt")
            emit("publish --force example.com:tool:3.0.0 v4.txt")
            emit("show dns example.com")
        else:
            emit("You are on the publisher, which holds the domain owner's private key.")
            emit("Type the following at the publisher prompt, one line at a time:")
            emit("publish example.com:tool:1.5.0 v3.txt")
            emit("publish --force example.com:tool:1.5.0 v3.txt")
            emit("publish --force --prev=1.0.0 example.com:tool:3.0.0 v3.txt")
        emit("show example.com:tool")
        emit("install example.com:tool:latest installed.txt")
        emit("exit")
        emit("\nThe files above are already present. Type exit or press Ctrl+C to stop all three nodes.")
        # The script itself is supplied through stdin; /dev/tty reads user input.
        with open("/dev/tty", "r") as terminal:
            while True:
                print("> ", end="", flush=True)
                command = terminal.readline()
                if not command or command.strip() == "exit":
                    break
                active.command(command.rstrip("\n"), echo=False)
    else:
        emit("\n2. Compare publisher refusal with replica refusal")
        attempts = [
            ("roll back", "example.com:tool:1.5.0 v3.txt"),
            ("fork", "example.com:tool:2.0.0 v3.txt"),
            ("fork", "--prev=1.0.0 example.com:tool:3.0.0 v3.txt"),
        ]
        for reason, arguments in attempts:
            publisher.command("publish " + arguments, "refusing to publish", reason)
            publisher.command("publish --force " + arguments,
                              "updating the latest pointer", reason, "refused by 3 nodes")

        emit("\n3. A keyless node reaches receiver-side ownership validation only with --force")
        owners_before = (state / "owners.json").read_bytes()
        keys_before = private_key_snapshot()
        if keys_before[peer1.name] not in (None, {}):
            raise RuntimeError("The keyless node unexpectedly has a private key")
        peer1.command("publish example.com:tool:3.0.0 v4.txt", "no private key for example.com")
        peer1.command("publish --force example.com:tool:3.0.0 v4.txt",
                      "updating the latest pointer", "signer is not the domain owner", "refused by 3 nodes")
        if (state / "owners.json").read_bytes() != owners_before or private_key_snapshot() != keys_before:
            raise RuntimeError("The forced keyless attempt changed ownership or private-key files")

        emit("\n4. Every node still installs the accepted version 2.0.0")
        for node in (publisher, peer1, peer2):
            installed = node.name + "-installed.txt"
            node.command("install example.com:tool:latest " + installed,
                         "installed example.com:tool 2.0.0", ", latest)")
            if (state / installed).read_bytes() != (state / "v2.txt").read_bytes():
                raise RuntimeError(node.name + " installed incorrect package bytes")
            node.command("show example.com:tool", "2.0.0", "1.0.0")
        emit("\nPASS: valid forced updates succeed; invalid forced updates reach all three")
        emit("replicas and are rejected, including a forced keyless publication. Ownership")
        emit("and private-key files are unchanged; all three still install the exact version 2 bytes.")
        emit("Transcript: " + str(state / "transcript.txt"))
except KeyboardInterrupt:
    emit("\nStopped.")
except Exception as error:
    emit("\nFAIL: " + str(error))
    sys.exit(1)
finally:
    for node in nodes:
        if node.process.poll() is None:
            try:
                node.process.stdin.write(b"exit\n")
                node.process.stdin.flush()
                node.process.stdin.close()
            except (BrokenPipeError, OSError):
                node.process.terminate()
    for node in nodes:
        try:
            node.process.wait(timeout=5)
        except subprocess.TimeoutExpired:
            node.process.kill()
            node.process.wait()
    for log in logs:
        log.close()
    transcript.close()
PY
