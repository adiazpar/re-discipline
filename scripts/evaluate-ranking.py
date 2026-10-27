"""Compare two real engine builds against a prewritten, private question set.

JSONL rows: {"q": "question", "expect": "docs/answer.md"}. An expect list
accepts multiple independently judged answers. Never add alternatives after
seeing the results to improve the score. Symbol rows are measured by `bench`.
No model, network provider, corpus editing, or dependency installation is needed.
"""
import argparse
import hashlib
import json
from pathlib import Path
import statistics
import subprocess
import time


def percentile(values, fraction):
    return sorted(values)[int((len(values) - 1) * fraction)]


def measure(binary, root, case, sources):
    start = time.perf_counter()
    result = subprocess.run(
        [str(binary), "query", "--root", str(root), "--sources", sources,
         "--baseline", "--offline", "--diagnostics", "--limit", "8", "--json", case["q"]],
        capture_output=True, text=True, timeout=120, check=True)
    elapsed = (time.perf_counter() - start) * 1000
    payload = json.loads(result.stdout)
    if result.stderr.strip() or payload.get("warnings"):
        raise RuntimeError("query returned warnings: " + result.stderr + str(payload.get("warnings")))
    if payload.get("assistance", {}).get("evaluated", 0):
        raise RuntimeError("model assistance must stay disabled")
    hits = payload.get("hits", [])
    expected = case["expect"]
    expected = [expected] if isinstance(expected, str) else expected
    paths = [h.get("path") for h in hits]
    rank = next((i + 1 for i, p in enumerate(paths) if p in expected), 0)
    identities = [h["finding_id"] for h in hits if h.get("finding_id")]
    return {"rank": rank, "paths": paths, "ms": round(elapsed, 3),
            "duplicate_ids": len(identities) - len(set(identities))}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--root", type=Path, required=True)
    parser.add_argument("--questions", type=Path, required=True)
    parser.add_argument("--baseline", type=Path, required=True)
    parser.add_argument("--candidate", type=Path, required=True)
    parser.add_argument("--output", type=Path, required=True,
                        help="Private output path: includes questions and document paths")
    parser.add_argument("--sources", choices=["local"], default="local", help="Local relevance comparison; server load is measured separately")
    parser.add_argument("--rounds", type=int, default=2)
    args = parser.parse_args()
    if args.rounds < 1:
        parser.error("--rounds must be positive")
    raw = args.questions.read_bytes()
    cases = [json.loads(line) for line in raw.splitlines() if line.strip()]
    cases = [c for c in cases if "q" in c]
    if not cases:
        parser.error("question set has no text queries")
    for c in cases:
        if not isinstance(c.get("q"), str) or not c["q"].strip() or not c.get("expect"):
            parser.error("each question needs a nonempty q and expect")
        if not isinstance(c["expect"], str) and not (
                isinstance(c["expect"], list) and all(isinstance(p, str) for p in c["expect"])):
            parser.error("expect must be a path or list of paths")
    binaries = {"baseline": args.baseline.resolve(), "candidate": args.candidate.resolve()}
    report = {"questions_sha256": hashlib.sha256(raw).hexdigest(),
              "sources": args.sources, "rounds": args.rounds, "binaries": {}, "cases": []}
    for name, binary in binaries.items():
        report["binaries"][name] = {"path": str(binary),
            "sha256": hashlib.sha256(binary.read_bytes()).hexdigest()}
        # Warm the index before measuring; both builds must support its format.
        subprocess.run([str(binary), "stats", "--root", str(args.root.resolve()), "--json"],
                       check=True, capture_output=True, timeout=120)
    for i, case in enumerate(cases):
        row = dict(case, baseline=[], candidate=[])
        for round_index in range(args.rounds):
            # Alternate order to reduce cache/order bias. No parallel queries.
            names = list(binaries)
            if (i + round_index) % 2:
                names.reverse()
            for name in names:
                row[name].append(measure(binaries[name], args.root.resolve(), case, args.sources))
        report["cases"].append(row)
        if (i + 1) % 40 == 0:
            print(f"Compared {i+1}/{len(cases)} questions", flush=True)
    report["summary"] = {}
    for name in binaries:
        ranks = [r[name][0]["rank"] for r in report["cases"]]
        times = [run["ms"] for r in report["cases"] for run in r[name]]
        report["summary"][name] = {
            "questions": len(cases), "top1": ranks.count(1),
            "top5": sum(0 < r <= 5 for r in ranks), "top8": sum(0 < r <= 8 for r in ranks),
            "mrr_at_8": statistics.mean(1 / r if r else 0 for r in ranks),
            "p50_ms": percentile(times, .5), "p95_ms": percentile(times, .95),
            "unstable_questions": sum(len({tuple(run["paths"]) for run in r[name]}) > 1
                                      for r in report["cases"]),
            "duplicate_ids": sum(run["duplicate_ids"] for r in report["cases"] for run in r[name])}
    args.output.parent.mkdir(parents=True, exist_ok=True)
    args.output.write_text(json.dumps(report, indent=2) + "\n")
    print(json.dumps(report["summary"], indent=2))


if __name__ == "__main__":
    main()
