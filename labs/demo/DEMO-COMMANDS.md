# Demo run sheet — copy and paste

Part 1 + Part 2. Follow the steps in order. Explanations are in
[REVIEW-WALKTHROUGH.md](REVIEW-WALKTHROUGH.md).

**S = normal terminal shell. A/B/C/D/P/X = that node's `>` prompt.**
Paste commands one line at a time; wait for the prompt before continuing.
Do not paste expected results. Avoid arrow keys; Ctrl+C stops the node.

## 1. Start a fresh demo

Docker must be running. Finish any existing demo first: this resets the
demo network, ownership, keys and downloads.

**S — from the repository root** (skip `cd labs` if already in `labs`):

```sh
cd labs
```

**S:**

```sh
scripts/demo-down.sh
printf 'file5: the fifth release of the demo package\n' > demo/files/file5.txt
scripts/demo-up.sh
docker service ls
```

Expect **bootstrap 1/1 + workers 49/49**. Leave S at the shell.

Open five more terminal tabs, each in the repository's `labs` directory.
Run each command in its own tab's **normal shell**, once:

| Tab | Startup command            |
| --- | -------------------------- |
| A   | `scripts/demo-node.sh A` |
| B   | `scripts/demo-node.sh B` |
| C   | `scripts/demo-node.sh C` |
| D   | `scripts/demo-node.sh D` |
| P   | `scripts/demo-node.sh P` |

Wait for **Joined via ...** and **`>`** on every node. Keep them open.
Start X only in step 10; starting it early can interfere with valid publications.
Run `demo-up.sh` only once during this demo.

**S — show the input files:**

```sh
docker exec demo-A cat /files/file1.txt /files/file2.txt /files/file3.txt /files/file5.txt
docker exec demo-A sha256sum /files/file1.txt /files/file2.txt /files/file5.txt
```

## 2. Part 1 — ping, store and download

**P:**

```text
ping bootstrap:8000
show rt
show ds
put /files/file1.txt
show ds
```

Expect a pong/RTT and a stored key. The `get` commands below use the supplied file1's
hash. If `put` prints a different key, use that key in all three `get` commands.

**B:**

```text
show ds
get 448969d3a84becc3c012fda5afe84e6cdf62fe4b336200b49a510be846a5d2fb /out/part1.txt
show ds
get 448969d3a84becc3c012fda5afe84e6cdf62fe4b336200b49a510be846a5d2fb
```

Expect the file; the repeated get should say **found locally**. The first get can
also be local if B was already a replica.

**S — open and compare B's file:**

```sh
docker exec demo-B cat /out/part1.txt
docker exec demo-B cmp /files/file1.txt /out/part1.txt && echo 'PASS: B downloaded file1 exactly'
```

## 3. Part 1 — retrieve after the uploader exits (**replication requirement—keeping data available despite churn**)

**P:**

```text
exit
```

**C:**

```text
get 448969d3a84becc3c012fda5afe84e6cdf62fe4b336200b49a510be846a5d2fb /out/after-p-exit.txt
```

**S:**

```sh
docker ps --filter 'name=^/demo-P$' --format '{{.Names}}'
docker exec demo-C cat /out/after-p-exit.txt
docker exec demo-C cmp /files/file1.txt /out/after-p-exit.txt && echo 'PASS: file1 survives P leaving'
```

Expect no P container name, the first-release text, and PASS.

## 4. Part 2 — publish 1; B and C install 1

**A:**

```text
keygen example.com
show dns example.com
publish example.com:tool:1.0.0 /files/file1.txt
show example.com:tool
```

**B:**

```text
show dns example.com
install example.com:tool:latest /out/latest-v1.txt
```

**C:**

```text
install example.com:tool:latest /out/latest-v1.txt
```

**S:**

```sh
docker exec demo-B cat /out/latest-v1.txt
docker exec demo-B cmp /files/file1.txt /out/latest-v1.txt && echo 'PASS: B installed version 1 bytes'
docker exec demo-C cat /out/latest-v1.txt
docker exec demo-C cmp /files/file1.txt /out/latest-v1.txt && echo 'PASS: C installed version 1 bytes'
```

Expect **installed ... 1.0.0**, first-release text and both PASS lines.
A and B should display the same owner key.

## 5. Publish 2; prove C installs 2 and D installs old version 1

**A:**

```text
publish example.com:tool:2.0.0 /files/file2.txt
show example.com:tool
```

**C:**

```text
install example.com:tool:latest /out/latest-v2.txt
show example.com:tool
```

**D:**

```text
install example.com:tool:1.0.0 /out/old-v1.txt
```

**S — show both downloaded files and their hashes:**

```sh
docker exec demo-C cat /out/latest-v2.txt
docker exec demo-D cat /out/old-v1.txt
docker exec demo-C cmp /files/file2.txt /out/latest-v2.txt && echo 'PASS: C latest is version 2 bytes'
docker exec demo-D cmp /files/file1.txt /out/old-v1.txt && echo 'PASS: D old version is still version 1 bytes'
docker exec demo-C sha256sum /files/file2.txt /out/latest-v2.txt
docker exec demo-D sha256sum /files/file1.txt /out/old-v1.txt
```

Expect **C: 2.0.0 / second release**, **D: 1.0.0 / first release**.
Both comparisons pass; each original/download hash pair matches.

**D — also request version 2 explicitly and show stored objects:**

```text
install example.com:tool:2.0.0 /out/pinned-v2.txt
show example.com:tool
show ds
```

**S:**

```sh
docker exec demo-D cat /out/pinned-v2.txt
docker exec demo-D cmp /files/file2.txt /out/pinned-v2.txt && echo 'PASS: D explicitly installed version 2 bytes'
```

Expect history **2 → 1** and PASS. A reader's `show ds` need not contain a latest pointer.

## 6. Wrong owner — refuse publication and show absence

**B:**

```text
publish example.com:intrusion:1.0.0 /files/file3.txt
keygen example.com
```

Expect **no private key** and refusal to replace the existing owner.

**C:**

```text
show dns example.com
show example.com:intrusion
install example.com:intrusion:latest /out/unauthorized.txt
```

Expect the same owner key; the package cannot be shown or installed.

**S:**

```sh
docker exec demo-C sh -c 'test ! -e /out/unauthorized.txt && echo "PASS: unauthorized install produced no file"'
```

## 7. Fork — attempt to replace version 2 with file3

**A:**

```text
publish example.com:tool:2.0.0 /files/file3.txt
publish --force example.com:tool:2.0.0 /files/file3.txt
```

Expect both attempts to fail as forks; the forced attempt should show replica refusals.

**C:**

```text
show example.com:tool
install example.com:tool:latest /out/after-fork-v2.txt
```

**S:**

```sh
docker exec demo-C cat /out/after-fork-v2.txt
docker exec demo-C cmp /files/file2.txt /out/after-fork-v2.txt && echo 'PASS: fork attempt left version 2 bytes unchanged'
```

Expect history **2 → 1**, installed **2.0.0**, second-release text and PASS.

## 8. Publish 5 successfully; then refuse 3 and 4

**A:**

```text
publish example.com:tool:5.0.0 /files/file5.txt
show example.com:tool
```

Expect success and history **5 → 2 → 1**.

**D:**

```text
install example.com:tool:latest /out/latest-v5.txt
```

**S:**

```sh
docker exec demo-D cat /out/latest-v5.txt
docker exec demo-D cmp /files/file5.txt /out/latest-v5.txt && echo 'PASS: D latest is version 5 bytes'
```

Expect **5.0.0**, fifth-release text and PASS. Now attempt rollback.

**A:**

```text
publish --force example.com:tool:3.0.0 /files/file3.txt
publish --force example.com:tool:4.0.0 /files/file3.txt
```

Expect explicit **roll back** refusals. A timeout alone does not prove rejection.

**D:**

```text
show example.com:tool
install example.com:tool:latest /out/after-rollback-v5.txt
install example.com:tool:3.0.0 /out/rejected-v3.txt
install example.com:tool:4.0.0 /out/rejected-v4.txt
```

Expect history **5 → 2 → 1**, latest **5.0.0**, and failed installs for 3 and 4.

**S:**

```sh
docker exec demo-D cat /out/after-rollback-v5.txt
docker exec demo-D cmp /files/file5.txt /out/after-rollback-v5.txt && echo 'PASS: rollback attempts left version 5 bytes unchanged'
docker exec demo-D sh -c 'test ! -e /out/rejected-v3.txt && test ! -e /out/rejected-v4.txt && echo "PASS: rejected versions produced no installed files"'
```

**Main demo complete.** Steps 9–10 are optional extras; otherwise go to step 11.

## 9. Optional — a higher version with the wrong previous record

**A:**

```text
publish --force --prev=2.0.0 example.com:tool:6.0.0 /files/file3.txt
```

Expect fork rejection.

**C:**

```text
show example.com:tool
install example.com:tool:latest /out/after-branch-v5.txt
```

**S:**

```sh
docker exec demo-C cat /out/after-branch-v5.txt
docker exec demo-C cmp /files/file5.txt /out/after-branch-v5.txt && echo 'PASS: higher-version fork left version 5 unchanged'
```

Expect history **5 → 2 → 1**, installed **5.0.0** and PASS.

## 10. Optional — rogue signature; run this last

Open another tab in `labs`. **X — normal shell:**

```sh
scripts/demo-node.sh X
```

Wait for **Joined via ...** and **`>`**. **X — node prompt:**

```text
keygen example.com
show dns example.com
publish example.com:evil:1.0.0 /files/file3.txt
```

Expect a different owner key and refusals from honest replicas. X may accept its own
pointer; C must still refuse to install it. Do not publish more legitimate releases now.

**C:**

```text
show dns example.com
show example.com:evil
install example.com:evil:latest /out/rogue.txt
```

Expect C's original trusted key and failed show/install.

**S:**

```sh
docker exec demo-C sh -c 'test ! -e /out/rogue.txt && echo "PASS: wrong-key publication cannot be installed"'
```

## 11. Finish

Have the experiment plots, test/coverage results and report ready. Be prepared to
explain missing-history catch-up; a live catch-up demo is not required.
The full checklist is in [the walkthrough](REVIEW-WALKTHROUGH.md#10-what-else-must-we-be-ready-to-showcase).

Save any outputs you want to keep. **S — only after the demo is finished:**

```sh
scripts/demo-down.sh
```
