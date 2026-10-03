#!/usr/bin/env bash
# Runs the unit tests with coverage and enforces two thresholds on the
# library code (internal/): a minimum total, and a floor per package so a
# new untested package can't hide behind well-tested ones. cmd/ is the
# composition root and OS integration, verified by the integration tests.
set -euo pipefail

TOTAL_MIN=${TOTAL_MIN:-78}
PACKAGE_MIN=${PACKAGE_MIN:-60}
PROFILE=${PROFILE:-coverage.out}

out=$(go test -race -count=1 -covermode=atomic -coverprofile="$PROFILE" ./...)
echo "$out"

fail=0
while read -r pkg pct; do
	if awk -v p="$pct" -v m="$PACKAGE_MIN" 'BEGIN{exit !(p < m)}'; then
		echo "::error::$pkg coverage $pct% is below the per-package minimum of $PACKAGE_MIN%"
		fail=1
	fi
done < <(echo "$out" | grep "/internal/" | grep "coverage:" | sed -E 's/.*(github\.com[^[:space:]]+).*coverage: ([0-9.]+)%.*/\1 \2/')
# Packages without any test files report 0% and fail the floor above too.

grep -v "/cmd/" "$PROFILE" > "$PROFILE.internal"
total=$(go tool cover -func="$PROFILE.internal" | awk '/^total:/ {sub("%","",$3); print $3}')
rm -f "$PROFILE.internal"
echo "internal/ total coverage: $total% (minimum $TOTAL_MIN%)"
if awk -v p="$total" -v m="$TOTAL_MIN" 'BEGIN{exit !(p < m)}'; then
	echo "::error::internal/ coverage $total% is below the minimum of $TOTAL_MIN%"
	fail=1
fi
exit $fail
