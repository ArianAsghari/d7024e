# `--force` verification

| Evidence | Saved output |
| --- | --- |
| [Publication and rejection](force-attempts-terminal.png) | [demo-transcript.txt](demo-transcript.txt), lines 10–14 and 16–27 |
| [Latest version on all replicas](force-preserved-latest-terminal.png) | [demo-transcript.txt](demo-transcript.txt), lines 30–47 |
| [UDP regression test](force-udp-test-terminal.png) | Test command and complete [udp-regression.txt](udp-regression.txt) |

The images display command and output excerpts from the saved logs. The matching HTML files apply terminal formatting; the log text is unchanged.

## Run

From `labs/`:

```sh
./scripts/force-demo.sh
go test . -run '^TestPublishForceOverUDP$' -race -count=1 -v
```

For manual input:

```sh
./scripts/force-demo.sh --interactive
```

See [FORCE-DEMO.md](../../demo/FORCE-DEMO.md) for the commands. Every run creates fresh local nodes, files and keys; existing Docker nodes are unaffected.

## Transcript checksums

```text
4b436b333785897f15f9d9d30b9336bafeb06b32e13473fa071f7e4f85ee0957  demo-transcript.txt
a4049d8e53fb28efaecab09816ab3b4e8c21867552235119fd03d97a2a509601  udp-regression.txt
```
