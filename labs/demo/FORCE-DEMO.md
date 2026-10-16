# Test `--force` yourself

`--force` bypasses the publisher's update precheck. If this node has no private key
for the domain, it signs the forced attempt with a temporary key held only in memory.
Receiving nodes still enforce ownership and history. A forced wrong-owner update,
rollback or fork **should fail at the replicas**. The output
`updating the latest pointer` with a receiver rejection demonstrates that the
attempt reached those checks.

The [captured run](../experiments/force-results/README.md) includes screenshots of
[ordinary versus forced attempts](../experiments/force-results/force-attempts-terminal.png)
and [the accepted latest release on all three nodes](../experiments/force-results/force-preserved-latest-terminal.png).

## Publish from a node without the owner's key

From this repository's `labs` directory, run:

```sh
bash scripts/force-demo.sh --interactive --keyless
```

It builds the current source and starts three real UDP nodes on unused loopback
ports. Go and Python 3 must be installed. It creates fresh files, owners and private
keys in a new temporary directory; the directory path is printed. Existing Docker
nodes and demo files are unaffected. The publisher uses that temporary directory
as its working directory, so the short filenames below already exist.

The publisher creates the owner key and publishes `1.0.0` then `2.0.0`. The prompt
then controls **replica-1**, which has no private keys. Enter one line at a time:

```text
publish example.com:tool:3.0.0 v4.txt
publish --force example.com:tool:3.0.0 v4.txt
show dns example.com
show example.com:tool
install example.com:tool:latest installed.txt
```

The ordinary attempt stops at `no private key for example.com`. The forced attempt
reports `updating the latest pointer` and `signer is not the domain owner`.
All three nodes reject the attempt, reported as `refused by 3 nodes`.
The domain owner stays the same, no private key is saved,
the history remains `2.0.0 → 1.0.0`, and installation still returns `2.0.0`.

Type `exit` to stop this run before trying the owner-key case below.

## Publish invalid history with the owner's key

Run:

```sh
bash scripts/force-demo.sh --interactive
```

This opens a fresh network at the owner publisher's prompt with the same accepted
`1.0.0 → 2.0.0` history. Enter one line at a time:

```text
publish example.com:tool:1.5.0 v3.txt
publish --force example.com:tool:1.5.0 v3.txt
publish --force --prev=1.0.0 example.com:tool:3.0.0 v3.txt
show example.com:tool
install example.com:tool:latest installed.txt
```

The ordinary `1.5.0` attempt reports
`refusing to publish` and `roll back`; the forced `1.5.0` attempt reports
`updating the latest pointer`, `roll back`, and `refused by 3 nodes`. The forced
`3.0.0` attempt reports `fork` and `refused by 3 nodes`: linking to `1.0.0`
skips the accepted `2.0.0` record, which creates a competing history. The final
commands must show `2.0.0 → 1.0.0` and install `2.0.0`.

To compare an ordinary and forced same-version fork too, enter:

```text
publish example.com:tool:2.0.0 v3.txt
publish --force example.com:tool:2.0.0 v3.txt
show example.com:tool
```

Type `exit` or press Ctrl+C when finished. The script stops only its three nodes.
It retains the temporary files and logs for inspection. To inspect the downloaded
bytes in your normal shell, replace the path below with the printed directory:

```sh
cat /COPY_PRINTED_DIRECTORY/installed.txt
```

Expect `Release 2: accepted latest package`.

## Check both cases automatically

For an automatic run covering valid forced updates, invalid history, and the
keyless forced attempt, run:

```sh
bash scripts/force-demo.sh
```

It checks ownership and private-key files are unchanged by the keyless attempt,
then verifies the installed bytes and accepted history on all three nodes. It
prints each command and output, followed by `PASS` and the saved transcript path.
Each run starts a fresh network, so you can repeat it without conflicting with
earlier package versions or keys.
