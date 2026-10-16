# `--force` verification

| Evidence | Saved output |
| --- | --- |
| [Publication and rejection](force-attempts-terminal.png) | [demo-transcript.txt](demo-transcript.txt), lines 10–14 and 16–27 |
| [Keyless publisher](force-keyless-terminal.png) | [demo-transcript.txt](demo-transcript.txt), lines 30–33 and 42–47 |
| [Latest version on all replicas](force-preserved-latest-terminal.png) | [demo-transcript.txt](demo-transcript.txt), lines 36–53 |
| [UDP regression test](force-udp-test-terminal.png) | Test command and complete [udp-regression.txt](udp-regression.txt) |

The images display command and output excerpts from the saved logs. The matching HTML files apply terminal formatting; the log text is unchanged.

## Run

From `labs/`:

```sh
./scripts/force-demo.sh
go test . -run '^TestPublishForce(OverUDP|WithoutDomainKeyOverUDP)$' -race -count=1 -v
```

For manual input:

```sh
./scripts/force-demo.sh --interactive --keyless
```

See [FORCE-DEMO.md](../../demo/FORCE-DEMO.md) for the commands. Omit `--keyless` for the owner-key rollback/fork demonstration. The keyless check confirms server-side signature rejection, unchanged registered ownership and private keys, and installation of the accepted latest version. Every run creates fresh local nodes, files and keys; existing Docker nodes are unaffected.

## Transcript checksums

```text
f8b63aa30559cff028c7fc759bdefae2e93adc6532e904dd15fa7877a770da6f  demo-transcript.txt
1cefc9a69b22c4fb9b361f19c7b0ad385eb0d2373df9d1b1f9c723ff8d75808f  udp-regression.txt
```
