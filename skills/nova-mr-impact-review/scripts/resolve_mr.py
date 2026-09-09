#!/usr/bin/env python3
"""Read immutable MR review metadata without exposing GitLab credentials."""
import argparse
import json
import os
from pathlib import Path
import re
import sys
import urllib.error
import urllib.request

HOST = "https://git.easycodesource.com"
SHA = re.compile(r"[0-9a-fA-F]{40}(?:[0-9a-fA-F]{24})?\Z")


class ReviewError(Exception):
    pass


class NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, req, fp, code, msg, headers, newurl):
        raise ReviewError("GitLab API redirected; verify the configured host")


def checked_sha(value, name, optional=False):
    if not value and optional:
        return ""
    if not isinstance(value, str) or not SHA.fullmatch(value) or set(value) == {"0"}:
        raise ReviewError(f"Missing or invalid {name}")
    return value.lower()


def resolve(get, project_id, mr_iid, project_path, head_sha, merge_sha="", squash_sha=""):
    if project_id < 1 or mr_iid < 1:
        raise ReviewError("Project ID and MR IID must be positive")
    if not project_path.startswith("nova/game-play/") or any(
        not re.fullmatch(r"[A-Za-z0-9][A-Za-z0-9._-]*", part) or part in (".", "..")
        for part in project_path.split("/")
    ):
        raise ReviewError("Project is outside nova/game-play")
    project = get(f"/projects/{project_id}")
    project_url = f"{HOST}/{project_path}"
    if project.get("id") != project_id or project.get("path_with_namespace") != project_path or project.get("web_url", "").rstrip("/") != project_url:
        raise ReviewError("Project identity does not match webhook")
    mr_path = f"/projects/{project_id}/merge_requests/{mr_iid}"
    mr = get(mr_path)
    if mr.get("iid") != mr_iid or mr.get("target_project_id") != project_id or mr.get("state") != "merged":
        raise ReviewError("MR identity or merged state does not match webhook")
    head = checked_sha(head_sha, "webhook head SHA")
    if checked_sha(mr.get("sha"), "MR head SHA") != head:
        raise ReviewError("MR head differs from webhook; do not review a newer revision")
    merge = checked_sha(mr.get("merge_commit_sha"), "merge commit SHA", optional=True)
    squash = checked_sha(mr.get("squash_commit_sha"), "squash commit SHA", optional=True)
    for provided, actual, name in ((merge_sha, merge, "merge"), (squash_sha, squash, "squash")):
        if provided and checked_sha(provided, name + " SHA") != actual:
            raise ReviewError(f"MR {name} SHA differs from webhook")
    refs = mr.get("diff_refs") or {}
    if (refs.get("head_sha") or "").lower() != head or not refs.get("base_sha"):
        # Diff refs can be temporarily unavailable; resolve the matching immutable version.
        versions = get(mr_path + "/versions?per_page=100")
        matches = [v for v in versions if (v.get("head_commit_sha") or "").lower() == head]
        if not matches:
            raise ReviewError("No matching MR diff version; retry later, never use branch HEAD")
        latest = max(matches, key=lambda v: v["id"])
        refs = {"base_sha": latest.get("base_commit_sha"), "head_sha": latest.get("head_commit_sha"), "start_sha": latest.get("start_commit_sha")}
    refs = {k: checked_sha(refs.get(k), "diff " + k) for k in ("base_sha", "head_sha", "start_sha")}
    if refs["head_sha"] != head:
        raise ReviewError("Diff head does not match webhook")
    return {
        "project_id": project_id, "project_path": project_path,
        "mr_iid": mr_iid, "mr_url": f"{project_url}/-/merge_requests/{mr_iid}",
        "source_branch": mr["source_branch"], "target_branch": mr["target_branch"],
        "head_sha": head, "merge_commit_sha": merge, "squash_commit_sha": squash,
        "diff_refs": refs,
        "result_sha": merge or squash or head,
    }


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("project_id", type=int)
    parser.add_argument("mr_iid", type=int)
    parser.add_argument("project_path")
    parser.add_argument("head_sha")
    parser.add_argument("--merge-sha", default="")
    parser.add_argument("--squash-sha", default="")
    args = parser.parse_args()
    token = os.environ.get("GITLAB_TOKEN", "")
    if not token:
        path = Path(os.environ.get("GITLAB_TOKEN_FILE", "/etc/ai-review/gitlab-api-token"))
        try:
            token = path.read_text().strip()
        except OSError:
            raise ReviewError("Configure GITLAB_TOKEN or the protected GitLab token file") from None
    if not token:
        raise ReviewError("GitLab token is empty")
    opener = urllib.request.build_opener(NoRedirect())

    def get(path):
        request = urllib.request.Request(HOST + "/api/v4" + path, headers={"PRIVATE-TOKEN": token})
        try:
            with opener.open(request, timeout=30) as response:
                return json.load(response)
        except urllib.error.HTTPError as exc:
            raise ReviewError(f"GitLab API HTTP {exc.code}") from None
        except (urllib.error.URLError, TimeoutError, json.JSONDecodeError):
            raise ReviewError("Unable to read GitLab API response") from None

    print(json.dumps(resolve(get, args.project_id, args.mr_iid, args.project_path, args.head_sha, args.merge_sha, args.squash_sha), ensure_ascii=False, indent=2))


if __name__ == "__main__":
    try:
        main()
    except ReviewError as exc:
        print(str(exc), file=sys.stderr)
        sys.exit(1)
