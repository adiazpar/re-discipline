"""Dependency-free checks for evaluation integrity; does not load models."""
import importlib.util
from pathlib import Path
import unittest


spec = importlib.util.spec_from_file_location(
    "evaluate_reranker", Path(__file__).with_name("evaluate-reranker.py"))
evaluation = importlib.util.module_from_spec(spec)
spec.loader.exec_module(evaluation)


class Scores(list):
    def tolist(self):
        return list(self)


class RecordingModel:
    def __init__(self, values=None):
        self.values = values
        self.seen = []

    def tokenizer(self, pairs, **kwargs):
        return {"input_ids": [[0] * len(document) for _, document in pairs]}

    def predict(self, pairs, **kwargs):
        self.seen = list(pairs)
        return Scores(self.values if self.values is not None else
                      [float(query) for query, _ in pairs])


class EvaluationIntegrityTests(unittest.TestCase):
    def test_length_grouping_keeps_each_score_with_its_document(self):
        pairs = [("11", "a"), ("22", "longest"), ("33", "three"), ("44", "equal")]
        model = RecordingModel()
        self.assertEqual(evaluation.score_pairs(model, pairs, 2, True), [11, 22, 33, 44])
        self.assertEqual(model.seen, [pairs[1], pairs[2], pairs[3], pairs[0]])
        self.assertEqual(evaluation.score_pairs(model, pairs, 2), [11, 22, 33, 44])
        self.assertEqual(model.seen, pairs)

    def test_empty_candidates_do_not_invoke_model(self):
        model = RecordingModel()
        self.assertEqual(evaluation.score_pairs(model, [], 2, True), [])
        self.assertEqual(model.seen, [])

    def test_invalid_outputs_cannot_silently_corrupt_metrics(self):
        pairs = [("1", "first"), ("2", "second")]
        for values in ([1], [1, 2, 3], [1, float("nan")], [float("inf"), 2], [[1], [2]]):
            with self.subTest(values=values), self.assertRaisesRegex(RuntimeError, "invalid scores"):
                evaluation.score_pairs(RecordingModel(values), pairs, 2, True)
        model = RecordingModel()
        model.tokenizer = lambda *args, **kwargs: {"input_ids": [[0]]}
        with self.assertRaisesRegex(RuntimeError, "different number"):
            evaluation.score_pairs(model, pairs, 2, True)

    def test_model_scores_do_not_override_authority_and_supersession(self):
        rows = [dict(exact=False, status="promoted"),
                dict(exact=True, status="superseded"),
                dict(exact=True, status="promoted")]
        self.assertEqual(evaluation.rank(rows, [100, 200, -100], 1000, 10), [2, 0, 1])

    def test_multiple_prejudged_answers_are_accepted_without_insertion(self):
        self.assertEqual(evaluation.answer_rank(["other", "answer-b"], ["answer-a", "answer-b"]), 2)
        self.assertEqual(evaluation.answer_rank(["other"], ["answer-a", "answer-b"]), 0)

    def test_projection_preserves_scope_while_removing_local_evidence(self):
        body = "# Finding\n\nA claim.\n\n## Evidence\nprivate/script.py\n\n## Scope\nBuild 7 only.\n"
        text = evaluation.projection("Finding", body)
        self.assertNotIn("private/script.py", text)
        self.assertIn("A claim.", text)
        self.assertIn("Build 7 only.", text)
        self.assertEqual(text.count("Finding"), 1)

    def test_head_fusion_preserves_membership_tail_and_score_alignment(self):
        rows = [dict(exact=False, status="promoted") for _ in range(8)]
        order = [4, 2, 0, 1, 3, 7, 5, 6]
        scores = list(range(8))
        result = evaluation.head_fusion(rows, order, scores, scores, scores)
        self.assertEqual(result, [4, 3, 2, 1, 0, 7, 5, 6])
        self.assertEqual(set(result[:5]), set(order[:5]))
        self.assertEqual(result[5:], order[5:])
        # A flat score channel must neither divide by zero nor shuffle ties.
        self.assertEqual(evaluation.head_fusion(rows, order, [3] * 8, [7] * 8, [-1] * 8), order)
        self.assertEqual(evaluation.head_fusion([], [], [], [], []), [])

    def test_head_fusion_keeps_authority_and_supersession(self):
        rows = [dict(exact=True, status="promoted"), dict(exact=False, status="promoted"),
                dict(exact=True, status="superseded")]
        scores = [-10, 0, 100]
        self.assertEqual(evaluation.head_fusion(rows, [0, 1, 2], scores, scores, scores), [0, 1, 2])

    def test_head_fusion_rejects_misaligned_or_invalid_features(self):
        rows = [dict(exact=False, status="promoted") for _ in range(2)]
        for order, values in [([0, 0], [1, 2]), ([0, 1], [1]),
                              ([0, 1], [1, float("nan")]), ([0, 1], [float("inf"), 2])]:
            with self.subTest(order=order, values=values), self.assertRaises(ValueError):
                evaluation.head_fusion(rows, order, values, [1, 2], [1, 2])
        self.assertEqual(evaluation.normalize_scores([-5, 0, 5]), [0, .5, 1])

    def test_lexical_features_only_use_the_actual_query_pool(self):
        explained = [dict(path="a", final=-4), dict(path="b", final=1), dict(path="c", final=-2)]
        self.assertEqual(evaluation.lexical_features(["a", "b"], explained, ["c", "a", "b"]), [0, 4, 0])
        with self.assertRaisesRegex(RuntimeError, "order differs"):
            evaluation.lexical_features(["b", "a"], explained, ["a", "b"])
        with self.assertRaisesRegex(RuntimeError, "invalid lexical"):
            evaluation.lexical_features(["a"], [dict(path="a", final=float("nan"))], ["a"])

    def test_semantic_only_candidates_receive_zero_lexical_evidence(self):
        self.assertEqual(evaluation.lexical_features([], None, ["semantic-a", "semantic-b"]), [0, 0])
        self.assertEqual(evaluation.lexical_features([], [], ["semantic-a"]), [0])
        with self.assertRaisesRegex(RuntimeError, "order differs"):
            evaluation.lexical_features(["a"], None, ["a"])


if __name__ == "__main__":
    unittest.main()
