#!/bin/sh
# Default-only rendering misses disabled RBAC and custom ServiceAccount errors.
# Source-rule drift is checked separately by sync-chart-rbac.sh.
set -eu

chart=charts/pxc-anonymizer
tpl=templates/metrics-auth-rbac.yaml
fail=0
out=""

# Sets $out to the rendered template. Helm exits nonzero with "could not find
# template" when the template renders empty, which is exactly the gated-off
# case; any other failure is a real error and stops the script.
render() {
  if out=$(helm template rel "$chart" --namespace ns --show-only "$tpl" "$@" 2>&1); then
    return 0
  fi
  case $out in
  *"could not find template"*)
    out=""
    return 0
    ;;
  *)
    echo "helm template failed for '${*:-defaults}':" >&2
    printf '%s\n' "$out" >&2
    exit 1
    ;;
  esac
}

expect_absent() {
  render "$@"
  if [ -n "$out" ]; then
    echo "FAIL: $tpl rendered with '$*' but must not" >&2
    fail=1
    return 0
  fi
  echo "ok: nothing from $tpl with '$*'"
}

expect_match() {
  needle=$1
  shift
  render "$@"
  if printf '%s\n' "$out" | grep -q "$needle"; then
    echo "ok: $tpl has '$needle' with '${*:-defaults}'"
    return 0
  fi
  echo "FAIL: $tpl lacks '$needle' with '${*:-defaults}':" >&2
  printf '%s\n' "$out" >&2
  fail=1
}

# Defaults (rbac.create=true, metrics.enabled=true): both objects exist, and
# the binding names the ServiceAccount the Deployment actually runs as.
expect_match '^kind: ClusterRole$'
expect_match '^kind: ClusterRoleBinding$'
expect_match '^  name: rel-pxc-anonymizer-metrics-auth$'
expect_match '^    name: rel-pxc-anonymizer$'
expect_match '^    namespace: ns$'

# No metrics endpoint means no authn/authz filter, so the permission is dead.
expect_absent --set metrics.enabled=false

# rbac.create=false means the user brings their own RBAC.
expect_absent --set rbac.create=false

# A ServiceAccount the chart does not create must still be the bound subject.
expect_match '^    name: custom$' --set serviceAccount.create=false --set serviceAccount.name=custom

# The bindings are the last hand-written RBAC in the chart: sync-chart-rbac.sh
# generates the rules, but nothing generates what binds them to the
# ServiceAccount. A rule nobody is bound to grants nothing, so losing a binding
# takes every permission away at once and leaves the rules looking correct.
tpl=templates/clusterrolebinding.yaml
expect_match '^  name: rel-pxc-anonymizer-manager$'
expect_match '^  kind: ClusterRole$'
expect_match '^    name: rel-pxc-anonymizer$'
expect_match '^    namespace: ns$'
expect_match '^    name: custom$' --set serviceAccount.create=false --set serviceAccount.name=custom
expect_absent --set rbac.create=false

# Leader election owns its own Role, and the Role, this binding and the
# --leader-elect flag all key on the same value, so they appear and disappear
# together.
tpl=templates/rolebinding.yaml
expect_match '^  name: rel-pxc-anonymizer-leader-election$'
expect_match '^  kind: Role$'
expect_match '^    name: rel-pxc-anonymizer$'
expect_absent --set leaderElection.enabled=false
expect_absent --set rbac.create=false

python3 - <<'PYCODE'
import json
import pathlib
import re
import subprocess
import sys

chart = "charts/pxc-anonymizer"
base = ["helm", "template", "rel", chart, "--namespace", "ns"]
manifest = subprocess.check_output(base, text=True)
sources = sorted(pathlib.Path("config/crd/bases").glob("*.yaml"))
assert len(sources) == 5, "expected the five envtest API CRDs"
crds = {("CustomResourceDefinition", p.stem.split("_", 1)[1] + ".pxc-anonymizer.io") for p in sources}
native = {
    ("Deployment", "rel-pxc-anonymizer"),
    ("ServiceAccount", "rel-pxc-anonymizer"),
    ("Service", "rel-pxc-anonymizer-metrics"),
    ("ClusterRole", "rel-pxc-anonymizer-manager"),
    ("ClusterRoleBinding", "rel-pxc-anonymizer-manager"),
    ("Role", "rel-pxc-anonymizer-leader-election"),
    ("RoleBinding", "rel-pxc-anonymizer-leader-election"),
    ("ClusterRole", "rel-pxc-anonymizer-metrics-auth"),
    ("ClusterRoleBinding", "rel-pxc-anonymizer-metrics-auth"),
}


def validate(text, expected, skipped):
    result = subprocess.run(
        ["kubeconform", "-strict", "-summary", "-verbose", "-output", "json", "-ignore-missing-schemas"],
        input=text, text=True, capture_output=True, check=False,
    )
    sys.stdout.write(result.stdout)
    sys.stderr.write(result.stderr)
    if result.returncode:
        sys.exit(result.returncode)
    report = json.loads(result.stdout)
    resources = report["resources"]
    actual = {(r["kind"], r["name"]) for r in resources}
    assert len(resources) == len(expected) and actual == expected, "rendered resource inventory differs"
    for resource in resources:
        identity = (resource["kind"], resource["name"])
        want = "statusSkipped" if identity in skipped else "statusValid"
        assert resource["status"] == want, "unexpected schema result: " + repr(resource)
    assert report["summary"] == {
        "valid": len(expected) - len(skipped), "invalid": 0, "errors": 0, "skipped": len(skipped),
    }, "unexpected schema totals"


def body(text):
    return "\n".join(line for line in text.splitlines() if line.strip()
                     and line != "---" and not line.startswith("# Source:")
                     and line != "    helm.sh/resource-policy: keep")


documents = re.split(r"(?m)^---\s*\n", manifest)
for source in sources:
    plural = source.stem.split("_", 1)[1]
    marker = "# Source: pxc-anonymizer/templates/crd-" + plural + ".yaml"
    matches = [doc for doc in documents if marker in doc.splitlines()]
    assert len(matches) == 1, "missing or duplicate rendered CRD: " + plural
    assert "    helm.sh/resource-policy: keep" in matches[0].splitlines(), "missing CRD retention"
    assert body(matches[0]) == body(source.read_text()), "chart differs from envtest CRD: " + plural
print("Chart CRDs match all five envtest input files, apart from Helm retention metadata")
validate(manifest, native | crds, crds)
disabled = subprocess.check_output(
    base + ["--set", "crds.install=false,rbac.create=false,metrics.enabled=false"], text=True,
)
validate(disabled, {("Deployment", "rel-pxc-anonymizer"), ("ServiceAccount", "rel-pxc-anonymizer")}, set())
PYCODE

if [ "$fail" -ne 0 ]; then
  echo "chart RBAC check failed" >&2
  exit 1
fi
echo "chart RBAC renders as expected"
