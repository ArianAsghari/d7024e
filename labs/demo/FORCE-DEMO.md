# Test `--force` yourself

`--force` bypasses the publisher's update precheck. Receiving nodes still validate
the update. A forced rollback or fork **should fail at the replicas**. That failure,
with `updating the latest pointer` and `refused by 3 nodes`, demonstrates that the
flag worked. Without force, the same attempt fails earlier with `refusing to publish`.

The [captured run](../experiments/force-results/README.md) includes screenshots of
[ordinary versus forced attempts](../experiments/force-results/force-attempts-terminal.png)
and [the accepted latest release on all three nodes](../experiments/force-results/force-preserved-latest-terminal.png).

From this repository's `labs` directory, run the isolated demo:

```sh
bash scripts/force-demo.sh --interactive
```

It builds the current source and starts three real UDP nodes on unused loopback
ports. Go and Python 3 must be installed. It creates fresh files, owners and private
keys in a new temporary directory; the directory path is printed. Existing Docker
nodes and demo files are unaffected. The publisher uses that temporary directory
as its working directory, so the short filenames below already exist.

The script creates the owner key and successfully publishes `1.0.0` then `2.0.0`
using `--force` before opening the prompt. At the `>` prompt, enter these commands
**one at a time**:

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

For an automatic run that also verifies installation bytes and history on all
three nodes, run:

```sh
bash scripts/force-demo.sh
```

It prints each command and the node's actual output, checks each expected result,
then prints `PASS` and the saved transcript path. Each run starts a fresh network,
so you can repeat it without conflicting with earlier package versions or keys.
