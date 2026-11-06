package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// Each call launches the compiled CLI, so policy changes and revision guards
// must survive independently evaluated plans and ledger reloads.
type planReviewCLI struct{ binary, root, path string }

func (c planReviewCLI) try(t *testing.T, verb string, flags ...string) (map[string]any, error) {
	t.Helper()
	command := exec.Command(c.binary, append([]string{"plan", verb, c.path, "--format", "json"}, flags...)...)
	command.Dir = c.root
	var diagnostics bytes.Buffer
	command.Stderr = &diagnostics
	output, err := command.Output()
	var result map[string]any
	if decodeErr := json.Unmarshal(output, &result); decodeErr != nil {
		t.Fatalf("%s %v returned invalid JSON: %v\n%s\n%s", verb, flags, decodeErr, output, &diagnostics)
	}
	return result, err
}

func (c planReviewCLI) call(t *testing.T, verb string, flags ...string) map[string]any {
	t.Helper()
	result, err := c.try(t, verb, flags...)
	if err != nil {
		t.Fatalf("%s %v failed: %v\n%v", verb, flags, err, result)
	}
	return result
}

func (c planReviewCLI) reject(t *testing.T, verb string, flags ...string) map[string]any {
	t.Helper()
	ledger := strings.TrimSuffix(c.path, ".plan.pkl") + ".ledger.jsonl"
	before, readErr := os.ReadFile(ledger)
	if readErr != nil && !os.IsNotExist(readErr) {
		t.Fatal(readErr)
	}
	result, err := c.try(t, verb, flags...)
	if err == nil {
		t.Fatalf("unexpected success: %s %v\n%v", verb, flags, result)
	}
	after, readErr := os.ReadFile(ledger)
	if readErr != nil && !os.IsNotExist(readErr) {
		t.Fatal(readErr)
	}
	if !bytes.Equal(before, after) {
		t.Fatalf("refusal changed ledger: %s %v", verb, flags)
	}
	return result
}

func (c planReviewCLI) work(t *testing.T, node string) map[string]any {
	t.Helper()
	report := c.call(t, "tick")
	for _, raw := range report["work"].([]any) {
		view := raw.(map[string]any)
		if view["id"] == node {
			return view["work"].(map[string]any)
		}
	}
	t.Fatalf("no reported work for %s: %v", node, report)
	return nil
}

func TestPlanReviewedWorkflowsEndToEnd(t *testing.T) {
	binary := filepath.Join(t.TempDir(), "workbench")
	if output, err := exec.Command("go", "build", "-o", binary, ".").CombinedOutput(); err != nil {
		t.Fatalf("build CLI: %v\n%s", err, output)
	}

	t.Run("guarded stale handoff", func(t *testing.T) {
		root, path := actorFixture(t)
		c := planReviewCLI{binary, root, path}
		initial := c.call(t, "tick")["ready"].([]any)[0].(map[string]any)["work"].(map[string]any)
		if initial["revision"] != float64(0) {
			t.Fatalf("initial revision: %v", initial)
		}
		c.call(t, "update", "--node", "api", "--by", "thr_a", "--assignee", "@me", "--status", "in-progress", "--expect-revision", "0")
		if w := c.work(t, "api"); w["revision"] != float64(1) {
			t.Fatalf("first update revision: %v", w)
		}
		c.call(t, "note", "--node", "api", "--by", "thr_a", "--text", "Handoff context")
		c.call(t, "update", "--node", "review", "--by", "thr_b", "--status", "in-progress", "--expect-revision", "0")
		if w := c.work(t, "api"); w["revision"] != float64(1) {
			t.Fatalf("unrelated writes changed node revision: %v", w)
		}
		c.call(t, "update", "--node", "api", "--by", "thr_b", "--assignee", "@me", "--reassign", "--status", "stalled", "--reason", "Waiting for access", "--resume-when", "Access provisioned", "--expect-revision", "1")
		for _, flags := range [][]string{{"--unassign"}, {"--status", "in-progress"}, {"--unassign", "--status", "paused", "--text", "Old handoff"}} {
			result := c.reject(t, "update", append([]string{"--node", "api", "--by", "thr_a", "--expect-revision", "1"}, flags...)...)
			if !strings.Contains(fmt.Sprint(result), "revision") {
				t.Fatalf("stale update failed for wrong reason: %v", result)
			}
		}
		w := c.work(t, "api")
		if w["revision"] != float64(2) || w["status"] != "stalled" || w["assignee"].(map[string]any)["id"] != "thr_b" {
			t.Fatalf("stale writer changed replacement work: %v", w)
		}
		// Revision guards are opt-in stale-intent checks, not actor authorization.
		c.call(t, "update", "--node", "api", "--by", "thr_a", "--status", "in-progress")
		since := c.work(t, "api")["statusSince"]
		c.call(t, "update", "--node", "api", "--by", "thr_a", "--unassign", "--expect-revision", "3")
		c.call(t, "update", "--node", "api", "--by", "thr_a", "--status", "in-progress", "--expect-revision", "4")
		w = c.work(t, "api")
		if w["revision"] != float64(5) || w["assignee"] != nil || w["statusSince"] != since {
			t.Fatalf("revision or interval semantics: %v", w)
		}
		for _, invalid := range []string{"-1", "invalid", "18446744073709551616"} {
			c.reject(t, "update", "--node", "api", "--by", "thr_a", "--unassign", "--expect-revision", invalid)
		}
	})

	t.Run("competing guarded claims", func(t *testing.T) {
		root, path := actorFixture(t)
		c := planReviewCLI{binary, root, path}
		type outcome struct {
			actor  string
			err    error
			output []byte
		}
		results := make(chan outcome, 2)
		start := make(chan struct{})
		for _, actor := range []string{"thr_a", "thr_b"} {
			go func(actor string) {
				<-start
				command := exec.Command(binary, "plan", "update", path, "--node", "api", "--by", actor, "--assignee", "@me", "--expect-revision", "0", "--format", "json")
				command.Dir = root
				output, err := command.CombinedOutput()
				results <- outcome{actor, err, output}
			}(actor)
		}
		close(start)
		successes := 0
		loser := ""
		winner := ""
		for range 2 {
			r := <-results
			if r.err == nil {
				successes++
				winner = r.actor
			} else {
				loser = r.actor
				if !strings.Contains(string(r.output), "revision") && !strings.Contains(string(r.output), "busy") {
					t.Fatalf("unexpected contention failure: %v\n%s", r.err, r.output)
				}
			}
		}
		if successes != 1 {
			t.Fatalf("expected one successful guarded claim, got %d", successes)
		}
		// A lock refusal may be retried, but its old expectation must not take over.
		c.reject(t, "update", "--node", "api", "--by", loser, "--assignee", "@me", "--reassign", "--expect-revision", "0")
		w := c.work(t, "api")
		if w["revision"] != float64(1) || w["assignee"].(map[string]any)["id"] != winner {
			t.Fatalf("claim conflict lost assignment: %v", w)
		}
	})

	t.Run("owner policy evolution", func(t *testing.T) {
		root, path := actorFixture(t)
		source := strings.ReplaceAll(actorPlan, `"user:alex@example.com"`, `"reviewer"`)
		writePlanFixture(t, root, "coordination.plan.pkl", source)
		people := `import "workbench:plan" as Plan
user = new Plan.ActorKind {
 pattern = #"[^@<>\s]+@[^@<>\s]+\.[^@<>\s]+"#
 aliases { new { pattern = "reviewer"; replacement = "alex@example.com" } }
}`
		writePlanFixture(t, root, "people.pkl", people)
		c := planReviewCLI{binary, root, path}
		c.call(t, "verify", "--node", "api", "--by", "user:alex@example.com")
		c.call(t, "adjudicate", "--node", "review", "--by", "user:alex@example.com", "--result", "pass", "--reason", "Inspected")
		c.call(t, "rule", "--node", "decision", "--by", "user:alex@example.com", "--choice", "ship")
		before := c.call(t, "recall")
		if before["counts"].(map[string]any)["complete"] != float64(3) {
			t.Fatalf("initial acceptance: %v", before)
		}
		writePlanFixture(t, root, "people.pkl", strings.Replace(people, "alex@example.com", "sam@example.com", 1))
		after := c.call(t, "recall")
		counts := after["counts"].(map[string]any)
		if counts["complete"] != float64(1) || counts["stale"] != float64(2) {
			t.Fatalf("owner policy change retained old authority: %v", after)
		}
		if !reflect.DeepEqual(before["events"], after["events"]) {
			t.Fatal("policy change rewrote historical actors/evidence")
		}
		c.reject(t, "adjudicate", "--node", "review", "--by", "user:alex@example.com", "--result", "pass", "--reason", "Old reviewer")
		c.call(t, "adjudicate", "--node", "review", "--by", "user:sam@example.com", "--result", "pass", "--reason", "New review")
		c.call(t, "rule", "--node", "decision", "--by", "user:sam@example.com", "--choice", "ship")
		if r := c.call(t, "recall"); r["counts"].(map[string]any)["complete"] != float64(3) {
			t.Fatalf("new owner could not refresh acceptance: %v", r)
		}
	})

	t.Run("stall references survive plan edits", func(t *testing.T) {
		root := t.TempDir()
		source := `amends "workbench:plan"
meta { title = "Stall references"; goal = "Explain invalid waiting references without invalidating the graph." }
local api = new Action { id = "api"; owner = "test"; oracle = module.mechanical("true") }
local work = new Guard { id = "work"; owner = "test"; needs { module.verified(api) }; oracle = module.mechanical("true") }
nodes { api; work }
`
		path := writePlanFixture(t, root, "stall.plan.pkl", source)
		c := planReviewCLI{binary, root, path}
		c.call(t, "update", "--node", "work", "--by", "tester", "--status", "stalled", "--reason", "Waiting", "--resume-when", "Artifact available", "--waiting-on-need", "produced(api)")
		c.call(t, "land", "--node", "api", "--by", "tester")
		original := c.call(t, "recall")
		raised := c.work(t, "work")["stallRaisedAt"]
		if w := c.work(t, "work"); w["waitingOnNeedSatisfied"] != true {
			t.Fatalf("initial reference: %v", w)
		}
		deleted := strings.Replace(source, `needs { module.verified(api) }; `, "", 1)
		deleted = strings.Replace(deleted, "nodes { api; work }", "nodes { work }", 1)
		changed := strings.Replace(source, "local api = new Action", "local api = new Guard", 1)
		for _, revision := range []string{deleted, changed} {
			writePlanFixture(t, root, "stall.plan.pkl", revision)
			report := c.call(t, "check")
			if report["valid"] != true || report["diagnostics"] != nil && len(report["diagnostics"].([]any)) != 0 {
				t.Fatalf("annotation invalidated graph: %v", report)
			}
			w := c.work(t, "work")
			warning, ok := w["waitingOnNeedError"].(string)
			if !ok || warning == "" || !strings.Contains(warning, "api") {
				t.Fatalf("missing scoped reference warning: %v", w)
			}
			if _, present := w["waitingOnNeedSatisfied"]; present {
				t.Fatalf("invalid reference claimed an evidence result: %v", w)
			}
			if w["status"] != "stalled" || w["stallRaisedAt"] != raised {
				t.Fatalf("plan revision changed stall interval: %v", w)
			}
			command := exec.Command(binary, "plan", "tick", path)
			command.Dir = root
			output, err := command.Output()
			if err != nil || !strings.Contains(string(output), warning) {
				t.Fatalf("agent output lost scoped warning: %v\n%s", err, output)
			}
		}
		writePlanFixture(t, root, "stall.plan.pkl", source)
		restored := c.call(t, "recall")
		w := c.work(t, "work")
		if w["waitingOnNeedError"] != nil || w["waitingOnNeedSatisfied"] != true || w["status"] != "stalled" {
			t.Fatalf("reference restoration: %v", w)
		}
		if !reflect.DeepEqual(original["events"], restored["events"]) {
			t.Fatal("projection of edited plans modified ledger history")
		}
	})

	t.Run("replacement boundaries and canonical round trip", func(t *testing.T) {
		root := t.TempDir()
		source := `amends "workbench:plan"
meta { title = "Identity normalization"; goal = "Keep normalized identities stable." }
actors { kinds { ["test"] {
 pattern = "id_.+"
 aliases {
  new { pattern = "(x)"; replacement = "id_$1é" }
  new { pattern = "(id_.+)"; replacement = "${1}_other" }
 }
} } }
nodes { new Guard { id = "proof"; owner = "test"; oracle = module.mechanical("true") } }
`
		path := writePlanFixture(t, root, "identity.plan.pkl", source)
		c := planReviewCLI{binary, root, path}
		c.reject(t, "check")
		source = strings.Replace(source, "id_$1é", "id_${1}é", 1)
		writePlanFixture(t, root, "identity.plan.pkl", source)
		first := c.call(t, "note", "--by", "x", "--text", "Normalized alias")["event"].(map[string]any)
		if first["by"] != "test:id_xé" {
			t.Fatalf("braced capture lost suffix: %v", first)
		}
		for _, identity := range []string{first["by"].(string), "test:x"} {
			next := c.call(t, "note", "--by", identity, "--text", "Round trip")["event"].(map[string]any)
			if !reflect.DeepEqual(first["actor"], next["actor"]) {
				t.Fatalf("normalization changed typed identity: %v vs %v", first, next)
			}
		}
		c.reject(t, "note", "--by", "id_xé", "--text", "Unqualified canonical/alias conflict")
	})

	t.Run("new timestamps and old events", func(t *testing.T) {
		root := t.TempDir()
		path := writePlanFixture(t, root, "time.plan.pkl", `amends "workbench:plan"
meta { title = "Timestamps"; goal = "Validate new event timestamps without rewriting history." }
nodes { new Guard { id = "api"; owner = "test"; oracle = module.mechanical("true") } }`)
		c := planReviewCLI{binary, root, path}
		event := c.call(t, "update", "--node", "api", "--by", "tester", "--status", "in-progress")["event"].(map[string]any)
		ledger := strings.TrimSuffix(path, ".plan.pkl") + ".ledger.jsonl"
		for _, at := range []any{nil, "", "bad", "2026-11-06 00:00:00"} {
			delete(event, "at")
			if at != nil {
				event["at"] = at
			}
			data, err := json.Marshal(event)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(ledger, data, 0600); err != nil {
				t.Fatal(err)
			}
			c.reject(t, "recall")
		}
		legacy := "{\"_tag\":\"Note\",\"by\":\"old\",\"text\":\"Before timestamps\"}\n{\"_tag\":\"Evidence\",\"node\":\"api\",\"by\":\"old\",\"result\":\"pass\"}\n"
		if err := os.WriteFile(ledger, []byte(legacy), 0600); err != nil {
			t.Fatal(err)
		}
		report := c.call(t, "recall")
		if report["counts"].(map[string]any)["complete"] != float64(1) || len(report["events"].([]any)) != 2 {
			t.Fatalf("legacy events refused: %v", report)
		}
	})
	t.Run("invalid owner with historical digest", func(t *testing.T) {
		root := t.TempDir()
		path := writePlanFixture(t, root, "legacy-owner.plan.pkl", `amends "workbench:plan"
meta { title = "Legacy owner"; goal = "Report owner diagnostics before interpreting legacy fingerprints." }
actors { kinds { ["thread"] { pattern = "thr_[a-z]+" } } }
nodes { new Selector { id = "decision"; owner = "unrecognized"; options { "ship"; "wait" } } }
`)
		writePlanFixture(t, root, "legacy-owner.ledger.jsonl", `{"_tag":"ForkRuled","node":"decision","choice":"ship","by":"old","digest":"historical-bun-digest"}`)
		c := planReviewCLI{binary, root, path}
		for _, verb := range []string{"check", "recall"} {
			report := c.reject(t, verb)
			if report["error"] != nil || report["valid"] != false {
				t.Fatalf("legacy reader masked definition diagnostics: %v", report)
			}
			found := false
			for _, raw := range report["diagnostics"].([]any) {
				diagnostic := raw.(map[string]any)
				if diagnostic["code"] == "InvalidActorOwner" {
					found = true
				}
			}
			if !found {
				t.Fatalf("missing actor-owner diagnostic: %v", report)
			}
		}
	})
}
