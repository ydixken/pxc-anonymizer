#!/bin/sh
set -eu

cd "$(dirname "$0")/.."
if [ "$#" -ne 0 ]; then
  echo "Usage: hack/check-docs-links.sh" >&2
  exit 2
fi
for file in mkdocs.yml docs/index.md docs/reference/api.md; do
  if [ ! -s "$file" ]; then
    echo "Required documentation input is missing or empty: $file" >&2
    exit 1
  fi
done
# MkDocs resolves Markdown links and anchors with the same parser used by the site.
exec mkdocs build --strict
