# Proposed review walkthrough: Part 1 and Part 2, with visible evidence

This is a proposed presentation sequence for the group. It uses Kim's existing
demo-tools scripts and adds explicit checks of downloaded files, the accepted history,
and the state after rejected publications. It works with the original demo-tools
branch without application or script changes.

The lecturer's corrected sequence is included: **publishing version 5 after version 2
is valid; subsequently trying to publish version 3 or 4 must be rejected.** A live
missing-history catch-up demonstration is not required by that announcement, but we
must explain how it works.

Sources: [lab requirements](../LAB-SPEC.md), [Part 2 design and oral questions](../PART2-DESIGN.md),
[our report](../../REPORT.md), and the lecturer's announcement and correction shared
with the group. The suggested order below is our proposal, not an additional official
presentation format or time limit.

## 1. Plan and preparation

Use sections 2–7 as the main live sequence. Section 8 adds a receiver-side ownership
attack and a second kind of fork. Sections 9–11 provide explanations and supporting
evidence. Agree on the available presentation time before deciding which optional
extensions to run live.

### Seven terminal tabs

| Tab | Purpose                                                      | What runs there              |
| --- | ------------------------------------------------------------ | ---------------------------- |
| S   | Control shell: Docker, file checks, tests                    | Normal host shell throughout |
| A   | Legitimate owner and package publisher                       | Node A                       |
| B   | Initial downloader; unauthorized publication attempt         | Node B                       |
| C   | Reader that sees version 1, then updates to version 2        | Node C                       |
| D   | Reader that installs old and explicitly selected versions    | Node D                       |
| X   | Optional rogue publisher with a separate fake ownership file | Start only in section 8B     |
| P   | Part 1 publisher; deliberately exits during the demo         | Node P                       |

All host commands below assume the shell is in this repository's **labs** directory.
From the repository root, run:

```sh
cd labs
```

If the prompt already ends in **labs %**, skip that command. Enter the same labs
directory in every new tab. No temporary checkout or machine-specific path is needed.

**Two different prompts:** commands such as docker, scripts/demo-node.sh, cd and clear
belong in the normal shell. Commands such as put, get, publish and install belong at
the node's **>** prompt. Do not paste expected output as input. The node CLI reads
plain lines; it does not provide arrow-key editing or command history. Ctrl+C stops
the node rather than clearing its current command.

### Start a fresh rehearsal, once

This setup resets the demo's shared ownership file, private keys, downloads and
in-memory network. Finish any existing rehearsal before doing it. Do not run
another startup in parallel with the manual demo. If continuing a
partially completed rehearsal, first identify the current version instead of blindly
repeating key generation and publication.

**S — normal shell:**

```sh
scripts/demo-down.sh
printf 'file5: the fifth release of the demo package\n' > demo/files/file5.txt
scripts/demo-up.sh
docker service ls
```

The extra file5 fixture makes version 5 visibly different from versions 1 and 2. The
original scripts already mount the whole demo/files directory into every interactive
node at /files, so no script or application change is needed for this fixture.

Wait for bootstrap **1/1** and workers **49/49**. These are the 50 background nodes.
Run the following command in each corresponding tab, **once per tab**:

| Tab | Command at the normal shell prompt |
| --- | ---------------------------------- |
| A   | scripts/demo-node.sh A             |
| B   | scripts/demo-node.sh B             |
| C   | scripts/demo-node.sh C             |
| D   | scripts/demo-node.sh D             |
| P   | scripts/demo-node.sh P             |

Wait for every tab to print **Joined via ...** and show **>**. Starting Kademlia node
alone does not mean joining has finished. Keep A/B/C/D open throughout the main
demo. Only P exits when instructed. We now have 50 background nodes plus five
interactive nodes; all use port 8000 on separate container IP addresses.

**Leave X's tab at the host shell for now.** Its separate ownership file is deliberately
untrusted. If X joins early and becomes a pointer replica, it can reject an honest
publication and cause a partially applied update. Start it only for the final optional
attack, after all legitimate publications and history checks are complete.

### Show the input files before publishing

**S — normal shell:**

```sh
docker exec demo-A cat /files/file1.txt /files/file2.txt /files/file3.txt /files/file5.txt
docker exec demo-A sha256sum /files/file1.txt /files/file2.txt /files/file5.txt
```

Expected file contents, one line per file:

```text
file1: the first release of the demo package
file2: the second release of the demo package
file3: a different file that tries to replace release 2
file5: the fifth release of the demo package
```

The filename does not set the registry version. The version argument to publish does.
We use file3 as replacement/attack content, and create file5 for a legitimate release.

## 2. Part 1: discovery, content addressing and surviving the publisher's exit

**P — node prompt:**

<!-- node: P -->

```text
ping bootstrap:8000
show rt
show ds
put /files/file1.txt
show ds
```

**Show and explain:** ping prints a pong and round-trip time; the routing table has
contacts learned through joining. The bootstrap is an initial contact, not a central
file server. Node IDs and file keys occupy the same 256-bit space, and XOR ranks
closeness to a target. The requester follows referrals itself during iterative lookup.

**The storage flow:** put hashes the file bytes, looks up nodes close to that hash,
and sends STORE to up to k = 10 selected reachable nodes. Each receiver checks that
the bytes hash to the requested key. A get first checks local storage; otherwise it
uses FIND_VALUE probes. A peer replies with either the value or closer contacts to
try. The downloader also verifies the hash before accepting the bytes. Knowing a
node's address is useful only if that node is running and reachable over the network.

**Join versus ping:** ping checks one contact and measures a round trip. Join starts
with that contact, looks up the joining node's own ID, and refreshes the appropriate
buckets to discover more peers. The demo scripts perform join automatically through
the bootstrap setting; the later ping does not create the entire network.

Put prints the full SHA-256 key. For the unchanged supplied file1, it is:

```text
448969d3a84becc3c012fda5afe84e6cdf62fe4b336200b49a510be846a5d2fb
```

Compare it with S's sha256sum output. The following get commands use that exact key;
if anyone edits file1, use the new key actually printed by put instead.

**B — node prompt:**

<!-- node: B -->

```text
show ds
get 448969d3a84becc3c012fda5afe84e6cdf62fe4b336200b49a510be846a5d2fb /out/part1.txt
show ds
```

**S — verify B's downloaded bytes:**

<!-- verify -->

```sh
docker exec demo-B cat /out/part1.txt
docker exec demo-B cmp /files/file1.txt /out/part1.txt && echo 'PASS: B downloaded file1 exactly'
```

Cmp compares the actual bytes. It is silent on success; the following PASS is printed
only if the comparison succeeds. A missing file or differing contents must not be
presented as a successful check.

B may report a remote source or **found locally**: B could already be one of the k
STORE replicas. Do not promise an empty data store or describe every local copy as a
read cache. If the first get was remote, repeating it locally demonstrates caching:

**B — node prompt:**

<!-- node: B -->

```text
get 448969d3a84becc3c012fda5afe84e6cdf62fe4b336200b49a510be846a5d2fb
```

**P — node prompt, deliberately stop the publisher:**

<!-- node: P -->

```text
exit
```

**C — node prompt, retrieve after P has exited:**

<!-- node: C -->

```text
get 448969d3a84becc3c012fda5afe84e6cdf62fe4b336200b49a510be846a5d2fb /out/after-p-exit.txt
```

**S — prove P is gone and C still retrieved the bytes:**

```sh
docker ps --filter 'name=^/demo-P$' --format '{{.Names}}'
```

That command should print no container name. Then:

<!-- verify -->

```sh
docker exec demo-C cat /out/after-p-exit.txt
docker exec demo-C cmp /files/file1.txt /out/after-p-exit.txt && echo 'PASS: file1 survives P leaving'
```

**Say:** copies outlive the original uploader. This demonstrates availability from
replicas/caches after P exits. It does not demonstrate periodic repair of replicas;
that is separate maintenance behavior, covered by the tests in section 10.

## 3. Part 2: establish ownership, publish 1, visibly install 1

**A — node prompt:**

<!-- node: A -->

```text
keygen example.com
show dns example.com
publish example.com:tool:1.0.0 /files/file1.txt
show example.com:tool
```

**B — node prompt:**

<!-- node: B -->

```text
show dns example.com
install example.com:tool:latest /out/latest-v1.txt
```

**C — node prompt, also read version 1 before the update:**

<!-- node: C -->

```text
install example.com:tool:latest /out/latest-v1.txt
```

**S — inspect and verify both installations:**

<!-- verify -->

```sh
docker exec demo-B cat /out/latest-v1.txt
docker exec demo-B cmp /files/file1.txt /out/latest-v1.txt && echo 'PASS: B installed version 1 bytes'
docker exec demo-C cat /out/latest-v1.txt
docker exec demo-C cmp /files/file1.txt /out/latest-v1.txt && echo 'PASS: C installed version 1 bytes'
```

Expect both install messages to identify **1.0.0**, and both files to contain the
file1 text. A and B should display the same trusted owner public key/fingerprint.

**Say:** fake DNS maps example.com to its trusted public key. A holds the matching
private key and signs the metadata. Readers use the public key to verify those
signatures, then check the downloaded file against the signed blob hash. The public
key is not a password, decryption key, or address used to download from example.com.

There are two uses of DNS-like names here: Docker resolves bootstrap to a container
IP, while the registry's fake owners.json maps a package domain to a public key.

## 4. Publish 2; prove C has 2 and D can still obtain 1

**A — node prompt:**

<!-- node: A -->

```text
publish example.com:tool:2.0.0 /files/file2.txt
show example.com:tool
```

**C — node prompt, the same reader that previously installed latest as 1:**

<!-- node: C -->

```text
install example.com:tool:latest /out/latest-v2.txt
show example.com:tool
```

**D — node prompt, explicitly request the older release:**

<!-- node: D -->

```text
install example.com:tool:1.0.0 /out/old-v1.txt
```

**S — show the actual C and D files next to their expected originals:**

<!-- verify -->

```sh
docker exec demo-C cat /out/latest-v2.txt
docker exec demo-D cat /out/old-v1.txt
docker exec demo-C cmp /files/file2.txt /out/latest-v2.txt && echo 'PASS: C latest is version 2 bytes'
docker exec demo-D cmp /files/file1.txt /out/old-v1.txt && echo 'PASS: D old version is still version 1 bytes'
docker exec demo-C sha256sum /files/file2.txt /out/latest-v2.txt
docker exec demo-D sha256sum /files/file1.txt /out/old-v1.txt
```

Expected: C's file contains the **second release**, D's file contains the **first
release**, and each original/download hash pair matches. The two release hashes
differ. The registry install messages must also name 2.0.0 and 1.0.0 respectively.

**D — additionally demonstrate installing a specific newer version:**

<!-- node: D -->

```text
install example.com:tool:2.0.0 /out/pinned-v2.txt
show example.com:tool
show ds
```

**S — verify D's explicit version 2 installation:**

<!-- verify -->

```sh
docker exec demo-D cat /out/pinned-v2.txt
docker exec demo-D cmp /files/file2.txt /out/pinned-v2.txt && echo 'PASS: D explicitly installed version 2 bytes'
```

Expected history is **2.0.0 → 1.0.0**. Separate output filenames keep old downloads
available for inspection; reusing one filename would overwrite that local output file.

### Explain what show ds and show DOMAIN:PACKAGE mean

Show ds inventories one node's local DHT objects. Show example.com:tool fetches and
displays the package history. Install retrieves a selected release; it does not print
the history automatically.

```text
stable latest-pointer key: SHA256("example.com:tool:latest")
          |
          v
signed pointer naming 2.0.0 and its record hash
          |
          v
record 2.0.0 -------- previous-record hash --------> record 1.0.0
    |                                                   |
    v                                                   v
file2 blob                                          file1 blob
```

Blobs and signed records have content-derived hash keys. The latest pointer has a
name-derived key so its value can change without changing how readers find it.
StoreAt sends the value under that chosen key. Receiving nodes use the registry
validator registered through SetValueValidator before accepting the mutable value.

**The install flow:** compute the stable pointer key, obtain and validate its signed
pointer and referenced records, select latest or follow previous-record links to the
requested older version, then fetch the blob by its signed hash and write the output
file. The DHT finds these objects; example.com is an ownership namespace, not the
website from which the package bytes are downloaded.

A reader may show immutable records and blobs without a stored latest pointer. Mutable
reads do not create extra pointer copies; explicit STORE establishes pointer replicas.
This demo shows C observing an update, while dedicated tests deliberately select
non-replica readers to prove the stale-pointer cache regression is fixed.

## 5. Attempt another owner's domain; show no package was published

Use example.com here because it already has a known owner, A. B does not have A's
private key. Use a different package name so absence is easy to inspect.

**B — node prompt:**

<!-- node: B -->

```text
publish example.com:intrusion:1.0.0 /files/file3.txt
keygen example.com
```

Expected: publication fails with **no private key for example.com**; keygen refuses
to replace an existing owner. Do not delete or overwrite the shared ownership file.

**C — node prompt:**

<!-- node: C -->

```text
show dns example.com
show example.com:intrusion
install example.com:intrusion:latest /out/unauthorized.txt
```

Expected: the trusted owner key is unchanged, and the intrusion package cannot be
shown or installed in this controlled network.

**S — show that install did not produce a file:**

<!-- verify -->

```sh
docker exec demo-C sh -c 'test ! -e /out/unauthorized.txt && echo "PASS: unauthorized install produced no file"'
```

**Say:** this first attempt was refused locally because B could not sign. Section 8
adds an actual wrong-key signature sent to the receiving replicas.

## 6. Attempt to replace version 2; prove latest and its bytes remain unchanged

**A — node prompt, first ordinary publication, then forced publication:**

<!-- node: A -->

```text
publish example.com:tool:2.0.0 /files/file3.txt
publish --force example.com:tool:2.0.0 /files/file3.txt
```

Expected: both fail as forks. The first is caught by the publishing client; the forced
attempt reaches receiver validation and should report refusals by the replicas.

**C — node prompt, check accepted state after the attack:**

<!-- node: C -->

```text
show example.com:tool
install example.com:tool:latest /out/after-fork-v2.txt
```

**S — verify it is still exactly file2:**

<!-- verify -->

```sh
docker exec demo-C cat /out/after-fork-v2.txt
docker exec demo-C cmp /files/file2.txt /out/after-fork-v2.txt && echo 'PASS: fork attempt left version 2 bytes unchanged'
```

Expect latest **2.0.0**, history **2 → 1**, and the second-release text. Since file2
and file3 differ, matching file2 also establishes that the proposed replacement was
not installed. Compare the printed record hashes before and after if showing the
exact accepted head on screen.

**Say:** being the owner does not permit replacing an accepted version with another
record. Force skips the client's prechecks; it does not disable the receivers' rules.

Failed forced publication can leave unreferenced immutable blobs/records in the DHT.
The security claim is that the accepted latest pointer/history did not change, not
that no raw bytes from the attempt exist anywhere.

## 7. Corrected lecturer sequence: publish 5, refuse 3 and 4

**A — node prompt, ordinary valid publication:**

<!-- node: A -->

```text
publish example.com:tool:5.0.0 /files/file5.txt
show example.com:tool
```

Expected: success without force, with history **5.0.0 → 2.0.0 → 1.0.0**. Versions
must increase but need not be consecutive integers. By default the new record points
to the currently accepted latest record, here version 2.

**D — node prompt:**

<!-- node: D -->

```text
install example.com:tool:latest /out/latest-v5.txt
```

**S — verify the new release before attempting rollback:**

<!-- verify -->

```sh
docker exec demo-D cat /out/latest-v5.txt
docker exec demo-D cmp /files/file5.txt /out/latest-v5.txt && echo 'PASS: D latest is version 5 bytes'
```

**A — node prompt, attempt the older versions after 5 was accepted:**

<!-- node: A -->

```text
publish --force example.com:tool:3.0.0 /files/file3.txt
publish --force example.com:tool:4.0.0 /files/file3.txt
```

Expected: both fail as rollbacks. In the stable 50-node rehearsal, expect the k = 10
pointer replicas to refuse them. A timeout is not equivalent to an explicit refusal.

**D — node prompt, inspect the result after both attempts:**

<!-- node: D -->

```text
show example.com:tool
install example.com:tool:latest /out/after-rollback-v5.txt
install example.com:tool:3.0.0 /out/rejected-v3.txt
install example.com:tool:4.0.0 /out/rejected-v4.txt
```

Expected: latest still installs as **5.0.0**, history remains **5 → 2 → 1**, and
versions 3 and 4 are not found in that history.

**S — verify unchanged latest bytes and absence of rejected outputs:**

<!-- verify -->

```sh
docker exec demo-D cat /out/after-rollback-v5.txt
docker exec demo-D cmp /files/file5.txt /out/after-rollback-v5.txt && echo 'PASS: rollback attempts left version 5 bytes unchanged'
docker exec demo-D sh -c 'test ! -e /out/rejected-v3.txt && test ! -e /out/rejected-v4.txt && echo "PASS: rejected versions produced no installed files"'
```

This completes the lecturer's corrected main sequence with visible file evidence.

## 8. Additional safety demonstrations, if time permits

### A. A higher version can still be a fork

The current accepted head must still be version 5 from section 7.

**A — node prompt:**

<!-- node: A -->

```text
publish --force --prev=2.0.0 example.com:tool:6.0.0 /files/file3.txt
```

Expected: fork rejection. Although 6 is newer than 5, the proposed link **6 → 2**
bypasses the exact accepted version-5 record. Its ancestry does not extend the head.

**C — node prompt:**

<!-- node: C -->

```text
show example.com:tool
install example.com:tool:latest /out/after-branch-v5.txt
```

**S — verify the accepted content again:**

<!-- verify -->

```sh
docker exec demo-C cat /out/after-branch-v5.txt
docker exec demo-C cmp /files/file5.txt /out/after-branch-v5.txt && echo 'PASS: higher-version fork left version 5 unchanged'
```

### B. Wrong-key signature rejected by receivers

Run this last: X introduces a node with a deliberately different trust configuration.
Do not publish further legitimate releases during this optional scenario.

**X — normal shell, start the reserved node now:**

```sh
scripts/demo-node.sh X
```

Wait for Joined via and the node prompt before continuing.

X uses a private rogue owners file. Its invented owner key does not change the trusted
shared file used by A/B/C/D and the 50 background nodes.

**X — node prompt:**

<!-- node: X -->

```text
keygen example.com
show dns example.com
publish example.com:evil:1.0.0 /files/file3.txt
```

Expected: X can sign using its own private key, but the honest receiving replicas
reject the pointer because it is not signed by the owner they trust for example.com.
X itself might be selected as a replica and accept its own pointer; that does not make
it valid to the honest nodes. Show their refusal and C's failed install below.

**C — node prompt:**

<!-- node: C -->

```text
show dns example.com
show example.com:evil
install example.com:evil:latest /out/rogue.txt
```

**S — confirm no rogue installation:**

<!-- verify -->

```sh
docker exec demo-C sh -c 'test ! -e /out/rogue.txt && echo "PASS: wrong-key publication cannot be installed"'
```

Contrast the two displayed public keys. A valid signature under X's invented key is
insufficient: receivers verify against the trusted key for the domain.

## 9. Explain catch-up; a live disconnected-node setup is not required

Draw a separate hypothetical history where versions 3 and 4 really were published:

```text
incoming history: 5 → 4 → 3 → [2] → 1
known checkpoint: the exact record hash at [2]
```

The node receives pointer 5, verifies ownership/signature, and follows the hashes of
its version records backward. It calls LookupData to fetch missing records, verifies
their hashes, signatures, package identity and version ordering, and checks that the
chain reaches the **exact version-2 record hash it previously accepted**. Only then
does it accept the new pointer. If required history cannot be fetched, it keeps the
old pointer. It does not need all historical package blobs just to validate the chain.

In our actual live history **5 → 2 → 1**, records 3 and 4 never existed. The validator
follows links; it does not invent records for skipped numeric version labels.

### Limitations to state accurately

- A node with no accepted checkpoint can validate a complete history but cannot know
  about a competing history it has never seen. This is not global consensus.
- Non-replica reads do not retain mutable pointer copies. Actual replicas that miss
  an update can still serve an older accepted pointer; latest is not a guarantee of
  the globally freshest release. Non-replica readers keep no mutable checkpoint.
- A publication timeout can mean that some receivers accepted, or will accept later.
  Check the observed head; do not describe every timeout as a successful rejection.
- Lookup failure can mean absence or lack of reachable peers. Our absence checks are
  evidence from a controlled running demo, not a general proof about every partition.
- The DHT store is in memory, and values have a 4096-byte size limit. There is no TTL.
  If all copies disappear, the value is lost. Downloaded output files and private-key
  files are separate filesystem state; they do not reload the DHT after a restart.

## 10. What else must we be ready to showcase?

The announcement focuses the live Part 2 sequence. The full lab specification still
applies, and every member should be able to demonstrate and explain each requirement.
Keep supporting test output, plots and source locations ready rather than promising
that the short live sequence itself proves every engineering property.

| Requirement or topic                                 | Evidence to have ready                                                                                                                      |
| ---------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------- |
| 50 separate node containers                          | Service listing showing 1 bootstrap + 49 workers; interactive nodes are additional                                                          |
| Part 1 commands and routing                          | Section 2: ping/RTT, put/hash, get/source, exit, show rt, show ds                                                                           |
| Full join and bucket refresh                         | Explain auto-bootstrap; show Join in maintenance.go and its focused test                                                                    |
| XOR and iterative lookup; adjustable alpha and k     | Explain node IDs vs file keys; alpha=3 concurrent probes, k=10 replicas/bucket capacity; code/tests and experiment plots                    |
| UDP RPC design                                       | PING, STORE, FIND_NODE, FIND_VALUE; random request IDs, matching replies, deadlines and retries; network.go                                 |
| Immutable integrity and explicit size limit          | File/hash checks; store/lookup rejection tests; documented 4096-byte limit, exceeding the required 255-byte minimum                         |
| Initial replication vs periodic repair under churn   | Section 2 shows surviving copies; separate maintenance tests prove periodic republishing and replica restoration                            |
| At least 1000 simulated nodes, including Part 2      | Both named thousand-node scenarios below; they use the simulated transport, not 1000 containers                                             |
| At least 80% statement coverage and race-clean tests | Full test run with coverage and -race; read the actual total from the generated profile                                                     |
| Ownership, signatures and registry CLI               | Sections 3, 5 and optional 8B; show dns and decoded show ds; private keys stay private                                                      |
| No fork and no rollback                              | Sections 6–7 and optional 8A; inspect history and actual bytes after each refusal                                                          |
| Missing-history catch-up                             | Section 9 explanation plus focused tests; lecturer explicitly waived the live scenario                                                      |
| Mutable pointer cache fix                            | C reads before/after update; targeted tests select non-replica readers and publishers                                                       |
| Thread safety                                        | Explain mutex-protected state, validation/network fetches outside storage locks, and rechecking the current value before committing         |
| Structured instrumentation                           | Lookup probe and outcome events; experiment JSONL logs and external Python analysis                                                         |
| Two required experiments                             | Scalability vs network size and retrieval success vs packet loss; setup, seeds, variation, expectations and limitations                     |
| Written report and administration                    | Group/members/repository, architecture/libraries, thread safety, limits, experiments; verify Canvas submission and review signup separately |

### Prepare test evidence before the presentation

**S — normal shell in labs; these can take minutes:**

```sh
go test -count=1 -v ./experiment ./registry/dht -run '^TestThousandNode(Network|Registry)$'
```

Do not add -short: both large tests skip themselves in short mode.

For complete verification:

```sh
go test -count=1 -race -coverpkg=./... -coverprofile=/tmp/kademlia-review-coverage.out ./...
go tool cover -func=/tmp/kademlia-review-coverage.out
go vet ./...
```

The full run already includes the two large tests. The first command is useful when
showing them explicitly by name; running both is not necessary just to avoid skipping
them. Coverage measures executed statements, not the percentage of tests that pass.
Race detection covers executed paths, not every possible concurrent schedule.

Focused catch-up/cache evidence:

```sh
go test -count=1 -v ./registry/dht -run '^Test(MissingHistoryCatchUp|BehindNodeThatCannotFetchHistoryKeepsItsPointer|NonReplicaReadsLatestAfterPublication|PublisherReadsLatestWithoutKeepingAnExtraPointer)$'
```

Focused Part 1 maintenance, integrity and concurrency evidence:

```sh
go test -count=1 -v ./kademlia -run '^Test(FullBucketPingsLeastRecentlySeen|JoinLooksUpSelfThenRefreshesFartherBuckets|LookupParallelismAndProbeLogs|StoreValueRejectsMismatchedKey|LookupDataRejectsHashMismatch|ValueSizeLimitAtAllStoreEntryPoints|RepublishRestoresReplicaAfterChurnAndKeepsLocalValue|PeriodicReplicationStopsOnCancellation|ValueValidatorRechecksConcurrentUpdates)$'
```

These checks are preparation/supporting evidence; do not spend the live review
waiting for every long test unless requested.

### Have the plots open

Use the committed [scalability plot](../experiments/results/scalability.svg),
[packet-loss plot](../experiments/results/reliability.svg), and
[numerical summary](../experiments/results/summary.md).

- Scalability: N=16–2048, k=10, alpha=3, ten seeds per size, no loss. Average node
  probes grew 10.0→14.2 and value probes 3.0→6.6; all 8000 measured lookups succeeded.
  The dashed logarithmic curves are least-squares fits to the measurements.
- Reliability: 500 nodes; compare alpha=1/3 and zero/two retries across loss rates.
  At 50% per-leg loss, alpha=3 achieved 99.7% success with two retries vs 83.7%
  without retries. The dashed curves are probability-model predictions, not fitted
  lines: q=1-[1-(1-p)^2]^(R+1), then success=1-(1-q)^k under direct, independent
  replica contacts. Actual lookups must first discover replicas.
- Error bars show standard deviation across seeds. Probes omit retransmissions;
  rounds are batches, not milliseconds. Loss begins after healthy setup. Read caches
  evolve, and these two plots do not measure churn.

### Useful source files to open if asked

| Topic                                    | File                                                                                                          |
| ---------------------------------------- | ------------------------------------------------------------------------------------------------------------- |
| Startup, commands and configuration      | [main.go](../main.go), [registry_cli.go](../registry_cli.go)                                                    |
| Lookup/store and mutable validation hook | [kademlia.go](../kademlia/kademlia.go), [value.go](../kademlia/value.go)                                        |
| Joining and periodic maintenance         | [maintenance.go](../kademlia/maintenance.go)                                                                   |
| RPCs and simulator                       | [network.go](../kademlia/network.go), [simnetwork.go](../kademlia/simnetwork.go)                                |
| Owner/signature/history validation       | [validate.go](../registry/validate.go), [records.go](../registry/records.go), [owners.go](../registry/owners.go) |
| Catch-up/cache tests                     | [dht_test.go](../registry/dht/dht_test.go)                                                                     |
| Experiments and fitted/model curves      | [experiment.go](../experiment/experiment.go), [analyze.py](../scripts/analyze.py)                               |

## 11. Quick oral checks for every group member

Try answering before reading the right column.

| Question                                                                | Answer to convey                                                                                                                                                     |
| ----------------------------------------------------------------------- | -------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Why does a matching file hash not prove who published it?               | Anyone can hash/upload bytes. The trusted owner's signature authorizes the metadata containing that hash.                                                            |
| Why have a mutable pointer and immutable records?                       | A stable name-derived key locates the changing head; content-addressed records and blobs retain older releases.                                                      |
| Why is the pointer structured and signed instead of just a record hash? | The signature binds the domain, package, version and record reference; validators can check its identity and consistency with the referenced record.                 |
| What makes an incoming STORE an update?                                 | The receiver has a current accepted value under the same pointer key and compares the incoming value/history against it.                                             |
| Why repeat domain, package and version in pointer and record?           | Those signed fields bind the object to its namespace and release; validation checks they agree.                                                                      |
| Why do versions need a total order?                                     | Nodes need a consistent newer/older comparison for rollback checks. Labels without a defined order would not provide it.                                             |
| Why is 5 after 2 allowed but 3 after 5 rejected?                        | Gaps are allowed; moving the head backward is not.                                                                                                                   |
| Why can 6 pointing to 2 be a fork when latest is 5?                     | Its history bypasses the exact record already accepted as the head. A larger number alone is insufficient.                                                           |
| What if a behind node cannot fetch missing records?                     | It rejects that pointer update and keeps its old accepted pointer.                                                                                                   |
| Does every install obtain the globally newest release?                  | No. Non-replica read caching was fixed, but a replica that missed an update can still answer with an older accepted pointer.                                         |
| Why do all containers use port 8000?                                    | They have different IP/network namespaces. Joined via names their shared bootstrap contact, not their own address.                                                   |
| Does force disable security?                                            | It skips publisher-side checks to exercise receiver validation; receivers still enforce their rules.                                                                 |
| Do we need a slide deck or a live catch-up setup?                       | The supplied spec/announcement does not require slides, and the announcement waives live catch-up. Be able to explain it and check any separate Canvas instructions. |

## 12. Finish and reset after the presentation

Keep the output files visible until all comparisons are complete. Then end the
remaining interactive nodes with exit, or use the cleanup script from S:

```sh
scripts/demo-down.sh
```

Cleanup removes the demo stack, shared owners file and temporary keys/downloads.
The generated demo/files/file5.txt fixture remains in the repository directory.
Save any desired screenshots or outputs before cleanup. A later rehearsal starts
from section 1 with fresh demo state.

## Suggested message when sharing this file

> I made a fuller walkthrough using the existing demo tools. Could we use it as the
> running order? It shows C installing version 2 and D installing old version 1,
> opens the actual downloaded files, compares their bytes, and checks that latest
> stays unchanged after invalid updates. It also follows the lecturer's corrected
> version-5-then-3/4 sequence and includes a checklist for the tests, experiments,
> catch-up explanation and report. The core flow is sections 2–7; section 8 has
> extra security demonstrations if time allows.
