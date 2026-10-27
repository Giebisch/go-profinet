#!/usr/bin/env bash
# Prints the release notes of one version from RELEASE.md: the lines after
# the heading "## <tag>" up to the next line that is exactly "---". This is
# the format of every otfabric repository and the rule the shared release
# workflow (otfabric/.github, go-package-release.yml) applies: without
# such a section the release body is a commit log.
#
# Usage: release-notes.sh <tag>     e.g. release-notes.sh v0.2.0
#        release-notes.sh --check   every version heading is exactly
#                                   "## vX.Y.Z" and its section is not
#                                   empty and ends with "---"
set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
FILE="${ROOT}/RELEASE.md"

if [[ "${1:-}" == "--check" ]]; then
    awk '
        /^## v/ {
            if (open) { printf "RELEASE.md:%d: %s starts before the section of %s ends with ---\n", NR, $0, head; bad = 1 }
            if ($0 !~ /^## v[0-9]+\.[0-9]+\.[0-9]+$/) { printf "RELEASE.md:%d: \"%s\" is not exactly \"## vX.Y.Z\"\n", NR, $0; bad = 1 }
            open = 1; head = $0; lines = 0; next
        }
        open && /^---$/ { if (!lines) { printf "RELEASE.md: %s has no notes\n", head; bad = 1 } open = 0; n++; next }
        open && NF { lines++ }
        END {
            if (open) { printf "RELEASE.md: the section of %s does not end with ---\n", head; bad = 1 }
            if (!n) { print "RELEASE.md: no version section"; bad = 1 }
            exit bad
        }
    ' "${FILE}"
    exit
fi

tag="${1:?usage: release-notes.sh <tag> | --check}"
[[ "${tag}" =~ ^v[0-9]+\.[0-9]+\.[0-9]+$ ]] || { echo "release-notes: '${tag}' is not vX.Y.Z" >&2; exit 2; }
notes=$(awk -v target="## ${tag}" '
    $0 == target { found = 1; next }
    found && /^---$/ { closed = 1; exit }
    found { print }
    END { if (found && !closed) exit 3 }
' "${FILE}") || { echo "release-notes: the section ## ${tag} in RELEASE.md does not end with ---" >&2; exit 1; }
if [[ -z "${notes//[[:space:]]/}" ]]; then
    echo "release-notes: RELEASE.md has no notes under ## ${tag}" >&2
    exit 1
fi
printf '%s\n' "${notes}"
