package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"
	"testing/quick"
	"time"
)

const actorPlan = `amends "workbench:plan"
import "threads.pkl"
import "people.pkl"
meta { title = "Coordination"; goal = "Track work without inventing proof." }
actors { kinds { ["thread"] = threads.thread; ["user"] = people.user; ["external"] { pattern = #"service/[a-z]+"# } } }
local api = new Action { id = "api"; owner = "builder"; grant { "api/**" }; oracle = module.mechanical("true") }
local review = new Guard { id = "review"; owner = "reviewer"; needs { module.verified(api) }; oracle = module.adjudicated("Inspect the API.", "user:alex@example.com") }
local decision = new Selector { id = "decision"; owner = "user:alex@example.com"; options { "ship"; "wait" }; needs { module.verified(review) } }
nodes { api; review; decision }
`

func actorFixture(t *testing.T) (string, string) {
	t.Helper()
	root := t.TempDir()
	writePlanFixture(t, root, "threads.pkl", `import "workbench:plan" as Plan
thread = new Plan.ActorKind {
 pattern = #"thr_[A-Za-z0-9_]+"#
 infer { env = "WORKBENCH_TEST_THREAD" }
 aliases { new { pattern = #"@thread:(thr_[A-Za-z0-9_]+)"#; replacement = "$1" } }
}`)
	writePlanFixture(t, root, "people.pkl", `import "workbench:plan" as Plan
user = new Plan.ActorKind {
 pattern = #"[^@<>\s]+@[^@<>\s]+\.[^@<>\s]+"#
 infer { env = "WORKBENCH_TEST_USER" }
 aliases { new { pattern = #"<([^<>\s]+@[^<>\s]+)>"#; replacement = "$1" } }
}`)
	t.Setenv("WORKBENCH_TEST_THREAD", "thr_builder")
	t.Setenv("WORKBENCH_TEST_USER", "")
	return root, writePlanFixture(t, root, "coordination.plan.pkl", actorPlan)
}

func TestPlanCoordinationEndToEnd(t *testing.T) {
	root, path := actorFixture(t)
	binary := filepath.Join(t.TempDir(), "workbench")
	if output, err := exec.Command("go", "build", "-o", binary, ".").CombinedOutput(); err != nil {
		t.Fatalf("build CLI: %v\n%s", err, output)
	}
	call := func(verb string, flags ...string) map[string]any {
		t.Helper()
		command := exec.Command(binary, append([]string{"plan", verb, path, "--format", "json"}, flags...)...)
		command.Dir = root
		var diagnostics bytes.Buffer
		command.Stderr = &diagnostics
		output, err := command.Output()
		if err != nil {
			t.Fatalf("%s %v: %v\n%s\n%s", verb, flags, err, output, &diagnostics)
		}
		var result map[string]any
		if err := json.Unmarshal(output, &result); err != nil {
			t.Fatal(err)
		}
		return result
	}
	getWork := func() map[string]any {
		t.Helper()
		report := call("tick")
		return report["work"].([]any)[0].(map[string]any)["work"].(map[string]any)
	}
	event := call("update", "--node", "api", "--assignee", "@me", "--status", "in-progress")["event"].(map[string]any)
	if !reflect.DeepEqual(event["actor"], event["assignee"]) || event["by"] != "thread:thr_builder" {
		t.Fatalf("@me diverged: %v", event)
	}
	if _, err := time.Parse(time.RFC3339Nano, event["at"].(string)); err != nil {
		t.Fatal(err)
	}
	started := getWork()["statusSince"]
	call("update", "--node", "api", "--status", "in-progress")
	if getWork()["statusSince"] != started {
		t.Fatal("repeated start invented an interval")
	}
	call("update", "--node", "api", "--status", "stalled", "--reason", "Credentials missing", "--resume-when", "Access provisioned", "--waiting-on-actor", "<alex@example.com>", "--waiting-on-need", "verified(api)")
	stall := getWork()
	raised := stall["stallRaisedAt"]
	if stall["stall"].(map[string]any)["waitingOnActor"].(map[string]any)["kind"] != "user" || stall["waitingOnNeedSatisfied"] != false {
		t.Fatalf("bad stall: %v", stall)
	}
	call("note", "--node", "api", "--text", "Contacted Alex")
	call("update", "--node", "api", "--assignee", "service/payments", "--reassign")
	if work := getWork(); work["status"] != "stalled" || work["stallRaisedAt"] != raised {
		t.Fatalf("assignment or note erased stall: %v", work)
	}
	call("land", "--node", "api")
	call("grant", "--node", "api", "--paths", "extra/**", "--reason", "More scope")
	call("revoke", "--node", "api", "--paths", "extra/**", "--reason", "Scope restored")
	report := call("tick")
	if report["counts"].(map[string]any)["complete"] != float64(0) {
		t.Fatal("coordination invented acceptance")
	}
	call("verify", "--node", "api")
	if work := getWork(); work["status"] != "stalled" || work["waitingOnNeedSatisfied"] != true {
		t.Fatalf("verification implicitly resumed work: %v", work)
	}
	var agent, diagnostics bytes.Buffer
	if err := run(context.Background(), []string{"plan", "tick", path}, func() (string, error) { return root, nil }, &agent, &diagnostics); err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{"Credentials missing", "Access provisioned", "user:alex@example.com", "Referenced need satisfied; awaiting explicit resume.", "service/payments"} {
		if !strings.Contains(agent.String(), expected) {
			t.Fatalf("tick lost active stall %q: %s", expected, &agent)
		}
	}
	// Assigned implementers do not acquire the named reviewer's authority.
	if _, _, _, err := invokePlan(t, root, "adjudicate", path, "--node", "review", "--result", "pass", "--reason", "Claimed review"); err == nil {
		t.Fatal("assignee acquired judgment authority")
	}
	call("adjudicate", "--node", "review", "--by", "<alex@example.com>", "--result", "pass", "--reason", "Inspected")
	// Explicit attribution takes precedence over an invalid inferred identity.
	t.Setenv("WORKBENCH_TEST_THREAD", "invalid")
	call("rule", "--node", "decision", "--by", "user:alex@example.com", "--choice", "ship")
	call("update", "--node", "api", "--by", "user:alex@example.com", "--assignee", "@me", "--reassign", "--status", "in-progress")
	if work := getWork(); work["stall"] != nil || work["assignee"].(map[string]any)["kind"] != "user" {
		t.Fatalf("resume failed: %v", work)
	}
	call("update", "--node", "api", "--by", "user:alex@example.com", "--unassign", "--status", "paused", "--text", "Handed off")
	recall := call("recall")
	if recall["counts"].(map[string]any)["complete"] != float64(3) {
		t.Fatal("work status retracted proof")
	}
	for _, raw := range recall["events"].([]any) {
		e := raw.(map[string]any)
		if e["actor"] == nil || e["at"] == nil {
			t.Fatalf("write missing attribution: %v", e)
		}
	}
	// Reading never re-resolves the environment or old actor policy.
	t.Setenv("WORKBENCH_TEST_USER", "bad")
	before := call("export")
	writePlanFixture(t, root, "threads.pkl", `import "workbench:plan" as Plan
thread = new Plan.ActorKind { pattern = "different" }`)
	after := call("export")
	if !reflect.DeepEqual(before["events"], after["events"]) || !reflect.DeepEqual(before["work"], after["work"]) {
		t.Fatal("policy edit reinterpreted history")
	}
}

func TestPlanCoordinationRefusalsAndInference(t *testing.T) {
	root, path := actorFixture(t)
	ledger := filepath.Join(root, "coordination.ledger.jsonl")
	// Refused operations must not append any partial assignment/status changes.
	reject := func(verb string, flags ...string) {
		t.Helper()
		before, _ := os.ReadFile(ledger)
		if _, _, _, err := invokePlan(t, root, verb, path, flags...); err == nil {
			t.Fatalf("accepted %s %v", verb, flags)
		}
		after, _ := os.ReadFile(ledger)
		if !bytes.Equal(before, after) {
			t.Fatalf("refusal appended event: %s %v", verb, flags)
		}
	}
	for _, flags := range [][]string{
		{}, {"--status", "completed"}, {"--status", "paused"}, {"--status", "stalled", "--reason", "missing"},
		{"--status", "in-progress", "--waiting-on-actor", "alex@example.com"},
		{"--assignee", "garbage"}, {"--assignee", "prefix-thr_builder-suffix"},
		{"--assignee", "@me", "--unassign"}, {"--unassign=true"}, {"--reassign"},
		{"--status", "stalled", "--reason", "wait", "--resume-when", "ready", "--waiting-on-need", "verified(absent)"},
		{"--status", "stalled", "--reason", "wait", "--resume-when", "ready", "--waiting-on-need", "produced(review)"},
	} {
		reject("update", append([]string{"--node", "api"}, flags...)...)
	}
	for _, value := range []string{"", "invalid"} {
		t.Setenv("WORKBENCH_TEST_THREAD", value)
		reject("note", "--text", "No identity")
		reject("verify", "--node", "api")
	}
	t.Setenv("WORKBENCH_TEST_THREAD", "thr_builder")
	t.Setenv("WORKBENCH_TEST_USER", "alex@example.com")
	reject("note", "--text", "Ambiguous inference")
	if _, _, _, err := invokePlan(t, root, "update", path, "--node", "api", "--by", "thr_builder", "--assignee", "@me"); err != nil {
		t.Fatal(err)
	}
	reject("update", "--node", "api", "--by", "user:alex@example.com", "--assignee", "@me", "--status", "stalled", "--reason", "wait", "--resume-when", "ready")
	// The same lock protects check-and-assign as every other append.
	file, err := os.OpenFile(ledger+".lock", os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		t.Fatal(err)
	}
	reject("update", "--node", "api", "--by", "thr_builder", "--status", "in-progress")
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_UN); err != nil {
		t.Fatal(err)
	}
	// A policy edit can disable new assignments without trapping the old one.
	writePlanFixture(t, root, "coordination.plan.pkl", strings.Replace(actorPlan, `id = "api";`, `id = "api"; assignable = false;`, 1))
	reject("update", "--node", "api", "--by", "thr_builder", "--assignee", "@me")
	if _, _, _, err := invokePlan(t, root, "update", path, "--node", "api", "--by", "thr_builder", "--unassign"); err != nil {
		t.Fatal(err)
	}
}

func TestPlanActorAmbiguityAndImportedAmendments(t *testing.T) {
	root, path := actorFixture(t)
	source := strings.Replace(actorPlan, `["thread"] = threads.thread`, `["thread"] = (threads.thread) { infer { env = "WORKBENCH_TEST_OTHER" } }`, 1)
	source = strings.Replace(source, `["external"] { pattern = #"service/[a-z]+"# }`, `["external"] { pattern = #"thr_[A-Za-z0-9_]+"# }`, 1)
	writePlanFixture(t, root, "coordination.plan.pkl", source)
	t.Setenv("WORKBENCH_TEST_OTHER", "thr_other")
	if _, _, _, err := invokePlan(t, root, "note", path, "--by", "thr_builder", "--text", "Ambiguous"); err == nil {
		t.Fatal("first kind won ambiguity")
	}
	result, _, _, err := invokePlan(t, root, "note", path, "--by", "thread:thr_builder", "--text", "Qualified")
	if err != nil || result["event"].(map[string]any)["by"] != "thread:thr_builder" {
		t.Fatalf("qualification failed: %v %v", result, err)
	}
	result, _, _, err = invokePlan(t, root, "note", path, "--text", "Inferred amendment")
	if err != nil || result["event"].(map[string]any)["by"] != "thread:thr_other" {
		t.Fatalf("import amendment failed: %v %v", result, err)
	}
	// Within-kind ambiguity is just as real as cross-kind ambiguity.
	writePlanFixture(t, root, "threads.pkl", `import "workbench:plan" as Plan
thread = new Plan.ActorKind {
 pattern = "thr_[a-z]+"
 aliases {
  new { pattern = "same"; replacement = "thr_one" }
  new { pattern = "same"; replacement = "thr_two" }
 }
}`)
	if _, _, _, err := invokePlan(t, root, "note", path, "--by", "thread:same", "--text", "Ambiguous"); err == nil {
		t.Fatal("within-kind ambiguity accepted")
	}
	for _, field := range []string{`pattern = "["`, `pattern = "x"; aliases { new { pattern = "(x)"; replacement = "$2" } }`} {
		writePlanFixture(t, root, "threads.pkl", "import \"workbench:plan\" as Plan\nthread = new Plan.ActorKind { "+field+" }")
		if _, _, _, err := invokePlan(t, root, "check", path); err == nil {
			t.Fatalf("invalid actor policy accepted: %s", field)
		}
	}
}

// Exercise generated command sequences through Pkl evaluation, invocation
// validation, ledger append/reload and the public report, rather than setters.
func TestPlanWorkCommandProperties(t *testing.T) {
	property := func(sequence []uint8, verified bool) bool {
		root := t.TempDir()
		path := writePlanFixture(t, root, "property.plan.pkl", `amends "workbench:plan"
meta { title = "Properties"; goal = "Coordination preserves acceptance." }
nodes { new Guard { id = "api"; owner = "test"; oracle = module.mechanical("true") } }`)
		if verified {
			if _, _, _, err := invokePlan(t, root, "verify", path); err != nil {
				t.Fatal(err)
			}
		}
		if len(sequence) > 6 {
			sequence = sequence[:6]
		}
		status := "unstarted"
		for _, value := range sequence {
			flags := []string{"--node", "api", "--by", "tester"}
			switch value % 5 {
			case 0:
				status = "in-progress"
				flags = append(flags, "--status", status)
			case 1:
				status = "paused"
				flags = append(flags, "--status", status, "--text", "Yielding")
			case 2:
				status = "stalled"
				flags = append(flags, "--status", status, "--reason", "Waiting", "--resume-when", "Ready")
			case 3:
				flags = append(flags, "--assignee", "@me")
			case 4:
				flags = append(flags, "--unassign")
			}
			if _, _, _, err := invokePlan(t, root, "update", path, flags...); err != nil {
				t.Fatal(err)
			}
		}
		result, first, _, err := invokePlan(t, root, "recall", path)
		_, second, _, err2 := invokePlan(t, root, "export", path)
		if err != nil || err2 != nil || first != second {
			return false
		}
		count := result["counts"].(map[string]any)["complete"].(float64)
		if (count == 1) != verified {
			return false
		}
		category := "ready"
		if verified {
			category = "complete"
		}
		node := result[category].([]any)[0].(map[string]any)
		work := node["work"].(map[string]any)
		if work["status"] != status || (work["stall"] != nil) != (status == "stalled") {
			return false
		}
		return true
	}
	if err := quick.Check(property, &quick.Config{MaxCount: 12}); err != nil {
		t.Fatal(err)
	}
}

// Ensure the legacy string attribution stays readable beside new typed events.
func TestPlanLegacyAttributionBesideTypedUpdates(t *testing.T) {
	root, path := actorFixture(t)
	writePlanFixture(t, root, "coordination.ledger.jsonl", `{"_tag":"Note","by":"old-human","text":"Prior context"}`)
	if _, _, _, err := invokePlan(t, root, "update", path, "--node", "api", "--status", "in-progress"); err != nil {
		t.Fatal(err)
	}
	result, _, _, err := invokePlan(t, root, "recall", path)
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(result["events"].([]any)[0])
	if string(encoded) != `{"_tag":"Note","by":"old-human","text":"Prior context"}` {
		t.Fatalf("legacy event changed: %s", encoded)
	}
}

func TestPlanBundledCoordinationExample(t *testing.T) {
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "examples", "planning", "coordination.plan.pkl")
	result, output, _, err := invokePlan(t, root, "check", path, "--module-root", root)
	if err != nil || result["valid"] != true {
		t.Fatalf("bundled actor definitions: %v\n%s", err, output)
	}
	if len(result["actors"].(map[string]any)["kinds"].(map[string]any)) != 3 {
		t.Fatal("bundled definitions were not combined")
	}
	// Also prove that the optional harness definition can be combined with the
	// example registry without granting ambient access during evaluation.
	source, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	source = []byte(strings.ReplaceAll(string(source), "../../skills/workbench-plan/assets/actors/", "actors/"))
	source = []byte(strings.Replace(string(source), `import "actors/bb.pkl" as bb`, `import "actors/bb.pkl" as bb
import "actors/codex.pkl" as codex`, 1))
	source = []byte(strings.Replace(string(source), `["thread"] = bb.thread`, `["thread"] = bb.thread
    ["codex"] = codex.thread`, 1))
	temp := t.TempDir()
	for _, name := range []string{"bb.pkl", "people.pkl", "services.pkl", "codex.pkl"} {
		data, err := os.ReadFile(filepath.Join(root, "skills", "workbench-plan", "assets", "actors", name))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Join(temp, "actors"), 0700); err != nil {
			t.Fatal(err)
		}
		writePlanFixture(t, temp, filepath.Join("actors", name), string(data))
	}
	composed := writePlanFixture(t, temp, "composed.plan.pkl", string(source))
	if _, _, _, err := invokePlan(t, temp, "check", composed); err != nil {
		t.Fatal(err)
	}
}
