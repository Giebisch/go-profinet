#!/bin/sh
# Prints the statement coverage of each package of a cover profile as a
# Markdown table: scripts/coverage-by-package.sh coverage.out
set -eu
profile=${1:-coverage.out}
echo "| Package | Coverage |"
echo "|---------|----------|"
awk 'NR > 1 {
	split($1, f, ":"); file = f[1]; pkg = file; sub(/\/[^\/]*$/, "", pkg)
	key = $1
	if (!(key in seen)) { seen[key] = 1; total[pkg] += $2; if ($3 > 0) covered[pkg] += $2 }
	else if ($3 > 0 && !(key in hit)) { covered[pkg] += $2 }
	if ($3 > 0) hit[key] = 1
} END {
	for (p in total) { all += total[p]; cov += covered[p]; printf "| %s | %.1f%% |\n", p, 100 * covered[p] / total[p] | "sort" }
	close("sort")
	printf "| **total** | **%.1f%%** |\n", 100 * cov / all
}' "$profile"
