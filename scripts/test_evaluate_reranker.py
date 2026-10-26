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


if __name__ == "__main__":
    unittest.main()
