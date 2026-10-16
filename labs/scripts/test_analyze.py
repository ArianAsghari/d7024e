"""Small synthetic check for the churn analysis's availability distinction."""
import csv
import importlib.util
import json
from pathlib import Path
import tempfile
import unittest


spec = importlib.util.spec_from_file_location("analyze", Path(__file__).with_name("analyze.py"))
analyze = importlib.util.module_from_spec(spec)
spec.loader.exec_module(analyze)


class ChurnAnalysisTest(unittest.TestCase):
    def test_counts_lost_values_separately_from_routing_misses_and_uses_run_labels(self):
        records = []
        for seed in (1, 2):
            records.append({"ev": "run", "n": 40, "k": 1, "alpha": 2, "seed": seed,
                            "crash_fraction": 0.5, "alive": 20, "join_failures": 0,
                            "node_lookups": 2, "values": 2})
            for exact in (True, seed == 2):
                records.append({"ev": "churn_lookup", "kind": "node", "exact_closest": exact,
                                "probes": 4, "rounds": 2, "failed_probes": 1})
            # Seed 1 loses all replicas of one key; seed 2 cannot route to
            # one key even though it still has a replica. Both failures must
            # lower overall success, but only the latter is a routing miss.
            for available, success in ((1, True), (seed - 1, False)):
                records.append({"ev": "churn_lookup", "kind": "value", "success": success,
                                "surviving_replicas": available, "probes": 3, "rounds": 1, "failed_probes": 1})
                records.append({"ev": "churn_replicas", "before_crash": 1, "after_crash": available,
                                "before_repair": available, "after_repair": available,
                                "closest_live_after_repair": available})
            records.append({"ev": "churn_repair", "errors": 0})
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory)
            (path / "churn.jsonl").write_text("\n".join(json.dumps(r) for r in records) + "\n")
            summary = []
            analyze.churn(directory, summary)
            with (path / "churn.csv").open() as handle:
                row = next(csv.DictReader(handle))
            self.assertEqual(row["values_available"], "3")
            self.assertEqual(row["values_found"], "2")
            self.assertEqual(row["routing_misses"], "1")
            self.assertEqual(float(row["value_success_rate"]), 0.5)
            self.assertAlmostEqual(float(row["available_value_success_rate"]), 2 / 3)
            self.assertEqual(row["node_exact"], "3")
            text = "\n".join(summary)
            self.assertIn("from 40 nodes", text)
            self.assertIn("k = 1, alpha = 2; 2 seeds", text)
            self.assertIn("2 node lookups and 2 distinct value lookups", text)
            self.assertIn("initial N=40", (path / "churn_cost.svg").read_text())


if __name__ == "__main__":
    unittest.main()
