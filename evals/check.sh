#!/usr/bin/env bash
# Check one eval result against the case's expectations.
#   $1 case file   $2 result json
set -uo pipefail
case_file="$1"; result="$2"

# must_contain: strings that have to appear in the response
while IFS= read -r needle; do
  [ -z "$needle" ] && continue
  grep -qF -- "$needle" "$result" || { echo "   missing expected: $needle"; exit 1; }
done < <(jq -r '.must_contain[]? // empty' "$case_file")

# must_not_contain: the negative cases matter more, because they catch a
# model doing something plausible and wrong
while IFS= read -r needle; do
  [ -z "$needle" ] && continue
  if grep -qF -- "$needle" "$result"; then echo "   found forbidden: $needle"; exit 1; fi
done < <(jq -r '.must_not_contain[]? // empty' "$case_file")

# shell_check: an arbitrary command that must exit 0 (tests pass, lint clean)
cmd=$(jq -r '.shell_check // empty' "$case_file")
if [ -n "$cmd" ]; then
  bash -c "$cmd" >/dev/null 2>&1 || { echo "   shell_check failed: $cmd"; exit 1; }
fi
exit 0
