import copy
import unittest
from resolve_mr import resolve, ReviewError

BASE, HEAD, MERGE, SQUASH = (c * 40 for c in "abcd")
PROJECT = {"id": 42, "path_with_namespace": "nova/game-play/kraken", "web_url": "https://git.easycodesource.com/nova/game-play/kraken"}
MR = {"iid": 17, "target_project_id": 42, "state": "merged", "source_branch": "fix/bet", "target_branch": "main", "sha": HEAD, "merge_commit_sha": MERGE, "squash_commit_sha": None, "diff_refs": {"base_sha": BASE, "head_sha": HEAD, "start_sha": BASE}}


class ResolveTests(unittest.TestCase):
    def run_resolver(self, mr=None, project=None, versions=None, **kwargs):
        calls = []
        def get(path):
            calls.append(path)
            if path == "/projects/42": return copy.deepcopy(project if project is not None else PROJECT)
            if path == "/projects/42/merge_requests/17": return copy.deepcopy(mr if mr is not None else MR)
            if path == "/projects/42/merge_requests/17/versions?per_page=100": return versions
            self.fail("Unexpected API request: " + path)
        result = resolve(get, 42, 17, "nova/game-play/kraken", HEAD, **kwargs)
        return result, calls

    def test_merge_and_squash_and_fast_forward(self):
        for merge, squash, expected in [(MERGE, None, MERGE), (None, SQUASH, SQUASH), (None, None, HEAD), (HEAD, None, HEAD)]:
            with self.subTest(merge=merge, squash=squash):
                mr = copy.deepcopy(MR)
                mr.update(merge_commit_sha=merge, squash_commit_sha=squash)
                result, calls = self.run_resolver(mr)
                self.assertEqual(result["result_sha"], expected)
                self.assertEqual(result["diff_refs"], MR["diff_refs"])
                self.assertEqual(len(calls), 2)

    def test_rejects_wrong_identity_or_revision(self):
        for field, value in [("state", "opened"), ("sha", BASE), ("iid", 99), ("target_project_id", 99)]:
            with self.subTest(field=field):
                mr = dict(MR, **{field: value})
                with self.assertRaises(ReviewError): self.run_resolver(mr)
        with self.assertRaises(ReviewError): self.run_resolver(project=dict(PROJECT, web_url="https://other.test/nova/game-play/kraken"))
        with self.assertRaises(ReviewError): self.run_resolver(merge_sha=BASE)
        with self.assertRaises(ReviewError): self.run_resolver(squash_sha=SQUASH)

    def test_missing_refs_selects_matching_version(self):
        mr = dict(MR, diff_refs=None)
        versions = [
            {"id": 3, "base_commit_sha": BASE, "head_commit_sha": MERGE, "start_commit_sha": BASE},
            {"id": 2, "base_commit_sha": BASE, "head_commit_sha": HEAD, "start_commit_sha": BASE},
            {"id": 1, "base_commit_sha": SQUASH, "head_commit_sha": HEAD, "start_commit_sha": SQUASH},
        ]
        result, calls = self.run_resolver(mr, versions=versions)
        self.assertEqual(result["diff_refs"], MR["diff_refs"])
        self.assertEqual(len(calls), 3)
        with self.assertRaises(ReviewError): self.run_resolver(mr, versions=[])

    def test_no_branch_tip_or_zero_baseline_fallback(self):
        for bad_base in [None, "0"*40, "main"]:
            mr = copy.deepcopy(MR)
            mr["diff_refs"]["base_sha"] = bad_base
            with self.assertRaises(ReviewError): self.run_resolver(mr, versions=[])


if __name__ == "__main__":
    unittest.main()
