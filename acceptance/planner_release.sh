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
test -s .agents/skills/workbench-plan/references/planning-tactics.md
for example in discovery delivery revised; do
  "$workbench" plan check ".agents/skills/workbench-plan/examples/bulk-export/$example.plan.pkl" --format json > "$example.json"
done
python3 - <<'PY'
import json
reports = {name: json.load(open(name + '.json')) for name in ('discovery', 'delivery', 'revised')}
for report in reports.values():
    assert report['valid'] and [n['id'] for n in report['ready']] == ['probe-build'], report
def nodes(report):
    return {n['id']: n for category in ('ready', 'blocked') for n in report[category]}
delivery, revised = nodes(reports['delivery']), nodes(reports['revised'])
assert 'verified(compatibility)' in delivery['format']['needs']
assert 'verified(contract)' in delivery['reader']['needs']
assert not any('writer' in need for need in delivery['reader']['needs'])
assert delivery['writer-red']['oracle']['once']
assert {'produced(writer-red)', 'verified(writer-red)'} <= set(delivery['writer']['needs'])
assert {'produced(writer)', 'verified(writer)', 'produced(reader)', 'verified(reader)'} <= set(delivery['roundtrip']['needs'])
assert revised['reader'] == delivery['reader'] and revised['contract'] == delivery['contract']
assert set(revised) - set(delivery) == {'streaming-tests'}
for name in ('writer', 'roundtrip', 'release-review'):
    assert revised[name]['oracle'] != delivery[name]['oracle']
assert set(delivery['writer']['needs']) <= set(revised['writer']['needs'])
assert {'produced(streaming-tests)', 'verified(streaming-tests)'} <= set(revised['writer']['needs'])
PY
printf 'Installed planner acceptance passed: %s\n' "$fixture"
