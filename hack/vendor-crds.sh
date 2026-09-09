#!/bin/sh
# Preserve upstream schemas without adding an operator Go dependency or YAML parser.
set -eu

exec python3 - "$0" "$@" <<'PYCODE'
import hashlib
import pathlib
import re
import subprocess
import sys

ROOT = pathlib.Path(sys.argv[1]).resolve().parent.parent
PXC_CHART_TAG = "pxc-operator-1.20.0"
PROVIDER_SQL_TAG = "v0.13.0"
API = "https://api.github.com/repos"
PXC_SOURCE_SHA256 = "047635e38bcc171cbd89c7238ac6fcbf814886b8296e2cf4d74c8600984250a1"
PXC_NAMES = (
    "perconaxtradbclusterbackups",
    "perconaxtradbclusterrestores",
    "perconaxtradbclusters",
)
SQL_NAMES = ("databases", "users", "grants")
# These hashes come from reviewed upstream bytes, never from the working tree.
HASHES = {
    "pxc.percona.com_perconaxtradbclusterbackups.yaml": "8e93c942260ecc5428b0268d9eb610cb2d65553ed1c4b5a3d02c3224f6b003bd",
    "pxc.percona.com_perconaxtradbclusterrestores.yaml": "0845b51d416521524d56cc5cbd874f3518341d5ef5fa49c6be856305a9b2e7ee",
    "pxc.percona.com_perconaxtradbclusters.yaml": "724731a30e3b87f0913e4b1f40f69a7eeee05226f5ac349e56576e967f463991",
    "mysql.sql.crossplane.io_databases.yaml": "21cec476e6bde4bef26a87cbf858c6964a89d2f1178c7800efa1770571dddb77",
    "mysql.sql.crossplane.io_users.yaml": "2757488442eccadc5ee28ed0b8bca844ae60ed81bf3b1520bef623dcc94ac90d",
    "mysql.sql.crossplane.io_grants.yaml": "1652e9467742db34db8e8298ae12a1281677cb8867de63cd4cb5016fc0571a7d",
}


def fetch(url, digest):
    data = subprocess.check_output([
        "curl", "--disable", "--fail", "--silent", "--show-error", "--location",
        "--proto", "=https", "--proto-redir", "=https",
        "--header", "Accept: application/vnd.github.raw+json",
        "--max-time", "30", "--max-filesize", "8388608", url,
    ])
    if hashlib.sha256(data).hexdigest() != digest:
        raise ValueError(f"upstream digest mismatch: {url}")
    return data


def documents(data):
    # Split only upstream document boundaries; schema formatting stays unchanged.
    return [doc for doc in re.split(br"(?m)^---\r?\n", data) if doc.strip()]


def identity(document):
    if re.findall(br"(?m)^apiVersion: (.+)$", document) != [b"apiextensions.k8s.io/v1"]:
        raise ValueError("expected one apiextensions.k8s.io/v1 document")
    if re.findall(br"(?m)^kind: (.+)$", document) != [b"CustomResourceDefinition"]:
        raise ValueError("expected one CustomResourceDefinition")
    names = re.findall(br"(?m)^  name: (.+)$", document)
    if len(names) != 1:
        raise ValueError("expected one CRD metadata.name")
    return names[0].decode("ascii")


def download():
    upstream = fetch(
        f"{API}/percona/percona-helm-charts/contents/charts/pxc-operator/crds/crd.yaml?ref={PXC_CHART_TAG}",
        PXC_SOURCE_SHA256,
    )
    pxc = {}
    for document in documents(upstream):
        name = identity(document)
        if name in pxc:
            raise ValueError(f"duplicate upstream CRD: {name}")
        pxc[name] = document
    if set(pxc) != {f"{name}.pxc.percona.com" for name in PXC_NAMES}:
        raise ValueError("expected exactly three pinned Percona CRDs")
    expected = {
        f"pxc.percona.com_{name}.yaml": pxc[f"{name}.pxc.percona.com"]
        for name in PXC_NAMES
    }
    for name in SQL_NAMES:
        filename = f"mysql.sql.crossplane.io_{name}.yaml"
        data = fetch(
            f"{API}/crossplane-contrib/provider-sql/contents/package/crds/{filename}?ref={PROVIDER_SQL_TAG}",
            HASHES[filename],
        )
        expected[filename] = data
    return expected


def main():
    if sys.argv[2:] not in ([], ["--check"]):
        sys.exit("usage: hack/vendor-crds.sh [--check]")
    check = sys.argv[2:] == ["--check"]
    directory = ROOT / "test" / "crds"
    present = {path.name for path in directory.iterdir() if path.suffix in (".yaml", ".yml")}
    unexpected = present - HASHES.keys()
    if unexpected:
        raise ValueError(f"unexpected CRD files (left unchanged): {', '.join(sorted(unexpected))}")
    if check and present != HASHES.keys():
        raise ValueError(f"missing CRDs: {', '.join(sorted(HASHES.keys() - present))}")
    files = {name: (directory / name).read_bytes() for name in HASHES} if check else download()
    if files.keys() != HASHES.keys():
        raise ValueError("expected exactly six pinned CRDs")
    for name, data in files.items():
        group, plural = name.removesuffix(".yaml").split("_", 1)
        parts = documents(data)
        if len(parts) != 1 or identity(parts[0]) != f"{plural}.{group}":
            raise ValueError(f"unexpected CRD identity: {name}")
        if hashlib.sha256(data).hexdigest() != HASHES[name]:
            raise ValueError(f"CRD digest mismatch: {name}")
    if not check:
        for name, data in files.items():
            (directory / name).write_bytes(data)
    verb = "Verified" if check else "Vendored"
    print(f"{verb} 6 CRDs: {PXC_CHART_TAG}, provider-sql {PROVIDER_SQL_TAG}")


try:
    main()
except (OSError, UnicodeError, ValueError, subprocess.CalledProcessError) as error:
    sys.exit(str(error))
PYCODE
