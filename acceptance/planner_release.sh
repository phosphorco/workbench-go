#!/usr/bin/env bash
# Exercise the installed executable and its private runtime, never go run.
set -euo pipefail
workbench="$1"
fixture="$2"
mkdir -p "$fixture"
cd "$fixture"
cat > release.plan.pkl <<'PKL'
amends "workbench:plan"
meta { title = "Installed planner"; goal = "Produce a verified artifact and review it." }
local produce = new Action {
  id = "produce"
  owner = "builder"
  outcome = "Write a < b & c verbatim."
  grant { "artifact.txt" }
  oracle = module.mechanical(#"test "$(cat artifact.txt)" = 'a < b & c'"#)
}
local review = new Guard {
  id = "review"
  owner = "reviewer"
  outcome = "Review the verified artifact."
  observes { "artifact.txt" }
  needs { module.produced(produce); module.verified(produce) }
  oracle = module.adjudicated("The artifact contains the requested literal text.", "reviewer")
}
nodes { produce; review }
PKL
PATH=/nonexistent "$workbench" plan check release.plan.pkl
PATH=/nonexistent "$workbench" plan tick release.plan.pkl > initial.agent
grep -F 'a < b & c' initial.agent
! grep -Fq '&lt;' initial.agent
test ! -e release.ledger.jsonl
test ! -e release.ledger.jsonl.lock
"$workbench" plan tick release.plan.pkl --format json > initial.json
python3 - <<'PY'
import json
r = json.load(open('initial.json'))
assert r['valid'] and [n['id'] for n in r['ready']] == ['produce'], r
assert [n['id'] for n in r['blocked']] == ['review'], r
PY
if "$workbench" plan verify release.plan.pkl --node produce --by builder; then
  echo 'absent artifact unexpectedly passed verification' >&2
  exit 1
fi
printf 'a < b & c\n' > artifact.txt
"$workbench" plan land release.plan.pkl --node produce
"$workbench" plan verify release.plan.pkl --node produce --by builder
"$workbench" plan tick release.plan.pkl --format json > verified.json
python3 - <<'PY'
import json
r = json.load(open('verified.json'))
assert [n['id'] for n in r['ready']] == ['review'] and not r['blocked'], r
PY
"$workbench" plan adjudicate release.plan.pkl --node review --by reviewer --result pass --reason 'Literal text inspected.'
"$workbench" plan recall release.plan.pkl --format json > completed.json
"$workbench" plan export release.plan.pkl > exported.json
python3 - <<'PY'
import json
r = json.load(open('completed.json'))
assert r['counts']['complete'] == 2 and not r['ready'] and not r['blocked'], r
assert r == json.load(open('exported.json'))
events = [json.loads(line) for line in open('release.ledger.jsonl')]
assert any(e.get('result') == 'fail' for e in events), events
assert any(e.get('result') == 'pass' for e in events), events
PY
"$workbench" self skills export workbench-plan --skills-dir .agents/skills
"$workbench" self skills export workbench --skills-dir .agents/skills
"$workbench" skills check
"$workbench" plan schema > Plan.pkl
test -s .agents/skills/workbench-plan/references/authoring.md
printf 'Installed planner acceptance passed: %s\n' "$fixture"
