# Abrupt-departure measurements

Measured on 16 October 2026 with Go 1.26.3, darwin/arm64. The working tree was based on `34889624d34f412e32eb95b028bba39b91c4ef14`, with the accompanying churn runner and analysis changes. These are new measurements, independent of the older mandatory scalability/packet-loss datasets.

From `labs/`, reproduce the full sweep and regenerate its tables and figures:

```sh
go run ./cmd/experiments -exp churn -seeds 10 -workers 4 -out experiments/churn-results
python3 scripts/analyze.py experiments/churn-results
```

To preserve this observed dataset while trying another run, use a different `-out` directory. A small integration check is available with `-quick -seeds 1`. The analysis has no third-party dependencies. Its availability/routing accounting can be checked with:

```sh
PYTHONDONTWRITEBYTECODE=1 python3 -m unittest discover -s scripts -p test_analyze.py
```

The recorded `churn.jsonl` SHA-256 is `db39f754c3c63babd3c8e2255c692713a5465990bc9cc68259b3e26ea5e48c14`. It contains 60 run labels, 9,000 per-lookup observations, 3,000 per-key replica observations and 60 repair summaries. `churn_runs.csv` exposes seed-level data; `churn.csv` and `summary.md` aggregate it. Figures are ordinary SVG files.

## Method

Each crash fraction (0%, 10%, 30%, 50%, 80%, 90%) and seed (1–10) starts with a fresh 1,000-node network, with k = 10 and alpha = 3. Nodes join through the normal bootstrap/self-lookup/bucket-refresh procedure. Fifty random, distinct, content-addressed values of 16–256 bytes are stored under healthy conditions. There are exactly ten copies per key before crashes in this dataset. A separate seeded permutation abruptly removes the requested fraction of nodes. For a given seed the crash sets are nested across fractions, and addresses, stored values and random target inputs are paired across fractions.

After removals, surviving nodes perform 100 random-target FIND_NODE lookups and one FIND_VALUE lookup for each of the 50 keys. Every value lookup starts on a live node that does not hold that key. FIND_NODE accuracy is checked against the globally closest **live** peer, excluding the origin. FIND_VALUE success requires the core's hash-checked remote result. The ground-truth count of original surviving copies separates complete data loss from an unsuccessful route to a value that still exists.

Per-lookup probes and failed probes are counted by the simulator's completed FIND_NODE/FIND_VALUE RPC observer. PINGs and STOREs are excluded. Rounds come from the core's lookup-completion event. No packet loss, retries, simulated delays, arrivals or periodic maintenance workers run during measurement. Dead-node RPCs fail immediately in the simulator: these results measure logical work and rounds, **not elapsed latency or real UDP timeout cost**. The workload progressively updates routing tables and caches values; these are averages over that workload rather than untouched routing-table snapshots.

After the measured lookups, one manual repair pass selects an original surviving holder per retained key, deduplicates the source nodes, and calls each selected node's `Republish` once in node-index order. Sources are selected before reads, so source selection does not depend on measurement-created caches. `Republish` does process the selected node's current store, including any caches added by those reads, which can affect repair outcomes. Replica counts are taken before crashes, immediately after crashes, after reads/before repair, and after repair. Post-repair counts include the reads' immutable caches and may exceed k. Coverage of the actual globally closest k live nodes is counted independently.

Means weight each seed equally; error bars and `*_std` columns are the sample standard deviation of the ten seed means. Success and availability columns pool the equal-size seed workloads. Reported retained-key copy counts average each seed's mean over its surviving keys; their denominator excludes keys whose last original copy crashed.

## Findings and limits

At 50% crashes all 1,000 node lookups and 500 value lookups still succeeded, but node lookup work rose from 13.53 probes / 4.96 rounds to 22.75 probes / 8.61 rounds. At 80% crashes only 848/1,000 node lookups found the actual closest live peer, while 397/500 value lookups succeeded. Fifty-four values had no remaining original copy, and 49 failures were routing misses despite a surviving copy. At 90% crashes those counts were 415/1,000 and 201/500, with 184 completely lost values and 115 routing misses. The lower mean node work at 90% than 80% is consistent with earlier exhaustion/failure; it does not demonstrate improved routing.

Replication is eroded before lookup success collapses: mean original live copies fell from 10.00 to 9.05, 6.99, 4.98, 1.87 and 0.93 as crashes increased. A single republish pass added copies, but did not guarantee ten copies or placement on all ten nearest live nodes. For example, at 50% crashes retained keys averaged 6.95 copies after repair, including caches, and only 9/500 keys occupied all ten true nearest live nodes. A successful repair call therefore does not establish complete repair. Nearby holders can retain a stale, limited referral set; this experiment does not evaluate whether repeated refresh/republish cycles restore full coverage.

This is a controlled **abrupt-departure fraction** experiment, not continuous churn expressed as departures per second. It does not measure new joins during measurement, correlated failures, lossy joins, periodic recovery timing or wall-clock latency. Seeds fix addresses, value/workload draws and removed nodes. The join implementation also uses cryptographic randomness for bucket-refresh targets, and concurrent RPC scheduling can change routing-table state; reruns need not reproduce individual observations byte for byte. The committed raw log records the observed run used by the report.
