#!/usr/bin/env python3
"""Preview or submit Nova game-play Jenkins build requests."""

from __future__ import annotations

import argparse
import base64
import http.cookiejar
import json
import os
from pathlib import Path
import re
import sys
from typing import Iterable
import urllib.error
import urllib.parse
import urllib.request


DEFAULT_JENKINS_URL = "https://jenkins.offline-ops.net"
DEFAULT_BRANCH = "main"
DEFAULT_DEPLOY_ENV = "香港 int2 测试环境 local01"
DEFAULT_OPERATION_TYPE = "FullDeploy"
SKILL_ROOT = Path(__file__).resolve().parents[1]
PROJECT_GROUP_DIR = SKILL_ROOT / "references" / "project-groups"
PROJECT_PATTERN = re.compile(r"^[A-Za-z0-9][A-Za-z0-9._-]*$")
GROUP_PATTERN = re.compile(r"^[A-Za-z0-9][A-Za-z0-9_-]*$")
OPERATION_TYPES = {
    "f": "FullDeploy",
    "full": "FullDeploy",
    "fulldeploy": "FullDeploy",
    "u": "UpdateConfigMapAndRestart",
    "update": "UpdateConfigMapAndRestart",
    "updateconfigmapandrestart": "UpdateConfigMapAndRestart",
    "r": "Reconfig",
    "reconfig": "Reconfig",
}


class BuildRequestError(RuntimeError):
    """Raised when Jenkins rejects or cannot receive a request."""


def resolve_credentials() -> tuple[str, str]:
    user = os.environ.get("JENKINS_USER", "").strip()
    token = os.environ.get("JENKINS_TOKEN", "").strip()
    return user, token


def positive_number(value: str) -> float:
    number = float(value)
    if number <= 0:
        raise argparse.ArgumentTypeError("must be greater than zero")
    return number


def normalize_operation(value: str) -> str:
    normalized = re.sub(r"[^a-z]", "", value.lower())
    try:
        return OPERATION_TYPES[normalized]
    except KeyError as exc:
        allowed = "FullDeploy, UpdateConfigMapAndRestart, Reconfig (or f/u/r)"
        raise argparse.ArgumentTypeError(f"expected one of: {allowed}") from exc


def normalize_base_url(value: str) -> str:
    parsed = urllib.parse.urlsplit(value.rstrip("/"))
    if parsed.scheme not in {"http", "https"} or not parsed.netloc:
        raise argparse.ArgumentTypeError("must be an absolute HTTP(S) URL")
    if parsed.username is not None or parsed.password is not None:
        raise argparse.ArgumentTypeError("must not include credentials")
    if parsed.query or parsed.fragment:
        raise argparse.ArgumentTypeError("must not include a query or fragment")
    if parsed.scheme == "http" and parsed.hostname not in {
        "localhost",
        "127.0.0.1",
        "::1",
    }:
        raise argparse.ArgumentTypeError("plain HTTP is allowed only for local testing")
    return urllib.parse.urlunsplit(
        (parsed.scheme, parsed.netloc, parsed.path.rstrip("/"), "", "")
    )


def url_origin(value: str) -> tuple[str, str, int | None]:
    parsed = urllib.parse.urlsplit(value)
    scheme = parsed.scheme.lower()
    default_port = 443 if scheme == "https" else 80 if scheme == "http" else None
    return scheme, (parsed.hostname or "").lower(), parsed.port or default_port


class SameOriginRedirectHandler(urllib.request.HTTPRedirectHandler):
    """Allow Jenkins redirects without forwarding credentials to another host."""

    def redirect_request(self, req, fp, code, msg, headers, newurl):
        if url_origin(req.full_url) != url_origin(newurl):
            raise BuildRequestError("Jenkins attempted a cross-origin redirect")
        return super().redirect_request(req, fp, code, msg, headers, newurl)


def read_project_file(path: Path) -> list[str]:
    try:
        lines = path.read_text(encoding="utf-8").splitlines()
    except OSError as exc:
        raise argparse.ArgumentTypeError(
            f"cannot read project file {path}: {exc}"
        ) from exc
    return [
        line.strip()
        for line in lines
        if line.strip() and not line.lstrip().startswith("#")
    ]


def normalize_group_name(value: str) -> str:
    name = value.strip()
    if name.endswith(".txt"):
        name = name[:-4]
    if not GROUP_PATTERN.fullmatch(name):
        raise argparse.ArgumentTypeError(
            f"invalid project group {value!r}; use letters, digits, underscore, or hyphen"
        )
    return name


def available_groups() -> list[str]:
    try:
        paths = PROJECT_GROUP_DIR.glob("*.txt")
        return sorted(path.stem for path in paths if path.is_file())
    except OSError as exc:
        raise BuildRequestError(f"cannot list bundled project groups: {exc}") from exc


def read_project_group(value: str) -> tuple[str, list[str]]:
    name = normalize_group_name(value)
    path = PROJECT_GROUP_DIR / f"{name}.txt"
    if not path.is_file():
        raise argparse.ArgumentTypeError(
            f"unknown project group {name!r}; use --list-groups to inspect bundled groups"
        )
    return name, read_project_file(path)


def unique_projects(values: Iterable[str]) -> list[str]:
    projects: list[str] = []
    seen: set[str] = set()
    for value in values:
        for project in value.split(","):
            project = project.strip()
            if not project:
                continue
            if not PROJECT_PATTERN.fullmatch(project):
                raise argparse.ArgumentTypeError(
                    f"invalid project name {project!r}; use letters, digits, dot, underscore, or hyphen"
                )
            if project not in seen:
                seen.add(project)
                projects.append(project)
    return projects


def build_url(base_url: str, project: str) -> str:
    project_segment = urllib.parse.quote(project, safe="")
    return f"{base_url}/job/nova/job/game-play/job/{project_segment}/build?delay=0sec"


def basic_auth_headers(user: str, token: str) -> dict[str, str]:
    encoded = base64.b64encode(f"{user}:{token}".encode("utf-8")).decode("ascii")
    return {
        "Authorization": f"Basic {encoded}",
        "Accept": "application/json, text/plain, */*",
        "User-Agent": "codex-jenkins-trigger-build/1.0",
    }


def response_excerpt(exc: urllib.error.HTTPError) -> str:
    try:
        body = exc.read(512).decode("utf-8", errors="replace")
    except OSError:
        return ""
    return " ".join(body.split())[:300]


def open_request(
    opener: urllib.request.OpenerDirector,
    request: urllib.request.Request,
    timeout: float,
    purpose: str,
):
    try:
        return opener.open(request, timeout=timeout)
    except urllib.error.HTTPError as exc:
        detail = response_excerpt(exc)
        suffix = f": {detail}" if detail else ""
        raise BuildRequestError(
            f"{purpose} failed with HTTP {exc.code} {exc.reason}{suffix}"
        ) from exc
    except urllib.error.URLError as exc:
        raise BuildRequestError(f"{purpose} failed: {exc.reason}") from exc


def fetch_crumb(
    opener: urllib.request.OpenerDirector,
    base_url: str,
    headers: dict[str, str],
    timeout: float,
) -> tuple[str, str]:
    url = f"{base_url}/crumbIssuer/api/json"
    request = urllib.request.Request(url, headers=headers, method="GET")
    with open_request(opener, request, timeout, "Jenkins crumb request") as response:
        raw = response.read()
    try:
        payload = json.loads(raw)
        field = payload["crumbRequestField"]
        crumb = payload["crumb"]
    except (json.JSONDecodeError, KeyError, TypeError) as exc:
        raise BuildRequestError("Jenkins crumb response was not valid JSON") from exc
    if (
        not isinstance(field, str)
        or not field
        or not isinstance(crumb, str)
        or not crumb
    ):
        raise BuildRequestError("Jenkins crumb response omitted required values")
    return field, crumb


def form_payload(
    project: str,
    branch: str,
    deploy_env: str,
    operation_type: str,
    crumb_field: str,
    crumb: str,
) -> bytes:
    parameters = [
        {"name": "jobTitle"},
        {"name": "OperationType", "value": operation_type},
        {"name": "branchTitle"},
        {"name": "GitBranch", "value": branch, "quickFilter": ""},
        {"name": "microServicesTitle"},
        {"name": "DeployMicroServices", "value": project},
        {"name": "envTitle"},
        {"name": "OperatingEnvs", "value": deploy_env},
        {"name": "additionalTitle"},
        {"name": "AdditionalOps"},
    ]
    payload = {
        "parameter": parameters,
        "statusCode": "303",
        "redirectTo": ".",
        "": "",
        crumb_field: crumb,
    }
    compact_json = json.dumps(payload, ensure_ascii=False, separators=(",", ":"))
    return urllib.parse.urlencode({"json": compact_json}).encode("utf-8")


def submit_build(
    opener: urllib.request.OpenerDirector,
    base_url: str,
    project: str,
    branch: str,
    deploy_env: str,
    operation_type: str,
    crumb_field: str,
    crumb: str,
    headers: dict[str, str],
    timeout: float,
) -> dict[str, object]:
    url = build_url(base_url, project)
    request_headers = dict(headers)
    request_headers["Content-Type"] = "application/x-www-form-urlencoded"
    request_headers[crumb_field] = crumb
    request = urllib.request.Request(
        url,
        data=form_payload(
            project, branch, deploy_env, operation_type, crumb_field, crumb
        ),
        headers=request_headers,
        method="POST",
    )
    with open_request(
        opener, request, timeout, f"build submission for {project}"
    ) as response:
        response_url = response.geturl()
        status = response.status
    if url_origin(response_url) != url_origin(url):
        raise BuildRequestError(f"build submission for {project} changed origin")
    if "/login" in urllib.parse.urlsplit(response_url).path:
        raise BuildRequestError(f"build submission for {project} redirected to login")
    return {
        "status": "submitted",
        "project": project,
        "branch": branch,
        "deploy_env": deploy_env,
        "operation_type": operation_type,
        "http_status": status,
        "request_url": url,
        "response_url": response_url,
    }


def parse_args(argv: list[str]) -> argparse.Namespace:
    parser = argparse.ArgumentParser(
        description="Preview or submit Nova game-play Jenkins build requests."
    )
    default_deploy_env = (
        os.environ.get("JENKINS_DEPLOY_ENV", "").strip() or DEFAULT_DEPLOY_ENV
    )
    try:
        default_operation_type = normalize_operation(
            os.environ.get("JENKINS_OPERATION_TYPE", "").strip()
            or DEFAULT_OPERATION_TYPE
        )
    except argparse.ArgumentTypeError as exc:
        parser.error(str(exc))
    parser.add_argument(
        "-p",
        "--project",
        action="append",
        default=[],
        help="project name; repeat the flag or use comma-separated names",
    )
    parser.add_argument(
        "-g",
        "--group",
        action="append",
        default=[],
        help="bundled project group name; repeat the flag to combine groups",
    )
    parser.add_argument(
        "--projects-file",
        action="append",
        default=[],
        type=Path,
        help="UTF-8 file containing one project name per line",
    )
    parser.add_argument(
        "-b",
        "--branch",
        default=DEFAULT_BRANCH,
        help=f"exact Git branch to build (default: {DEFAULT_BRANCH})",
    )
    parser.add_argument(
        "-e",
        "--deploy-env",
        default=default_deploy_env,
        help=f"Jenkins deployment environment (default: {default_deploy_env})",
    )
    parser.add_argument(
        "-t",
        "--operation-type",
        default=default_operation_type,
        type=normalize_operation,
        help=f"FullDeploy, UpdateConfigMapAndRestart, Reconfig, or f/u/r (default: {default_operation_type})",
    )
    parser.add_argument(
        "--base-url",
        default=os.environ.get("JENKINS_URL", DEFAULT_JENKINS_URL),
        type=normalize_base_url,
        help="Jenkins base URL (default: JENKINS_URL or the Nova Jenkins URL)",
    )
    parser.add_argument(
        "--timeout",
        default=30.0,
        type=positive_number,
        help="HTTP timeout in seconds (default: 30)",
    )
    parser.add_argument(
        "--execute",
        action="store_true",
        help="send the requests; without this flag, only print a preview",
    )
    parser.add_argument(
        "--list-groups",
        action="store_true",
        help="print bundled project group names and exit without network access",
    )
    args = parser.parse_args(argv)

    if args.list_groups:
        if args.execute:
            parser.error("--list-groups cannot be combined with --execute")
        return args

    try:
        raw_projects = list(args.project)
        group_names: list[str] = []
        for group in args.group:
            group_name, group_projects = read_project_group(group)
            group_names.append(group_name)
            raw_projects.extend(group_projects)
        for project_file in args.projects_file:
            raw_projects.extend(read_project_file(project_file))
        args.projects = unique_projects(raw_projects)
    except argparse.ArgumentTypeError as exc:
        parser.error(str(exc))
    if not args.projects:
        parser.error("provide at least one --project, --group, or --projects-file")
    if not args.branch.strip():
        parser.error("--branch must not be empty")
    if not args.deploy_env.strip():
        parser.error("--deploy-env must not be empty")
    args.branch = args.branch.strip()
    args.deploy_env = args.deploy_env.strip()
    args.groups = group_names
    return args


def preview(args: argparse.Namespace) -> dict[str, object]:
    return {
        "mode": "preview",
        "network_request_sent": False,
        "base_url": args.base_url,
        "projects": args.projects,
        "groups": args.groups,
        "branch": args.branch,
        "deploy_env": args.deploy_env,
        "operation_type": args.operation_type,
        "request_urls": [
            build_url(args.base_url, project) for project in args.projects
        ],
        "credential_sources": ["JENKINS_USER", "JENKINS_TOKEN"],
    }


def main(argv: list[str] | None = None) -> int:
    args = parse_args(argv if argv is not None else sys.argv[1:])
    if args.list_groups:
        print(
            json.dumps(
                {
                    "mode": "list-groups",
                    "network_request_sent": False,
                    "groups": available_groups(),
                },
                ensure_ascii=False,
                indent=2,
            )
        )
        return 0
    if not args.execute:
        print(json.dumps(preview(args), ensure_ascii=False, indent=2))
        return 0

    user, token = resolve_credentials()
    if not user or not token:
        missing = [
            name
            for name, value in (("JENKINS_USER", user), ("JENKINS_TOKEN", token))
            if not value
        ]
        print(
            f"error: set {' and '.join(missing)} in the Codex service environment before using --execute",
            file=sys.stderr,
        )
        return 2

    headers = basic_auth_headers(user, token)
    opener = urllib.request.build_opener(
        SameOriginRedirectHandler(),
        urllib.request.HTTPCookieProcessor(http.cookiejar.CookieJar()),
    )
    try:
        crumb_field, crumb = fetch_crumb(opener, args.base_url, headers, args.timeout)
    except BuildRequestError as exc:
        print(f"error: {exc}", file=sys.stderr)
        return 1

    failed = False
    for project in args.projects:
        try:
            result = submit_build(
                opener,
                args.base_url,
                project,
                args.branch,
                args.deploy_env,
                args.operation_type,
                crumb_field,
                crumb,
                headers,
                args.timeout,
            )
        except BuildRequestError as exc:
            failed = True
            result = {
                "status": "failed",
                "project": project,
                "branch": args.branch,
                "deploy_env": args.deploy_env,
                "operation_type": args.operation_type,
                "error": str(exc),
            }
        print(json.dumps(result, ensure_ascii=False))
    return 1 if failed else 0


if __name__ == "__main__":
    raise SystemExit(main())
