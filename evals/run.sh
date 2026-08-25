#!/usr/bin/env bash
# Run every eval case and gate on the pass rate.
#
# An eval is a real task plus the checks that define an acceptable answer.
# The suite is meant to be living: as models improve, cases that once
# discriminated stop doing so and must be retired, and every production
# incident earns a new case. A suite that never changes is measuring the
# past.
set -uo pipefail

cd "$(dirname "$0")/.."
RESULTS="evals/results"
mkdir -p "$RESULTS"
: > "$RESULTS/summary.txt"

pass=0; fail=0; total=0

shopt -s nullglob
for case_file in evals/cases/*.json; do
  total=$((total+1))
  name=$(jq -r '.name' "$case_file")
  prompt=$(jq -r '.prompt' "$case_file")
  tools=$(jq -r '.allowed_tools // "Read,Edit,Bash(make test)"' "$case_file")

  echo "── $name"
  if claude -p "$prompt" \
       --allowedTools "$tools" \
       --output-format json > "$RESULTS/$name.json" 2>"$RESULTS/$name.err"; then
    if ./evals/check.sh "$case_file" "$RESULTS/$name.json"; then
      echo "   PASS"; pass=$((pass+1))
      echo "PASS $name" >> "$RESULTS/summary.txt"
      continue
    fi
  fi
  echo "   FAIL"; fail=$((fail+1))
  echo "FAIL $name" >> "$RESULTS/summary.txt"
done

if [ "$total" -eq 0 ]; then
  # An empty suite passing vacuously is the single most dangerous outcome
  # here, because auto-merge keys off this gate. Refuse instead.
  echo "no eval cases found in evals/cases/ — refusing to report a pass" | tee -a "$RESULTS/summary.txt"
  exit 1
fi

rate=$(awk "BEGIN{printf \"%.3f\", $pass/$total}")
min=$(jq -r '.auto_merge.require_eval_pass_rate // 0.95' orion.json 2>/dev/null || echo 0.95)
mincases=$(jq -r '.auto_merge.min_eval_cases // 20' orion.json 2>/dev/null || echo 20)

{
  echo
  echo "pass rate: $pass/$total = $rate (threshold $min)"
  echo "case count: $total (minimum for auto-merge: $mincases)"
} | tee -a "$RESULTS/summary.txt"

awk "BEGIN{exit !($rate >= $min)}" || { echo "below threshold"; exit 1; }
