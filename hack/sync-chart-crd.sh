#!/bin/sh
# Chart CRDs follow controller-gen; stale templates must fail instead of shipping.
set -eu
exec python3 - "$@" <<'PYCODE'
import pathlib
import sys

if sys.argv[1:] not in ([], ["--check"]):
    sys.exit("usage: hack/sync-chart-crd.sh [--check]")
check = sys.argv[1:] == ["--check"]
sources = sorted(pathlib.Path("config/crd/bases").glob("*.yaml"))
if len(sources) != 5:
    sys.exit("expected exactly five generated API CRDs")
target = pathlib.Path("charts/pxc-anonymizer/templates")
expected = set()
failed = False
for source in sources:
    name = source.stem.split("_", 1)[1]
    destination = target / ("crd-" + name + ".yaml")
    expected.add(destination)
    lines = source.read_text().splitlines()
    matches = [i for i, line in enumerate(lines) if "controller-gen.kubebuilder.io/version:" in line]
    if len(matches) != 1:
        sys.exit("missing unique controller-gen annotation: " + str(source))
    i = matches[0] + 1
    lines[i:i] = [
        "    {{- if .Values.crds.keep }}",
        "    helm.sh/resource-policy: keep",
        "    {{- end }}",
    ]
    rendered = "{{- if .Values.crds.install }}\n" + "\n".join(lines) + "\n{{- end }}\n"
    if check:
        if not destination.exists() or destination.read_text() != rendered:
            print("chart CRD drift: " + str(destination), file=sys.stderr)
            failed = True
    else:
        destination.write_text(rendered)
for stale in set(target.glob("crd-*.yaml")) - expected:
    print("unexpected chart CRD template: " + str(stale), file=sys.stderr)
    failed = True
if failed:
    sys.exit(1)
print("chart CRDs are current" if check else "chart CRDs regenerated: 5")
PYCODE
