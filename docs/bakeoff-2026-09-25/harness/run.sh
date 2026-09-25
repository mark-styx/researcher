#!/bin/bash
# run.sh <A|B|C> <t1|t2|t3>: one bake-off run, timed, with per-call cost logging.
set -u
B=$(cd "$(dirname "$0")" && pwd)
arm=$1 t=$2
export PATH="$B/bin:$PATH" BAKEOFF_LOG="$B/logs/calls.jsonl" BAKEOFF_ARM=$arm BAKEOFF_TOPIC=$t
start=$(date +%s)
case $arm in
  A) (cd "$B/book-$t" && RESEARCHGUY_CONFIG_DIR="$B/cfg-a" BAKEOFF_DENY_LOCAL=1 bookworm research) > "$B/logs/A-$t.log" 2>&1; rc=$?
     cp "$B"/book-$t/research/web/001-researcher-1/*.md "$B/out/A-$t.md" 2>/dev/null ;;
  B|C) cfg=$(echo "$arm" | tr BC bc)
     RESEARCHGUY_CONFIG_DIR="$B/cfg-$cfg" researchguy dive "$(cat "$B/$t.md")" \
       --backend hybrid --mode inquiry --branches 5 --no-research \
       --out "$B/out/$arm-$t.md" --json > "$B/out/$arm-$t.json" 2> "$B/logs/$arm-$t.log"; rc=$? ;;
esac
end=$(date +%s)
printf "%s\t%s\t%s\t%s\t%s\n" "$arm" "$t" "$rc" "$((end-start))" "$(date -r $start +%H:%M:%S)" >> "$B/logs/times.tsv"
