import copy
import unittest
from resolve_mr import resolve, resolve_with_retry, ReviewError, DiffNotReady

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
            if path.startswith("/projects/42/merge_requests/17/versions?per_page=100"):
                if isinstance(versions, dict):
                    return versions[int(path.split("&page=")[-1]) if "&page=" in path else 1]
                return versions or []
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

    def test_pre_merge_events_pin_source_even_after_merge_or_close(self):
        for action in ("open", "update", "reopen"):
            for state in ("opened", "closed", "merged"):
                with self.subTest(action=action, state=state):
                    result, _ = self.run_resolver(dict(MR, state=state), event_action=action)
                    self.assertEqual(result["result_sha"], HEAD)
                    self.assertEqual(result["merge_commit_sha"], "")
                    self.assertEqual(result["squash_commit_sha"], "")
                    self.assertEqual(result["review_phase"], "pre_merge")

    def test_queued_update_uses_historical_version_with_pagination(self):
        mr = dict(MR, state="opened", sha=MERGE, diff_refs={"base_sha": SQUASH, "head_sha": MERGE, "start_sha": SQUASH})
        versions = {
            1: [{"id": i + 100, "base_commit_sha": SQUASH, "head_commit_sha": MERGE, "start_commit_sha": SQUASH} for i in range(100)],
            2: [{"id": 2, "base_commit_sha": BASE, "head_commit_sha": HEAD, "start_commit_sha": BASE}],
        }
        result, calls = self.run_resolver(mr, versions=versions, event_action="update")
        self.assertEqual(result["diff_refs"], MR["diff_refs"])
        self.assertEqual(result["result_sha"], HEAD)
        self.assertTrue(calls[-1].endswith("&page=2"))
        with self.assertRaises(DiffNotReady):
            self.run_resolver(mr, versions=[], event_action="update")

    def test_diff_version_search_is_bounded(self):
        versions = {page: [{"id": i, "head_commit_sha": MERGE} for i in range(100)] for page in range(1, 11)}
        with self.assertRaises(DiffNotReady):
            self.run_resolver(dict(MR, diff_refs=None), versions=versions, event_action="open")

    def test_rejects_changed_target_source_or_unsupported_event(self):
        for action in ("merge", "open", "update"):
            with self.subTest(action=action):
                with self.assertRaises(ReviewError):
                    self.run_resolver(dict(MR, target_branch="release"), event_action=action)
                with self.assertRaises(ReviewError):
                    self.run_resolver(event_action=action, source_branch="different")
                with self.assertRaises(ReviewError):
                    self.run_resolver(event_action=action, target_branch="release")
        with self.assertRaises(ReviewError):
            self.run_resolver(event_action="approved")

    def test_new_mr_retries_pending_refs_but_not_invalid_identity(self):
        waits = []
        reads = 0
        def get(path):
            nonlocal reads
            if path == "/projects/42": return copy.deepcopy(PROJECT)
            if path.endswith("/versions?per_page=100"): return []
            reads += 1
            return dict(MR, state="opened", diff_refs=None if reads == 1 else MR["diff_refs"])
        result = resolve_with_retry(get, 42, 17, PROJECT["path_with_namespace"], HEAD, event_action="open", sleep=waits.append)
        self.assertEqual(result["result_sha"], HEAD)
        self.assertEqual(waits, [2])
        waits.clear()
        with self.assertRaises(ReviewError):
            resolve_with_retry(lambda _: {}, 42, 17, PROJECT["path_with_namespace"], HEAD, event_action="open", sleep=waits.append)
        self.assertEqual(waits, [])

    def test_missing_refs_retry_has_a_limit(self):
        waits = []
        def get(path):
            if path == "/projects/42": return PROJECT
            if path.endswith("/versions?per_page=100"): return []
            return dict(MR, state="opened", diff_refs=None)
        with self.assertRaises(DiffNotReady):
            resolve_with_retry(get, 42, 17, PROJECT["path_with_namespace"], HEAD, event_action="open", sleep=waits.append)
        self.assertEqual(waits, [2, 4])

    def test_incomplete_matching_version_retries_until_ready(self):
        reads, waits = 0, []
        def get(path):
            nonlocal reads
            if path == "/projects/42": return PROJECT
            if path.endswith("/versions?per_page=100"):
                reads += 1
                return [{"id": 1, "head_commit_sha": HEAD, "base_commit_sha": None if reads == 1 else BASE, "start_commit_sha": BASE}]
            return dict(MR, state="opened", diff_refs=None)
        result = resolve_with_retry(get, 42, 17, PROJECT["path_with_namespace"], HEAD, event_action="open", sleep=waits.append)
        self.assertEqual(result["diff_refs"], MR["diff_refs"])
        self.assertEqual(waits, [2])


if __name__ == "__main__":
    unittest.main()
