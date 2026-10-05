#!/bin/sh
# Prints what CHANGELOG.md says about one version: the lines under `## [VERSION]`, up to the next
# version. It fails, saying why, when the version has no section or the section is empty, so that a
# release is never published without saying what changed.
#
#   scripts/release-notes.sh 0.2.0 [CHANGELOG.md]
set -eu
version="${1:?the version, like 0.2.0}"
file="${2:-CHANGELOG.md}"
notes="$(awk -v heading="## [$version]" '
  index($0, heading) == 1 { inside = 1; next }
  inside && /^## \[/ { exit }
  inside && /^\[[^]]*\]: / { exit }
  inside { print }
' "$file" | sed -e '/./,$!d')"
if [ -z "$(printf '%s' "$notes" | tr -d '[:space:]')" ]; then
  echo "$file has no section for $version, or it is empty: write what changed under ## [$version] - DATE" >&2
  exit 1
fi
printf '%s\n' "$notes"
