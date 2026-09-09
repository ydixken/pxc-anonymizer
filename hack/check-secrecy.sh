#!/bin/sh
set -eu

exec python3 - <<'PY'
import hashlib
import os
from pathlib import Path
import re
import stat
import subprocess
import sys


def fail(reason):
    print("secrecy: " + reason, file=sys.stderr)
    raise SystemExit(1)


def git(*args):
    result = subprocess.run(["git", *args], capture_output=True, check=False)
    if result.returncode:
        fail("git input unavailable")
    return result.stdout


def read_list(path, label):
    try:
        if path.is_symlink() or not path.is_file():
            fail(label + " deny-list missing")
        return path.read_text(encoding="utf-8")
    except (OSError, UnicodeError):
        fail(label + " deny-list unreadable")


def entries(text, label):
    values = [line.strip() for line in text.splitlines()
              if line.strip() and not line.lstrip().startswith("#")]
    if not values:
        fail(label + " deny-list missing")
    return values


def digest_values(text):
    values = entries(text, "digest")
    if any(re.fullmatch(r"[0-9a-f]{64}", value) is None for value in values):
        fail("digest deny-list invalid")
    return set(values)


def check(data):
    for token in re.findall(rb"[A-Za-z0-9_]+(?:[.-][A-Za-z0-9_]+)*", data.lower()):
        for part in {token, *re.split(rb"[.-]", token)}:
            if hashlib.sha256(part).hexdigest() in digests:
                fail("forbidden content found")
    text = data.decode("utf-8", errors="replace")
    if any(pattern.search(text) for pattern in patterns):
        fail("forbidden content found")


try:
    root = Path(os.fsdecode(git("rev-parse", "--show-toplevel")).strip())
    os.chdir(root)
    digest_path = "hack/secrecy-deny.sha256"
    digests = digest_values(read_list(root / digest_path, "digest"))
    patterns = []
    if os.environ.get("CI") != "true":
        raw_patterns = entries(read_list(root / "tasks/secrecy-deny.regex", "local"), "local")
        try:
            patterns = [re.compile(pattern, re.IGNORECASE) for pattern in raw_patterns]
        except re.error:
            fail("local deny-list invalid")

    candidates = set(git("ls-files", "--cached", "--others", "--exclude-standard", "-z").split(b"\0"))
    candidates.discard(b"")
    if not candidates:
        fail("no publication candidates")

    # A clean working tree can conceal a secret that is still staged for commit.
    staged = git("ls-files", "--stage", "-z").split(b"\0")
    blobs = {}
    for entry in staged:
        if not entry:
            continue
        metadata, name = entry.split(b"\t", 1)
        mode, object_id, _ = metadata.split()
        if mode == b"160000":
            fail("unsupported publication candidate")
        if object_id not in blobs:
            blobs[object_id] = git("cat-file", "blob", object_id.decode("ascii"))
        if name == os.fsencode(digest_path):
            digests.update(digest_values(blobs[object_id].decode("utf-8")))
    for data in blobs.values():
        check(data)

    for name in sorted(candidates):
        path = Path(os.fsdecode(name))
        check(name)
        check(os.fsencode(path.with_suffix("")))
        try:
            mode = path.lstat().st_mode
            if stat.S_ISLNK(mode):
                data = os.fsencode(os.readlink(path))
            elif stat.S_ISREG(mode):
                data = path.read_bytes()
            else:
                fail("candidate unreadable")
        except OSError:
            fail("candidate unreadable")
        check(data)
except (OSError, UnicodeError, ValueError):
    fail("input processing failed")

print("secrecy: PASS")
PY
