#!/usr/bin/env python3
# Copyright 2026 The pxc-anonymizer Authors.
# SPDX-License-Identifier: Apache-2.0
"""Milestone-gate smoke check for the BackupPointer publication path.

Creates exactly one BackupPointer, waits until it reports Ready and Fresh for
the observed generation, verifies the selected backup object and the published
public pointer document, then deletes only the BackupPointer it created.
"""

import argparse
import json
import math
import shlex
import subprocess
import sys
import time
import urllib.error
import urllib.parse
import urllib.request
import uuid

API_GROUP = "pxc-anonymizer.io"
API_VERSION = "v1alpha1"
POINTER_API_VERSION = f"{API_GROUP}/{API_VERSION}"
POINTER_KIND = "BackupPointer"
POINTER_RESOURCE = f"backuppointers.{API_GROUP}"
BACKUP_RESOURCE = "perconaxtradbclusterbackups.pxc.percona.com"
BACKUP_API_VERSION = "pxc.percona.com/v1"
BACKUP_KIND = "PerconaXtraDBClusterBackup"
BACKUP_STATE = "Succeeded"
SCHEMA_VERSION = 2
MAX_BODY_BYTES = 1 << 20
POLL_INTERVAL = 3.0
CLEANUP_BUDGET = 30.0
EVIDENCE_CHARS = 400


class SmokeError(Exception):
    """Smoke failure carrying operator-facing evidence."""


class Deadline:
    """Monotonic budget shared by every subprocess and HTTP request."""

    def __init__(self, seconds, label):
        self._end = time.monotonic() + float(seconds)
        self._label = label

    def left(self):
        return self._end - time.monotonic()

    def require(self, what):
        remaining = self.left()
        if remaining <= 0.0:
            raise SmokeError(f"{self._label} budget exhausted before {what}")
        return remaining


class NoRedirects(urllib.request.HTTPRedirectHandler):
    """Refuse every redirect so an unvalidated URL is never requested."""

    def redirect_request(self, req, fp, code, msg, headers, newurl):
        raise SmokeError(
            f"public pointer GET answered with redirect HTTP {code}; "
            "refusing to follow it"
        )


def condense(text):
    return " ".join(str(text).split())[:EVIDENCE_CHARS]


def dig(container, path):
    current = container
    for part in path.split("."):
        if not isinstance(current, dict):
            return None
        current = current.get(part)
    return current


def require_str(container, path, where):
    value = dig(container, path)
    if not isinstance(value, str) or not value.strip():
        raise SmokeError(f"{where} is missing a non-empty string at {path}")
    return value


def is_int(value):
    return isinstance(value, int) and not isinstance(value, bool)


def positive_seconds(value):
    try:
        parsed = float(value)
    except (TypeError, ValueError):
        raise argparse.ArgumentTypeError("timeout must be a number of seconds")
    if not math.isfinite(parsed) or parsed <= 0.0:
        raise argparse.ArgumentTypeError("timeout must be a positive number of seconds")
    return parsed


def require_flag(value, flag):
    if not isinstance(value, str) or not value.strip():
        raise SmokeError(f"{flag} must be a non-empty value")
    return value.strip()


def validate_http_url(value, flag):
    """Validate an http(s) URL before it is used or embedded anywhere."""
    if "?" in value or "#" in value:
        raise SmokeError(f"{flag} must not contain a query or fragment delimiter")
    try:
        parsed = urllib.parse.urlsplit(value)
        host = parsed.hostname
        user, password = parsed.username, parsed.password
    except ValueError as exc:
        raise SmokeError(f"{flag} is not a parsable URL: {condense(exc)}") from exc
    if parsed.scheme not in ("http", "https"):
        raise SmokeError(f"{flag} must use the http or https scheme")
    if not host:
        raise SmokeError(f"{flag} must include a hostname")
    if user or password or "@" in parsed.netloc:
        raise SmokeError(f"{flag} must not embed credentials")
    if parsed.query or parsed.fragment:
        raise SmokeError(f"{flag} must not carry a query or fragment")
    return parsed


def build_public_url(base, key):
    path = base.path if base.path.endswith("/") else base.path + "/"
    path += urllib.parse.quote(key, safe="/")
    return urllib.parse.urlunsplit((base.scheme, base.netloc, path, "", ""))


def kubectl(arguments, options, deadline, what, stdin=None):
    """Run kubectl with the supplied context and namespace always explicit."""
    command = [
        "kubectl",
        "--context",
        options.context,
        "--namespace",
        options.namespace,
    ] + list(arguments)
    remaining = deadline.require(what)
    printable = shlex.join(command)
    try:
        done = subprocess.run(
            command,
            input=stdin,
            capture_output=True,
            text=True,
            timeout=remaining,
            check=False,
        )
    except FileNotFoundError as exc:
        raise SmokeError(f"{what}: kubectl executable not found") from exc
    except subprocess.TimeoutExpired as exc:
        raise SmokeError(
            f"{what}: kubectl exceeded its {remaining:.1f}s bound: {printable}"
        ) from exc
    if done.returncode != 0:
        detail = condense(done.stderr or "no stderr output")
        raise SmokeError(
            f"{what}: kubectl exited {done.returncode}: {printable}: {detail}"
        )
    return done.stdout


def kubectl_json(arguments, options, deadline, what, stdin=None):
    output = kubectl(arguments, options, deadline, what, stdin=stdin)
    try:
        document = json.loads(output)
    except json.JSONDecodeError as exc:
        raise SmokeError(f"{what}: kubectl did not return valid JSON") from exc
    if not isinstance(document, dict):
        raise SmokeError(f"{what}: kubectl did not return a JSON object")
    return document


def manifest_for(options, name, key):
    object_storage = {
        "bucket": options.bucket,
        "endpointURL": options.endpoint_url,
        "credentialsSecretRef": {"name": options.credentials_secret},
        "region": options.region,
    }
    if options.path_style is not None:
        object_storage["pathStyle"] = options.path_style
    return {
        "apiVersion": POINTER_API_VERSION,
        "kind": POINTER_KIND,
        "metadata": {"name": name, "namespace": options.namespace},
        "spec": {
            "source": {"pxcCluster": options.cluster},
            "target": {
                "objectStorage": object_storage,
                "key": key,
            },
        },
    }


def create_pointer(options, deadline, ownership, name, key):
    try:
        created = kubectl_json(
            ["create", "-f", "-", "-o", "json"],
            options,
            deadline,
            f"create {POINTER_KIND} {name}",
            stdin=json.dumps(manifest_for(options, name, key)),
        )
    except (SmokeError, KeyboardInterrupt) as exc:
        raise SmokeError(
            f"creation did not confirm ownership of {POINTER_KIND} {name}; "
            f"it may remain and no unsafe deletion was attempted: {exc}"
        ) from exc
    try:
        where = "create response"
        api_version = require_str(created, "apiVersion", where)
        kind = require_str(created, "kind", where)
        got_name = require_str(created, "metadata.name", where)
        got_namespace = require_str(created, "metadata.namespace", where)
        got_uid = require_str(created, "metadata.uid", where)
        if api_version != POINTER_API_VERSION or kind != POINTER_KIND:
            raise SmokeError(f"identity is {api_version}/{kind}")
        if got_name != name:
            raise SmokeError(f"name is {got_name}, requested {name}")
        if got_namespace != options.namespace:
            raise SmokeError(
                f"namespace is {got_namespace}, requested {options.namespace}"
            )
    except SmokeError as exc:
        raise SmokeError(
            f"{POINTER_KIND} {name} was created but its ownership could not be "
            f"confirmed ({exc}); no deletion was attempted, so the object may "
            "remain and must be inspected by hand"
        ) from exc
    ownership["name"] = got_name
    ownership["uid"] = got_uid


def condition_status(conditions, wanted):
    if not isinstance(conditions, list):
        return ""
    for entry in conditions:
        if isinstance(entry, dict) and entry.get("type") == wanted:
            status = entry.get("status")
            return status if isinstance(status, str) else ""
    return ""


def readiness_gap(pointer):
    """Empty string when settled, otherwise the pending reason."""
    generation = dig(pointer, "metadata.generation")
    observed = dig(pointer, "status.observedGeneration")
    if not is_int(generation):
        return "metadata.generation is not reported as an integer"
    if not is_int(observed):
        return "status.observedGeneration is not reported yet"
    if observed != generation:
        return f"status.observedGeneration {observed} lags generation {generation}"
    ready = condition_status(dig(pointer, "status.conditions"), "Ready")
    fresh = condition_status(dig(pointer, "status.conditions"), "Fresh")
    if ready != "True" or fresh != "True":
        return f"Ready={ready or '<unset>'} Fresh={fresh or '<unset>'}"
    return ""


def wait_for_pointer(options, deadline, ownership):
    name = ownership["name"]
    reason = "no status observed yet"
    while True:
        if deadline.left() <= 0.0:
            raise SmokeError(
                f"{POINTER_KIND} {name} did not become Ready and Fresh in time: {reason}"
            )
        pointer = kubectl_json(
            ["get", POINTER_RESOURCE, name, "-o", "json"],
            options,
            deadline,
            f"read {POINTER_KIND} {name}",
        )
        where = f"{POINTER_KIND} {name}"
        if (
            require_str(pointer, "metadata.name", where) != name
            or require_str(pointer, "metadata.namespace", where) != options.namespace
            or require_str(pointer, "metadata.uid", where) != ownership["uid"]
        ):
            raise SmokeError(
                f"read-back of {where} does not match the created object identity"
            )
        reason = readiness_gap(pointer)
        if not reason:
            return pointer
        time.sleep(min(POLL_INTERVAL, max(0.0, deadline.left())))


def verify_backup(options, deadline, backup_name, destination):
    backup = kubectl_json(
        ["get", BACKUP_RESOURCE, backup_name, "-o", "json"],
        options,
        deadline,
        f"read backup {backup_name}",
    )
    where = f"backup {backup_name}"
    api_version = require_str(backup, "apiVersion", where)
    kind = require_str(backup, "kind", where)
    if api_version != BACKUP_API_VERSION or kind != BACKUP_KIND:
        raise SmokeError(f"{where} identity is {api_version}/{kind}, not expected")
    cluster = require_str(backup, "spec.pxcCluster", where)
    if cluster != options.cluster:
        raise SmokeError(f"{where} belongs to cluster {cluster}, expected {options.cluster}")
    state = require_str(backup, "status.state", where)
    if state != BACKUP_STATE:
        raise SmokeError(f"{where} is in state {state}, expected {BACKUP_STATE}")
    if require_str(backup, "status.destination", where) != destination:
        raise SmokeError(f"{where} destination differs from the pointer destination")


def fetch_public_document(url, deadline):
    remaining = deadline.require("public pointer fetch")
    opener = urllib.request.build_opener(NoRedirects)
    request = urllib.request.Request(
        url, method="GET", headers={"Accept": "application/json"}
    )
    try:
        with opener.open(request, timeout=remaining) as response:
            status = getattr(response, "status", None)
            if not is_int(status) or not 200 <= status < 300:
                raise SmokeError(f"public pointer GET returned HTTP status {status}")
            media = response.headers.get_content_type()
            if media != "application/json":
                raise SmokeError(
                    f"public pointer media type is {media or '<empty>'}, "
                    "expected application/json"
                )
            payload = response.read(MAX_BODY_BYTES + 1)
    except urllib.error.HTTPError as exc:
        raise SmokeError(f"public pointer GET failed with HTTP status {exc.code}") from exc
    except urllib.error.URLError as exc:
        raise SmokeError(f"public pointer GET failed: {condense(exc.reason)}") from exc
    except TimeoutError as exc:
        raise SmokeError(
            f"public pointer GET exceeded its {remaining:.1f}s bound"
        ) from exc
    if len(payload) > MAX_BODY_BYTES:
        raise SmokeError(f"public pointer document exceeds {MAX_BODY_BYTES} bytes")
    try:
        document = json.loads(payload.decode("utf-8"))
    except (UnicodeDecodeError, json.JSONDecodeError) as exc:
        raise SmokeError("public pointer document is not valid JSON") from exc
    if not isinstance(document, dict):
        raise SmokeError("public pointer document is not a JSON object")
    return document


def verify_public_document(document, backup_name, destination):
    where = "public pointer document"
    schema = document.get("schemaVersion")
    if not is_int(schema):
        raise SmokeError(f"{where} schemaVersion is not an integer")
    if schema != SCHEMA_VERSION:
        raise SmokeError(f"{where} schemaVersion is {schema}, expected {SCHEMA_VERSION}")
    name = require_str(document, "name", where)
    if name != backup_name:
        raise SmokeError(f"{where} name {name} is not the selected backup {backup_name}")
    if require_str(document, "destination", where) != destination:
        raise SmokeError(f"{where} destination differs from the verified destination")


def run_checks(options, ownership, name, key, public_url):
    deadline = Deadline(options.timeout, "smoke")
    create_pointer(options, deadline, ownership, name, key)
    print(f"PASS: created {POINTER_KIND} {name} in namespace {options.namespace}")

    pointer = wait_for_pointer(options, deadline, ownership)
    print(f"PASS: {name} reports Ready=True and Fresh=True for the observed generation")

    where = f"{POINTER_KIND} {name} status"
    backup_name = require_str(pointer, "status.current.backupName", where)
    destination = require_str(pointer, "status.current.destination", where)
    print(f"PASS: {name} selected backup {backup_name}")

    verify_backup(options, deadline, backup_name, destination)
    print(
        f"PASS: backup {backup_name} is {BACKUP_STATE} for cluster {options.cluster} "
        "with a matching destination"
    )

    verify_public_document(
        fetch_public_document(public_url, deadline), backup_name, destination
    )
    print(
        f"PASS: public pointer at key {key} is schemaVersion {SCHEMA_VERSION} "
        "and consistent with the selected backup"
    )


def delete_pointer(options, ownership):
    name = ownership["name"]
    raw_path = "/apis/{0}/{1}/namespaces/{2}/backuppointers/{3}".format(
        API_GROUP,
        API_VERSION,
        urllib.parse.quote(options.namespace, safe=""),
        urllib.parse.quote(name, safe=""),
    )
    body = json.dumps(
        {
            "apiVersion": "v1",
            "kind": "DeleteOptions",
            "preconditions": {"uid": ownership["uid"]},
        }
    )
    deadline = Deadline(CLEANUP_BUDGET, "cleanup")
    kubectl(
        ["delete", "--raw", raw_path, "-f", "-"],
        options,
        deadline,
        f"delete {POINTER_KIND} {name}",
        stdin=body,
    )
    kubectl(
        ["wait", "--for=delete", f"{POINTER_RESOURCE}/{name}",
         f"--timeout={int(CLEANUP_BUDGET)}s"],
        options,
        deadline,
        f"wait for deletion of {POINTER_KIND} {name}",
    )


def cleanup(options, ownership):
    """Delete only the created BackupPointer; return a failure string or None."""
    if not ownership.get("name") or not ownership.get("uid"):
        return None
    name = ownership["name"]
    try:
        delete_pointer(options, ownership)
    except KeyboardInterrupt:
        return f"interrupted; {POINTER_KIND} {name} may still exist"
    except SmokeError as exc:
        return str(exc)
    except Exception as exc:  # report, never mask
        return f"unexpected error: {type(exc).__name__}: {condense(exc)}"
    print(f"PASS: deleted {POINTER_KIND} {name} with a UID precondition")
    return None


def parse_arguments(argv):
    parser = argparse.ArgumentParser(
        description=(
            "Smoke check for the BackupPointer publication path. The --context flag "
            "is mandatory: this smoke never falls back to the current context and "
            "never modifies kubectl configuration. Run it only at a full milestone "
            "gate against an already prepared environment."
        )
    )
    parser.add_argument(
        "--context",
        required=True,
        help=(
            "mandatory kubectl context, passed explicitly to every kubectl call; "
            "there is no fallback to the current context and this smoke runs only "
            "at a full milestone gate"
        ),
    )
    parser.add_argument(
        "--namespace",
        required=True,
        help="namespace passed explicitly to every kubectl call; never created or deleted",
    )
    parser.add_argument(
        "--cluster",
        required=True,
        help="value for spec.source.pxcCluster and the expected backup spec.pxcCluster",
    )
    parser.add_argument(
        "--bucket",
        required=True,
        help="bucket recorded in spec.target.objectStorage.bucket",
    )
    parser.add_argument(
        "--credentials-secret",
        required=True,
        help=(
            "name for spec.target.objectStorage.credentialsSecretRef.name; "
            "Secret values are never read or printed"
        ),
    )
    parser.add_argument(
        "--endpoint-url",
        required=True,
        help="http(s) endpoint recorded in spec.target.objectStorage.endpointURL",
    )
    parser.add_argument(
        "--public-base-url",
        required=True,
        help="http(s) base URL joined with the generated key for the unauthenticated GET",
    )
    parser.add_argument(
        "--region",
        default="auto",
        help="S3 signing region recorded in objectStorage.region (default: auto)",
    )
    parser.add_argument(
        "--path-style",
        action="store_true",
        default=None,
        help="set objectStorage.pathStyle=true; omit to keep automatic addressing",
    )
    parser.add_argument(
        "--timeout",
        type=positive_seconds,
        default=180.0,
        help="overall positive budget in seconds for all checks (default: 180)",
    )
    return parser.parse_args(argv)


def normalize(options):
    options.context = require_flag(options.context, "--context")
    options.namespace = require_flag(options.namespace, "--namespace")
    options.cluster = require_flag(options.cluster, "--cluster")
    options.bucket = require_flag(options.bucket, "--bucket")
    options.credentials_secret = require_flag(
        options.credentials_secret, "--credentials-secret"
    )
    options.endpoint_url = require_flag(options.endpoint_url, "--endpoint-url")
    options.public_base_url = require_flag(options.public_base_url, "--public-base-url")
    options.region = require_flag(options.region, "--region")


def main(argv):
    options = parse_arguments(argv)
    ownership = {}
    failure = None
    key = None
    try:
        normalize(options)
        validate_http_url(options.endpoint_url, "--endpoint-url")
        base = validate_http_url(options.public_base_url, "--public-base-url")
        run_id = uuid.uuid4().hex
        name = f"backuppointer-smoke-{run_id[:16]}"
        key = f"smoke/{run_id}/pointer.json"
        run_checks(options, ownership, name, key, build_public_url(base, key))
    except KeyboardInterrupt:
        failure = "interrupted before all checks completed"
    except SmokeError as exc:
        failure = str(exc)
    except Exception as exc:  # cleanup must still run
        failure = f"unexpected error: {type(exc).__name__}: {condense(exc)}"
    finally:
        cleanup_failure = cleanup(options, ownership)

    if ownership.get("name") and key:
        print(
            f"NOTE: the unique public pointer object may remain at key {key} in "
            f"bucket {options.bucket}; it is left as evidence and was not deleted"
        )
    if failure:
        print(f"FAIL: {failure}", file=sys.stderr)
    if cleanup_failure:
        print(f"FAIL: cleanup: {cleanup_failure}", file=sys.stderr)
    if failure or cleanup_failure:
        return 1
    print(f"PASS: {POINTER_KIND} smoke completed and cleanup succeeded")
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
