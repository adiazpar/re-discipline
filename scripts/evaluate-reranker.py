"""Offline, developer-only retrieval experiment; never used by the plugin.

Install the research dependencies in a disposable environment, download a
revision-pinned model separately, and pass its local directory. Corpus text
and questions never go to an inference service. Freeze questions, judgments
and parameters before using this on a held-out set. Reports contain private
queries and paths and must not be committed to a public repository.
An optional embedding model adds semantic candidates from this one local index.
That mode performs an exhaustive vector scan, not a production vector service.
"""
import argparse
import hashlib
import json
import math
import os
from pathlib import Path
import re
import sqlite3
import statistics
import subprocess
import time


def rank(rows, scores, weight, k):
    neural = sorted(range(len(rows)), key=lambda i: -scores[i])
    positions = {i: j + 1 for j, i in enumerate(neural)}
    return sorted(range(len(rows)), key=lambda i: (
        rows[i].get("status") == "superseded", not rows[i]["exact"],
        -(1 / (k + i + 1) + weight / (k + positions[i]))))


def normalize_scores(values):
    if not values:
        return []
    if not all(isinstance(v, (int, float)) and math.isfinite(v) for v in values):
        raise ValueError("Fusion requires finite scalar scores")
    low, high = min(values), max(values)
    if high == low:
        return [0.0] * len(values)
    return [(v - low) / (high - low) for v in values]


def head_fusion(rows, order, lexical, semantic, neural):
    """Frozen research candidate: reorder the first five, preserve their set.

    Normalize over the entire original candidate pool. Constant channels add
    zero; these are relative ranking features, not calibrated confidence.
    Weights were selected on development data, not on the evaluation file.
    """
    size = len(rows)
    if (sorted(order) != list(range(size)) or
            any(len(v) != size for v in (lexical, semantic, neural))):
        raise ValueError("Fusion features and permutation must match the candidate pool")
    channels = [normalize_scores(v) for v in (lexical, semantic, neural)]
    scores = [.25 * l + .25 * d + .5 * n for l, d, n in zip(*channels)]
    head = sorted(order[:5], key=lambda i: (
        rows[i].get("status") == "superseded", not rows[i]["exact"], -scores[i]))
    return head + order[5:]


def lexical_features(lexical_paths, explained, candidate_paths):
    # CLI query expands its internal pool before calling the engine. Explain
    # must use that same pool; otherwise scores can describe a different run.
    if explained is None:
        explained = []  # Native explain encodes its empty slice as JSON null.
    if [r["path"] for r in explained[:len(lexical_paths)]] != lexical_paths:
        raise RuntimeError("Explain order differs from query; cannot align lexical features")
    values = {}
    for row in explained[:len(lexical_paths)]:
        value = row["final"]
        if not isinstance(value, (int, float)) or not math.isfinite(value):
            raise RuntimeError("Explain returned an invalid lexical score")
        # Native FTS scores are lower-is-better. Missing lexical candidates
        # and nonnegative native scores supply zero lexical evidence.
        values[row["path"]] = max(0.0, -value)
    return [values.get(path, 0.0) for path in candidate_paths]


def answer_rank(paths, expected):
    return next((i + 1 for i, p in enumerate(paths) if p in expected), 0)


def metrics(ranks):
    return {"questions": len(ranks), "top1": ranks.count(1),
            "top5": sum(0 < r <= 5 for r in ranks),
            "top8": sum(0 < r <= 8 for r in ranks),
            "mrr_at_8": statistics.mean(1 / r if 0 < r <= 8 else 0 for r in ranks)}


def projection(title, body):
    # The index already excludes relation bullets. Avoid duplicate headings
    # and local evidence-path sections in the text supplied to the model.
    body = re.sub(r"^\s*# [^\n]*\n", "", body, count=1).strip()
    body = re.sub(r"(?ims)^## (?:Evidence|Local evidence|Supporting evidence)\s*\n.*?(?=^## |\Z)", "", body).strip()
    return title + "\n" + body


def sha256(path):
    digest = hashlib.sha256()
    with path.open("rb") as stream:
        for block in iter(lambda: stream.read(1024 * 1024), b""):
            digest.update(block)
    return digest.hexdigest()


def model_hashes(directory):
    return {str(f.relative_to(directory)): sha256(f) for f in sorted(directory.rglob("*"))
            if f.is_file() and f.suffix in {".safetensors", ".onnx", ".json", ".txt", ".model", ".data", ".onnx_data"}}


def score_pairs(model, pairs, batch_size, sort_by_length=False):
    """Reduce padding without changing text or assigning scores to other rows."""
    if not pairs:
        return []
    order = list(range(len(pairs)))
    if sort_by_length:
        tokens = model.tokenizer(pairs, padding=False, truncation=True)["input_ids"]
        if len(tokens) != len(pairs):
            raise RuntimeError("Tokenizer returned a different number of pairs")
        order.sort(key=lambda i: -len(tokens[i]))
    values = model.predict([pairs[i] for i in order], batch_size=batch_size,
                           show_progress_bar=False).tolist()
    if len(values) != len(pairs) or not all(isinstance(s, (int, float)) and math.isfinite(s) for s in values):
        raise RuntimeError("Reranker returned invalid scores")
    scores = [0.0] * len(pairs)
    for original_index, value in zip(order, values):
        scores[original_index] = value
    return scores


class SemanticCandidates:
    """Disposable in-memory prototype. No network, persistent index or links."""

    def __init__(self, args, db):
        import numpy as np
        from sentence_transformers import SentenceTransformer
        self.np, self.args = np, args
        self.model = SentenceTransformer(str(args.embedding_model.resolve()), device=args.device,
                                        local_files_only=True, trust_remote_code=False,
                                        model_kwargs={"use_safetensors": True})
        self.model.max_seq_length = args.embedding_max_length
        records = db.execute("SELECT docs.path,docs.title,body,docs.status,docmeta.idents,"
                             "docmeta.finding_id FROM docs LEFT JOIN docmeta "
                             "ON docmeta.path=docs.path ORDER BY docs.path").fetchall()
        if not records:
            raise ValueError("Cannot embed an empty index")
        # The production client reconciles UUID copies before ranking. This
        # single-index prototype must not silently bypass that policy.
        ids = [r[5] for r in records if r[5]]
        if len(ids) != len(set(ids)):
            raise ValueError("Hybrid prototype requires at most one local copy per finding UUID")
        self.rows = [dict(path=r[0], title=r[1], status=r[3], declared=r[4] or "") for r in records]
        self.indices = {row["path"]: i for i, row in enumerate(self.rows)}
        start = time.perf_counter()
        self.vectors = self.model.encode_document([projection(r[1], r[2]) for r in records],
                                                  normalize_embeddings=True, batch_size=args.embedding_batch_size,
                                                  show_progress_bar=False)
        if not np.isfinite(self.vectors).all():
            raise RuntimeError("Embedding model returned invalid vectors")
        self.build = {"documents": len(records), "dimensions": self.vectors.shape[1],
                      "vector_bytes": self.vectors.nbytes,
                      "encode_ms": (time.perf_counter() - start) * 1000}

    def candidates(self, question, lexical, exact):
        a, np = self.args, self.np
        start = time.perf_counter()
        vector = self.model.encode_query(a.query_prefix + question, normalize_embeddings=True,
                                        show_progress_bar=False)
        if not np.isfinite(vector).all():
            raise RuntimeError("Embedding model returned an invalid query vector")
        similarities = self.vectors @ vector
        dense = [dict(row, exact=bool(exact and exact in row["declared"].split())) for row in self.rows]
        order = sorted(range(len(dense)), key=lambda i: (
            dense[i]["status"] == "superseded", not dense[i]["exact"], -float(similarities[i])))
        dense = [dense[i] for i in order[:a.fusion_depth]]
        lr = {r["path"]: i + 1 for i, r in enumerate(lexical)}
        dr = {r["path"]: i + 1 for i, r in enumerate(dense)}
        pool = {r["path"]: r for r in lexical[:a.shortlist] + dense[:a.semantic_candidates]}
        rows = sorted(pool.values(), key=lambda r: (
            r["status"] == "superseded", not r["exact"],
            -((1 / (a.dense_k + lr[r["path"]]) if r["path"] in lr else 0) +
              (a.dense_weight / (a.dense_k + dr[r["path"]]) if r["path"] in dr else 0))))
        scores = [float(similarities[self.indices[row["path"]]]) for row in rows]
        return rows, (time.perf_counter() - start) * 1000, scores


def main():
    script_path = Path(__file__).resolve()
    script_hash = sha256(script_path)
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument("--root", type=Path, required=True)
    p.add_argument("--binary", type=Path, required=True)
    p.add_argument("--questions", type=Path, required=True)
    p.add_argument("--model", type=Path, required=True, help="Local, revision-pinned model snapshot")
    p.add_argument("--output", type=Path, required=True, help="Private report path")
    p.add_argument("--device", choices=["cpu", "mps", "cuda"], default="cpu")
    p.add_argument("--backend", choices=["torch", "onnx"], default="torch")
    p.add_argument("--onnx-file", default="onnx/model.onnx", help="Previously exported local graph")
    p.add_argument("--shortlist", type=int, default=20)
    p.add_argument("--max-length", type=int, default=512)
    p.add_argument("--batch-size", type=int, default=8)
    p.add_argument("--sort-by-length", action="store_true", help="Group similar token lengths and restore scores to original candidate order")
    p.add_argument("--attention", choices=["auto", "sdpa", "eager"], default="auto", help="Torch attention implementation; auto supports models without SDPA")
    p.add_argument("--threads", type=int, default=4)
    p.add_argument("--weight", type=float, default=1, help="Neural reciprocal-rank-fusion weight; not a confidence")
    p.add_argument("--rrf-k", type=float, default=10)
    p.add_argument("--embedding-model", type=Path, help="Optional local embedding snapshot")
    p.add_argument("--embedding-max-length", type=int, default=512)
    p.add_argument("--embedding-batch-size", type=int, default=32)
    p.add_argument("--query-prefix", default="", help="Exact query instruction required by the embedding model")
    p.add_argument("--semantic-candidates", type=int, default=8)
    p.add_argument("--fusion-depth", type=int, default=40)
    p.add_argument("--dense-weight", type=float, default=.25)
    p.add_argument("--dense-k", type=float, default=10)
    p.add_argument("--head-fusion", action="store_true", help="Research only: reorder the first five RRF results with frozen score weights")
    a = p.parse_args()
    if not 8 <= a.shortlist <= 128 or a.max_length < 16 or min(a.batch_size, a.threads) < 1:
        p.error("shortlist must be 8–128; lengths, batch size and threads must be positive")
    if not all(0 <= n < float("inf") for n in (a.weight, a.rrf_k, a.dense_weight, a.dense_k)):
        p.error("fusion weight and k must be finite and nonnegative")
    if not a.model.is_dir():
        p.error("model must be an existing local snapshot directory")
    if a.backend == "onnx" and (a.device != "cpu" or not (a.model / a.onnx_file).is_file()):
        p.error("ONNX experiments require --device cpu and a previously exported local graph")
    if a.embedding_model and (not a.embedding_model.is_dir() or a.embedding_max_length < 16 or
                              a.embedding_batch_size < 1 or
                              not a.shortlist <= a.fusion_depth <= 128 or
                              not 1 <= a.semantic_candidates <= a.fusion_depth):
        p.error("hybrid mode requires a local model, positive length and candidate depths within 1–128")
    if a.head_fusion and not a.embedding_model:
        p.error("head fusion requires the lexical + semantic candidate pool")
    raw = a.questions.read_bytes()
    cases = [json.loads(line) for line in raw.splitlines() if line.strip()]
    cases = [c for c in cases if "q" in c]
    if not cases:
        p.error("no text questions")
    for c in cases:
        if isinstance(c.get("expect"), str):
            c["expect"] = [c["expect"]]
        if not isinstance(c.get("q"), str) or not c["q"].strip() or not isinstance(c.get("expect"), list) or not c["expect"] or not all(isinstance(x, str) and x for x in c["expect"]):
            p.error("each text question requires q and one or more pre-judged expected paths")

    os.environ.update(HF_HUB_OFFLINE="1", HF_HUB_DISABLE_TELEMETRY="1", TOKENIZERS_PARALLELISM="false")
    import torch
    from sentence_transformers import CrossEncoder
    torch.set_num_threads(a.threads)
    # Some checkpoints are stored in fp16, which can be much slower on CPUs.
    # Use explicit fp32 for reproducible comparisons across Torch checkpoints.
    kwargs = {"use_safetensors": True, "dtype": torch.float32}
    if a.attention != "auto":
        kwargs["attn_implementation"] = a.attention
    if a.backend == "onnx":
        import onnxruntime as ort
        options = ort.SessionOptions()
        options.intra_op_num_threads, options.inter_op_num_threads = a.threads, 1
        kwargs = {"export": False, "file_name": a.onnx_file,
                  "provider": "CPUExecutionProvider", "session_options": options}
    start = time.perf_counter()
    model = CrossEncoder(str(a.model.resolve()), max_length=a.max_length, device=a.device,
                         backend=a.backend,
                         local_files_only=True, trust_remote_code=False,
                         activation_fn=torch.nn.Identity(),
                         model_kwargs=kwargs,
                         config_kwargs={"reference_compile": False})
    load_ms = (time.perf_counter() - start) * 1000
    model.predict([("warmup", "A short passage for warming the model.")], show_progress_bar=False)
    root, binary = a.root.resolve(), a.binary.resolve()
    subprocess.run([str(binary), "stats", "--root", str(root), "--json"],
                   check=True, capture_output=True, timeout=120)
    index_path = root / ".re-discipline/index.db"
    index_hash = sha256(index_path)
    db = sqlite3.connect(index_path.as_uri() + "?mode=ro", uri=True)
    semantic = SemanticCandidates(a, db) if a.embedding_model else None
    report = {"questions_sha256": hashlib.sha256(raw).hexdigest(),
              "evaluator_sha256": script_hash,
              "binary_sha256": sha256(binary),
              "index_sha256": index_hash,
              "model_files_sha256": model_hashes(a.model),
              "torch_dtype": "float32" if a.backend == "torch" else None,
              "attention": getattr(model.model.config, "_attn_implementation", None),
              "parameters": {k: str(v) if isinstance(v, Path) else v for k, v in vars(a).items()},
              "model_load_ms": load_ms, "cases": []}
    if semantic:
        report["embedding_files_sha256"] = model_hashes(a.embedding_model)
        report["embedding_build"] = semantic.build
    for case_number, case in enumerate(cases, 1):
        start = time.perf_counter()
        result = subprocess.run([str(binary), "query", "--root", str(root), "--sources", "local",
                                 "--baseline", "--limit", str(a.fusion_depth if semantic else a.shortlist), "--json", case["q"]],
                                check=True, capture_output=True, text=True, timeout=120)
        rows = json.loads(result.stdout)
        retrieval_ms = (time.perf_counter() - start) * 1000
        exact = case["q"].strip().strip('"`').lower()
        if not re.fullmatch("[a-z0-9_:]+", exact):
            exact = ""
        for row in rows:
            record = db.execute("SELECT idents FROM docmeta WHERE path=?", (row["path"],)).fetchone()
            row["exact"] = bool(exact and record and exact in (record[0] or "").split())
        before = [row["path"] for row in rows]
        semantic_ms = 0
        if semantic:
            rows, semantic_ms, semantic_scores = semantic.candidates(case["q"], rows, exact)
        candidates = [row["path"] for row in rows]
        lexical_scores, feature_ms = [], 0
        if a.head_fusion:
            start = time.perf_counter()
            pool = min(128, max(32, 4 * a.fusion_depth))
            result = subprocess.run([str(binary), "explain", "--root", str(root), "--limit", str(pool),
                                     "--json", case["q"]], check=True, capture_output=True, text=True, timeout=120)
            lexical_scores = lexical_features(before, json.loads(result.stdout)["rows"], candidates)
            feature_ms = (time.perf_counter() - start) * 1000
        pairs = []
        for row in rows:
            record = db.execute("SELECT body, docmeta.idents FROM docs LEFT JOIN docmeta ON "
                                "docmeta.path=docs.path WHERE docs.path=?", (row["path"],)).fetchone()
            if record is None:
                raise RuntimeError("Corpus changed while evaluating; rerun on a fixed snapshot")
            body, declared = record
            row["exact"] = bool(exact and exact in (declared or "").split())
            pairs.append((case["q"], projection(row["title"], body)))
        start = time.perf_counter()
        scores = score_pairs(model, pairs, a.batch_size, a.sort_by_length)
        inference_ms = (time.perf_counter() - start) * 1000
        order = rank(rows, scores, a.weight, a.rrf_k)
        rrf_paths = [rows[i]["path"] for i in order]
        if a.head_fusion:
            order = head_fusion(rows, order, lexical_scores, semantic_scores, scores)
        after = [rows[i]["path"] for i in order]
        report["cases"].append(dict(case, baseline_rank=answer_rank(before[:8], case["expect"]),
                                   candidate_rank=answer_rank(candidates, case["expect"]),
                                   fusion_rank=answer_rank(candidates[:8], case["expect"]),
                                   reranked_rank=answer_rank(after[:8], case["expect"]),
                                   baseline_paths=before, candidate_paths=candidates, reranked_paths=after, scores=scores,
                                   retrieval_ms=retrieval_ms, semantic_ms=semantic_ms, inference_ms=inference_ms))
        if a.head_fusion:
            report["cases"][-1].update(rrf_paths=rrf_paths, rrf_rank=answer_rank(rrf_paths[:8], case["expect"]),
                                      lexical_scores=lexical_scores, semantic_scores=semantic_scores,
                                      feature_ms=feature_ms)
        if case_number % 25 == 0:
            print(f"Evaluated {case_number}/{len(cases)} questions", flush=True)
    db.close()
    if (sha256(index_path) != index_hash or sha256(binary) != report["binary_sha256"] or
            sha256(script_path) != script_hash):
        raise RuntimeError("Index, binary or evaluator changed while evaluating; rerun on fixed inputs")
    times = sorted(c["inference_ms"] for c in report["cases"])
    report["summary"] = {
        "baseline": metrics([c["baseline_rank"] for c in report["cases"]]),
        "reranked": metrics([c["reranked_rank"] for c in report["cases"]]),
        "fusion": metrics([c["fusion_rank"] for c in report["cases"]]),
        "candidate_recall": sum(c["candidate_rank"] > 0 for c in report["cases"]),
        "inference_p50_ms": statistics.median(times), "inference_p95_ms": times[int(.95 * (len(times) - 1))]}
    if a.head_fusion:
        report["summary"]["rrf_before_head_fusion"] = metrics([c["rrf_rank"] for c in report["cases"]])
        report["head_fusion"] = {"head_size": 5, "weights": {"lexical": .25, "semantic": .25, "neural": .5},
                                 "normalization": "minmax over original candidate pool; constant channel is zero"}
    a.output.parent.mkdir(parents=True, exist_ok=True)
    a.output.write_text(json.dumps(report, indent=2) + "\n")
    print(json.dumps(report["summary"], indent=2))


if __name__ == "__main__":
    main()
