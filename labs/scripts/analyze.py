#!/usr/bin/env python3
"""Analyse the event logs written by `go run ./cmd/experiments`.

Reads available scalability, reliability and churn JSON-lines logs and
writes CSV tables, SVG figures and summary.md into the same directory.
Standard library only (Python 3.8+), so the figures can be regenerated anywhere.

    python3 scripts/analyze.py experiments/results

Log format (one JSON object per line). The lookup events are written by the
nodes themselves (slog JSON handler, see kademlia/lookup.go):
  lookup_probe     one FIND_NODE / FIND_VALUE RPC sent by a lookup:
                   node_id, lookup_id, rpc, target, alpha, round, peer_id
  lookup_complete  outcome of one lookup: node_id, lookup_id, rpc, success,
                   probes, rounds, local, found_target, error
The experiment adds labels:
  run              starts a run: exp, n, seed, k, alpha (+ variant, retries, loss)
  node_result      after a node lookup: target, closest (ground truth), best (found)

A "probe" is one RPC issued by the lookup algorithm; retransmissions of the same
RPC by the transport are not counted again. A "round" is one batch of up to
alpha probes sent together, so rounds approximate the time a lookup needs.
Lookups are averaged per run (one network, one seed); the reported spread is
the standard deviation of those per-run means across seeds.
"""
import csv
import json
import math
import os
import statistics
import sys
from collections import defaultdict


def read_runs(path):
    """Yield (run_record, [other records]) groups from a JSON-lines log."""
    run, records = None, []
    with open(path) as handle:
        for line in handle:
            record = json.loads(line)
            record["ev"] = record.get("ev") or record.get("event")
            if record["ev"] == "run":
                if run is not None:
                    yield run, records
                run, records = record, []
            else:
                records.append(record)
    if run is not None:
        yield run, records


def mean_std(values):
    values = list(values)
    if not values:
        return float("nan"), float("nan")
    return statistics.fmean(values), (statistics.stdev(values) if len(values) > 1 else 0.0)


# ---------------------------------------------------------------------------
# Minimal SVG line chart

PALETTE = ["#1f77b4", "#d62728", "#2ca02c", "#9467bd", "#ff7f0e", "#17becf"]


def nice_ticks(lo, hi, count=5):
    """Round tick positions covering [lo, hi]."""
    raw = (hi - lo) / count
    magnitude = 10 ** math.floor(math.log10(raw))
    step = next(m * magnitude for m in (1, 2, 2.5, 5, 10) if raw <= m * magnitude)
    ticks, value = [], math.floor(lo / step) * step
    while True:
        ticks.append(round(value, 10))
        if value >= hi - 1e-9:  # the last tick must cover the data
            return ticks
        value += step


def svg_chart(path, title, xlabel, ylabel, series, xlog=False, ymin=None, ymax=None, xticks=None, note=None,
              legend="upper"):
    """series: list of dicts(label, points=[(x, mean, std)], style='line'|'dashed', color)."""
    width, height = 720, 460
    left, right, top, bottom = 70, 20, 50, 70
    xs = [p[0] for s in series for p in s["points"]]
    tx = (lambda v: math.log2(v)) if xlog else (lambda v: v)
    x0, x1 = tx(min(xs)), tx(max(xs))
    ys = [p[1] + p[2] for s in series for p in s["points"]] + [p[1] - p[2] for s in series for p in s["points"]]
    lo = ymin if ymin is not None else min(0.0, min(ys))
    hi = ymax if ymax is not None else max(ys) * 1.03
    yticks = nice_ticks(lo, hi)
    y0 = lo if ymin is not None else yticks[0]
    y1 = hi if ymax is not None else yticks[-1]
    yticks = [v for v in yticks if y0 - 1e-9 <= v <= y1 + 1e-9]
    if x1 == x0:
        x1 = x0 + 1

    def px(v):
        return left + (tx(v) - x0) / (x1 - x0) * (width - left - right)

    def py(v):
        return top + (1 - (v - y0) / (y1 - y0)) * (height - top - bottom)

    out = [
        f'<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 {width} {height}" font-family="sans-serif" font-size="12">',
        f'<rect width="{width}" height="{height}" fill="white"/>',
        f'<text x="{width/2}" y="24" text-anchor="middle" font-size="15" font-weight="bold">{title}</text>',
    ]
    for v in yticks:
        y = py(v)
        out.append(f'<line x1="{left}" y1="{y:.1f}" x2="{width-right}" y2="{y:.1f}" stroke="#e0e0e0"/>')
        out.append(f'<text x="{left-8}" y="{y+4:.1f}" text-anchor="end">{v:.3g}</text>')
    for v in (xticks if xticks else sorted(set(xs))):
        x = px(v)
        out.append(f'<line x1="{x:.1f}" y1="{top}" x2="{x:.1f}" y2="{height-bottom}" stroke="#f0f0f0"/>')
        out.append(f'<text x="{x:.1f}" y="{height-bottom+16}" text-anchor="middle">{v:g}</text>')
    out.append(f'<rect x="{left}" y="{top}" width="{width-left-right}" height="{height-top-bottom}" fill="none" stroke="#444"/>')
    out.append(f'<text x="{(left+width-right)/2}" y="{height-bottom+40}" text-anchor="middle">{xlabel}</text>')
    out.append(f'<text transform="translate(18 {(top+height-bottom)/2}) rotate(-90)" text-anchor="middle">{ylabel}</text>')
    for idx, s in enumerate(series):
        color = s.get("color", PALETTE[idx % len(PALETTE)])
        dash = ' stroke-dasharray="6 4"' if s.get("style") == "dashed" else ""
        pts = " ".join(f"{px(x):.1f},{py(m):.1f}" for x, m, _ in s["points"])
        out.append(f'<polyline points="{pts}" fill="none" stroke="{color}" stroke-width="2"{dash}/>')
        if s.get("style") != "dashed":
            for x, m, sd in s["points"]:
                out.append(f'<line x1="{px(x):.1f}" y1="{py(m-sd):.1f}" x2="{px(x):.1f}" y2="{py(m+sd):.1f}" stroke="{color}"/>')
                out.append(f'<circle cx="{px(x):.1f}" cy="{py(m):.1f}" r="3.5" fill="{color}"/>')
    ly = top + 14 if legend == "upper" else height - bottom - 17 * len(series) + 8
    for idx, s in enumerate(series):
        color = s.get("color", PALETTE[idx % len(PALETTE)])
        dash = ' stroke-dasharray="6 4"' if s.get("style") == "dashed" else ""
        out.append(f'<line x1="{left+12}" y1="{ly-4}" x2="{left+40}" y2="{ly-4}" stroke="{color}" stroke-width="2"{dash}/>')
        out.append(f'<text x="{left+46}" y="{ly}">{s["label"]}</text>')
        ly += 17
    if note:
        out.append(f'<text x="{left}" y="{height-8}" font-size="11" fill="#555">{note}</text>')
    out.append("</svg>")
    with open(path, "w") as handle:
        handle.write("\n".join(out))


def write_csv(path, rows):
    with open(path, "w", newline="") as handle:
        writer = csv.DictWriter(handle, fieldnames=list(rows[0].keys()))
        writer.writeheader()
        writer.writerows(rows)


# ---------------------------------------------------------------------------
# Experiment 1: probes vs N


def affine_fit(xs, ys):
    """Slope a, intercept b of y = a*x + b, and R^2."""
    n = len(xs)
    mx, my = sum(xs) / n, sum(ys) / n
    a = sum((x - mx) * (y - my) for x, y in zip(xs, ys)) / sum((x - mx) ** 2 for x in xs)
    b = my - a * mx
    ss_res = sum((y - (a * x + b)) ** 2 for x, y in zip(xs, ys))
    ss_tot = sum((y - my) ** 2 for y in ys)
    return a, b, (1 - ss_res / ss_tot) if ss_tot else 1.0


def scalability(directory, summary):
    path = os.path.join(directory, "scalability.jsonl")
    if not os.path.exists(path):
        return
    kind_of = {"FIND_NODE": "node", "FIND_VALUE": "value"}
    probes_run = defaultdict(lambda: defaultdict(list))  # kind -> n -> per-run mean probes
    rounds_run = defaultdict(lambda: defaultdict(list))  # kind -> n -> per-run mean rounds
    accuracy = defaultdict(list)  # n -> per-run fraction of node lookups that found the true closest node
    lookups = defaultdict(int)
    failures = defaultdict(int)
    k = alpha = None
    for run, records in read_runs(path):
        n, k, alpha = run["n"], run["k"], run["alpha"]
        probes, rounds, hits = defaultdict(list), defaultdict(list), []
        for r in records:
            if r["ev"] == "lookup_complete":
                kind = kind_of[r["rpc"]]
                probes[kind].append(r["probes"])
                rounds[kind].append(r["rounds"])
                lookups[(kind, n)] += 1
                if not r["success"]:
                    failures[(kind, n)] += 1
            elif r["ev"] == "node_result":
                hits.append(1.0 if r["best"] == r["closest"] else 0.0)
        for kind in probes:
            probes_run[kind][n].append(statistics.fmean(probes[kind]))
            rounds_run[kind][n].append(statistics.fmean(rounds[kind]))
        if hits:
            accuracy[n].append(statistics.fmean(hits))

    sizes = sorted(probes_run["node"])
    rows = []
    for kind in ("node", "value"):
        for n in sizes:
            m, s_ = mean_std(probes_run[kind][n])
            rm, rs = mean_std(rounds_run[kind][n])
            rows.append({"kind": kind, "n": n, "log2n": round(math.log2(n), 3), "mean_probes": round(m, 3),
                         "std_across_seeds": round(s_, 3), "mean_rounds": round(rm, 3), "std_rounds": round(rs, 3),
                         "seeds": len(probes_run[kind][n]), "lookups": lookups[(kind, n)],
                         "failed_lookups": failures[(kind, n)]})
    write_csv(os.path.join(directory, "scalability.csv"), rows)

    series, fits = [], {}
    labels = {"node": "node lookup (FIND_NODE)", "value": "value lookup (FIND_VALUE)"}
    for idx, kind in enumerate(("node", "value")):
        pts = [(r["n"], r["mean_probes"], r["std_across_seeds"]) for r in rows if r["kind"] == kind]
        a, b, r2 = affine_fit([math.log2(p[0]) for p in pts], [p[1] for p in pts])
        fits[kind] = (a, b, r2)
        series.append({"label": labels[kind] + " - measured", "points": pts, "color": PALETTE[idx]})
        series.append({"label": f"{kind}: fit {a:.2f}*log2(N) + {b:.2f}", "style": "dashed", "color": PALETTE[idx],
                       "points": [(p[0], a * math.log2(p[0]) + b, 0) for p in pts]})
    svg_chart(os.path.join(directory, "scalability.svg"), f"Lookup cost vs network size (k={k}, alpha={alpha})",
              "network size N (log scale)", "probes (RPCs) per lookup", series, xlog=True, ymin=0, xticks=sizes,
              note="points: mean over seeds, bars: +/- 1 std across seeds; dashed: least-squares fit a*log2(N)+b")
    round_series = [{"label": f"{kind} lookup", "color": PALETTE[idx],
                     "points": [(r["n"], r["mean_rounds"], r["std_rounds"]) for r in rows if r["kind"] == kind]}
                    for idx, kind in enumerate(("node", "value"))]
    svg_chart(os.path.join(directory, "scalability_rounds.svg"), f"Lookup rounds vs network size (k={k}, alpha={alpha})",
              "network size N (log scale)", "rounds per lookup (sequential steps)", round_series, xlog=True, ymin=0,
              xticks=sizes, note="a round = alpha probes sent together; rounds approximate the time a lookup needs")

    node = {r["n"]: r for r in rows if r["kind"] == "node"}
    value = {r["n"]: r for r in rows if r["kind"] == "value"}
    seeds = sorted({r["seeds"] for r in rows})
    summary.append("## Experiment 1 - lookup probes vs network size\n")
    summary.append(f"k = {k}, alpha = {alpha}, {', '.join(map(str, seeds))} seeds per network size. "
                   "Figures: `scalability.svg`, `scalability_rounds.svg`; data: `scalability.csv`.\n")
    summary.append("| N | log2 N | node lookup probes (mean +/- std) | node lookup rounds | value lookup probes (mean +/- std) | value lookup rounds | node lookup found true closest node |")
    summary.append("|---|---|---|---|---|---|---|")
    for n in sizes:
        summary.append(f"| {n} | {math.log2(n):.0f} | {node[n]['mean_probes']:.1f} +/- {node[n]['std_across_seeds']:.1f} | "
                       f"{node[n]['mean_rounds']:.2f} | "
                       f"{value[n]['mean_probes']:.1f} +/- {value[n]['std_across_seeds']:.1f} | "
                       f"{value[n]['mean_rounds']:.2f} | {100 * statistics.fmean(accuracy[n]):.1f}% |")
    summary.append("")
    for kind, (a, b, r2) in fits.items():
        summary.append(f"Least-squares fit ({kind} lookups): probes = {a:.2f} * log2(N) + {b:.2f}  (R^2 = {r2:.3f}).")
    summary.append("")
    summary.append(f"Failed lookups (no loss configured): {sum(failures.values())} of {sum(lookups.values())}.\n")


# ---------------------------------------------------------------------------
# Experiment 2: success rate vs packet loss


def reliability(directory, summary):
    path = os.path.join(directory, "reliability.jsonl")
    if not os.path.exists(path):
        return
    rates = defaultdict(list)  # (variant, loss) -> per-run success rate
    probes = defaultdict(list)  # (variant, loss) -> per-run mean probes
    rounds = defaultdict(list)  # (variant, loss) -> per-run mean rounds
    params = {}  # variant -> (alpha, retries)
    n = k = None
    for run, records in read_runs(path):
        key = (run["variant"], run["loss"])
        params[run["variant"]] = (run["alpha"], run["retries"])
        n, k = run["n"], run["k"]
        lookups = [r for r in records if r["ev"] == "lookup_complete"]
        rates[key].append(statistics.fmean(1.0 if r["success"] else 0.0 for r in lookups))
        probes[key].append(statistics.fmean(r["probes"] for r in lookups))
        rounds[key].append(statistics.fmean(r["rounds"] for r in lookups))

    variants = sorted(params, key=lambda v: (-params[v][1], -params[v][0]))
    label = {v: f"alpha={params[v][0]}, retries={params[v][1]}" for v in variants}
    losses = sorted({loss for _, loss in rates})
    rows = []
    for v in variants:
        for loss in losses:
            m, s_ = mean_std(rates[(v, loss)])
            rows.append({"variant": v, "alpha": params[v][0], "retries": params[v][1], "loss": loss,
                         "success_rate": round(m, 4), "std_across_seeds": round(s_, 4), "seeds": len(rates[(v, loss)]),
                         "mean_probes": round(statistics.fmean(probes[(v, loss)]), 2),
                         "mean_rounds": round(statistics.fmean(rounds[(v, loss)]), 2)})
    write_csv(os.path.join(directory, "reliability.csv"), rows)

    series = [{"label": label[v], "color": PALETTE[idx],
               "points": [(100 * l, 100 * statistics.fmean(rates[(v, l)]), 100 * mean_std(rates[(v, l)])[1]) for l in losses]}
              for idx, v in enumerate(variants)]
    # Expectation: one RPC attempt survives with (1-p)^2 (request and response),
    # so with R retries an RPC succeeds with q = 1-(1-(1-p)^2)^(R+1). A value held
    # by k replicas is lost only if every replica probe fails: 1-(1-q)^k.
    for idx, retries in enumerate(sorted({params[v][1] for v in variants}, reverse=True)):
        pts = []
        for l in losses:
            q = 1 - (1 - (1 - l) ** 2) ** (retries + 1)
            pts.append((100 * l, 100 * (1 - (1 - q) ** k), 0))
        series.append({"label": f"model, retries={retries}: 1-(1-q)^k", "style": "dashed",
                       "color": PALETTE[(len(variants) + idx) % len(PALETTE)], "points": pts})
    svg_chart(os.path.join(directory, "reliability.svg"), f"Value lookup success vs packet loss (N={n}, k={k})",
              "packet loss probability per packet (%)", "lookups that found the value (%) - axis starts at 70%",
              series, ymin=70, ymax=100, legend="lower",
              note="points: mean over seeds, bars: +/- 1 std across seeds; dashed: replica-failure model")

    summary.append("## Experiment 2 - lookup success rate vs packet loss\n")
    summary.append(f"N = {n}, k = {k}. Figure: `reliability.svg`, data: `reliability.csv`. "
                   "Success = the value was found and passed the hash check. "
                   "Cell = mean success rate +/- std across seeds.\n")
    header = "| loss | " + " | ".join(label[v] for v in variants) + " |"
    summary.append(header)
    summary.append("|---|" + "---|" * len(variants))
    for loss in losses:
        cells = []
        for v in variants:
            m, s_ = mean_std(rates[(v, loss)])
            cells.append(f"{100 * m:.1f}% +/- {100 * s_:.1f}")
        summary.append(f"| {100 * loss:.0f}% | " + " | ".join(cells) + " |")
    summary.append("")
    summary.append("Mean probes per lookup (messages) and mean rounds per lookup (latency proxy):\n")
    summary.append(header)
    summary.append("|---|" + "---|" * len(variants))
    for loss in losses:
        summary.append(f"| {100 * loss:.0f}% | " + " | ".join(
            f"{statistics.fmean(probes[(v, loss)]):.1f} probes / {statistics.fmean(rounds[(v, loss)]):.1f} rounds"
            for v in variants) + " |")
    summary.append("")


def churn(directory, summary):
    """Keep replica loss separate from failure to route to a surviving copy."""
    path = os.path.join(directory, "churn.jsonl")
    if not os.path.exists(path):
        return
    runs, grouped = [], defaultdict(list)
    configs = set()
    for run, records in read_runs(path):
        configs.add((run["n"], run["k"], run["alpha"], run["node_lookups"], run["values"]))
        node = [r for r in records if r["ev"] == "churn_lookup" and r["kind"] == "node"]
        value = [r for r in records if r["ev"] == "churn_lookup" and r["kind"] == "value"]
        replicas = [r for r in records if r["ev"] == "churn_replicas"]
        available = [r for r in value if r["surviving_replicas"] > 0]
        retained = [r for r in replicas if r["after_crash"] > 0]
        repair = next(r for r in records if r["ev"] == "churn_repair")
        row = {"n": run["n"], "seed": run["seed"], "crash_fraction": run["crash_fraction"],
               "alive": run["alive"], "join_failures": run["join_failures"],
               "node_lookups": len(node), "node_exact": sum(r["exact_closest"] for r in node),
               "value_lookups": len(value), "values_available": len(available),
               "values_found": sum(r["success"] for r in value),
               "routing_misses": sum(not r["success"] for r in available), "repair_errors": repair["errors"]}
        for kind, lookups in (("node", node), ("value", value)):
            for metric in ("probes", "rounds", "failed_probes"):
                row[f"{kind}_{metric}"] = statistics.fmean(r[metric] for r in lookups)
        for stage in ("before_crash", "after_crash", "before_repair", "after_repair"):
            row[f"replicas_{stage}"] = statistics.fmean(r[stage] for r in replicas)
        row["retained_replicas_after_repair"] = statistics.fmean(r["after_repair"] for r in retained) if retained else 0
        row["closest_live_after_repair"] = statistics.fmean(r["closest_live_after_repair"] for r in retained) if retained else 0
        row["keys_restored_to_k_closest"] = sum(r["closest_live_after_repair"] == run["k"] for r in retained)
        runs.append(row)
        grouped[run["crash_fraction"]].append(row)
    if not runs:
        raise ValueError("churn log contains no runs")
    if len(configs) != 1:
        raise ValueError("churn analysis requires one N/k/alpha/workload configuration per directory")
    n, k, alpha, node_per_seed, values_per_seed = configs.pop()
    write_csv(os.path.join(directory, "churn_runs.csv"), runs)

    rows = []
    for fraction, seeds in sorted(grouped.items()):
        row = {"crash_fraction": fraction, "alive": seeds[0]["alive"], "seeds": len(seeds)}
        for key in ("node_lookups", "node_exact", "value_lookups", "values_available", "values_found", "routing_misses",
                    "join_failures", "repair_errors", "keys_restored_to_k_closest"):
            row[key] = sum(r[key] for r in seeds)
        row["node_exact_rate"] = row["node_exact"] / row["node_lookups"]
        row["value_success_rate"] = row["values_found"] / row["value_lookups"]
        row["value_available_rate"] = row["values_available"] / row["value_lookups"]
        row["available_value_success_rate"] = ((row["values_available"] - row["routing_misses"]) / row["values_available"]
                                               if row["values_available"] else 0)
        for key in ("node_probes", "node_rounds", "node_failed_probes", "value_probes", "value_rounds", "value_failed_probes",
                    "replicas_before_crash", "replicas_after_crash", "replicas_before_repair", "replicas_after_repair",
                    "retained_replicas_after_repair", "closest_live_after_repair"):
            mean, std = mean_std(r[key] for r in seeds)
            row[key], row[key + "_std"] = round(mean, 4), round(std, 4)
        rows.append(row)
    write_csv(os.path.join(directory, "churn.csv"), rows)

    def points(metric, scale=1):
        return [(100 * r["crash_fraction"], scale * r[metric], scale * r.get(metric + "_std", 0)) for r in rows]

    svg_chart(os.path.join(directory, "churn_cost.svg"), f"Lookup cost after abrupt node departures (initial N={n})",
              "initial nodes crashed (%)", "logical RPC probes per lookup", [
                  {"label": "FIND_NODE: all probes", "points": points("node_probes")},
                  {"label": "FIND_NODE: failed probes", "points": points("node_failed_probes")},
                  {"label": "FIND_VALUE: all probes", "points": points("value_probes")},
                  {"label": "FIND_VALUE: failed probes", "points": points("value_failed_probes")},
              ], ymin=0, note="Means over seeds; bars: +/- 1 seed standard deviation. No simulated timeout delay.")
    svg_chart(os.path.join(directory, "churn_availability.svg"), "Routing and value availability after abrupt departures",
              "initial nodes crashed (%)", "success or availability (%)", [
                  {"label": "FIND_NODE finds globally closest live peer", "points": points("node_exact_rate", 100)},
                  {"label": "values with an original live replica", "points": points("value_available_rate", 100)},
                  {"label": "FIND_VALUE success (all values)", "points": points("value_success_rate", 100)},
                  {"label": "FIND_VALUE success given a live replica", "points": points("available_value_success_rate", 100)},
              ], ymin=0, ymax=100, legend="lower",
              note=f"Each seed: {node_per_seed} node and {values_per_seed} distinct value lookups; counts in churn.csv.")
    svg_chart(os.path.join(directory, "churn_replicas.svg"), "Original replicas lost and surviving values repaired",
              "initial nodes crashed (%)", "mean live copies per value", [
                  {"label": "before crash: all values", "points": points("replicas_before_crash")},
                  {"label": "immediately after crash: all values", "points": points("replicas_after_crash")},
                  {"label": "after repair: surviving values only", "points": points("retained_replicas_after_repair")},
                  {"label": "at actual closest k after repair: surviving values", "points": points("closest_live_after_repair")},
              ], ymin=0, legend="lower",
              note="Reads add caches before manual repair; total copies can exceed k. Lost values cannot be repaired.")

    seed_counts = ", ".join(str(count) for count in sorted({r["seeds"] for r in rows}))
    seed_word = "seed" if seed_counts == "1" else "seeds"
    summary.append(f"## Experiment 3 - abrupt departures from {n} nodes\n")
    summary.append(f"k = {k}, alpha = {alpha}; {seed_counts} {seed_word} per fraction, {node_per_seed} node lookups and "
                   f"{values_per_seed} distinct value lookups per seed. "
                   "Healthy join/store precedes random abrupt removals. There are no arrivals, packet loss, "
                   "real timeout delays or periodic maintenance during measurement. Each value reader is a live non-holder. "
                   "This is a crash-fraction sweep, not a measured continuous churn rate.\n")
    summary.append("| crashed | alive | closest live node found | value found | value has live copy | routing misses with live copy |")
    summary.append("|---|---:|---:|---:|---:|---:|")
    for r in rows:
        summary.append(f"| {100*r['crash_fraction']:.0f}% | {r['alive']} | {r['node_exact']}/{r['node_lookups']} | "
                       f"{r['values_found']}/{r['value_lookups']} | {r['values_available']}/{r['value_lookups']} | {r['routing_misses']} |")
    summary.append("\nLookup cost includes successes and failures; mean +/- standard deviation of per-seed means.\n")
    summary.append("| crashed | node probes | failed node probes | node rounds | value probes | failed value probes | value rounds |")
    summary.append("|---|---:|---:|---:|---:|---:|---:|")
    for r in rows:
        cells = [f"{r[key]:.2f} +/- {r[key + '_std']:.2f}" for key in
                 ("node_probes", "node_failed_probes", "node_rounds", "value_probes", "value_failed_probes", "value_rounds")]
        summary.append(f"| {100*r['crash_fraction']:.0f}% | " + " | ".join(cells) + " |")
    summary.append("\nRepair sources are selected from original surviving holders before reads, one per retained key, "
                   "deduplicated by node. Each selected node calls Republish once after the lookup workload. "
                   "Reads cache immutable values, so post-repair copy counts include caches and can exceed k. "
                   "Placement is checked against the globally closest live k, independently of the routing tables.\n")
    summary.append("| crashed | copies immediately after crash (all keys) | copies after repair (retained keys) | copies at true closest k after repair (retained keys) | retained keys restored at all k closest |")
    summary.append("|---|---:|---:|---:|---:|")
    for r in rows:
        summary.append(f"| {100*r['crash_fraction']:.0f}% | {r['replicas_after_crash']:.2f} | "
                       f"{r['retained_replicas_after_repair']:.2f} | {r['closest_live_after_repair']:.2f} | "
                       f"{r['keys_restored_to_k_closest']}/{r['values_available']} |")
    summary.append(f"\nJoin failures: {sum(r['join_failures'] for r in rows)}; manual repair errors: {sum(r['repair_errors'] for r in rows)}. "
                   "Data: `churn.jsonl`, `churn_runs.csv`, `churn.csv`. Figures: `churn_cost.svg`, "
                   "`churn_availability.svg`, `churn_replicas.svg`.\n")


def main():
    directory = sys.argv[1] if len(sys.argv) > 1 else "experiments/results"
    summary = ["# Experiment results (generated by scripts/analyze.py)\n"]
    scalability(directory, summary)
    reliability(directory, summary)
    churn(directory, summary)
    with open(os.path.join(directory, "summary.md"), "w") as handle:
        handle.write("\n".join(summary) + "\n")
    print("\n".join(summary))


if __name__ == "__main__":
    main()
