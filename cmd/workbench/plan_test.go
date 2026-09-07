package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

func TestPlanLocalEvidenceLoop(t *testing.T) {
	root := t.TempDir()
	definition := `amends "workbench:plan"
meta { title = "Local proof"; goal = "Prove real evidence unlocks work." }
local api = new Action {
  id = "api"
  owner = "model"
  outcome = "Write the API artifact."
  grant { "api/**" }
  oracle = module.mechanical("test -f artifact.txt")
}

local review = new Guard {
  id = "review"
  owner = "reviewer"
  observes { "api/**" }
  needs { module.verified(api) }
  oracle = module.adjudicated("The artifact meets the brief.", "cole")
}
nodes { api; review }
`
	path := filepath.Join(root, "proof.plan.pkl")
	if err := os.WriteFile(path, []byte(definition), 0600); err != nil {
		t.Fatal(err)
	}
	invoke := func(verb string, args ...string) (map[string]any, string, error) {
		t.Helper()
		var output, diagnostics bytes.Buffer
		arguments := append([]string{"plan", verb, path, "--format", "json"}, args...)
		err := run(context.Background(), arguments, func() (string, error) { return root, nil }, &output, &diagnostics)
		var result map[string]any
		if !json.Valid(output.Bytes()) {
			t.Fatalf("%v: invalid JSON %q, error %v, diagnostics %s", arguments, output.String(), err, &diagnostics)
		}
		if decodeErr := json.Unmarshal(output.Bytes(), &result); decodeErr != nil {
			t.Fatal(decodeErr)
		}
		return result, output.String(), err
	}
	result, first, err := invoke("tick")
	if err != nil {
		t.Fatal(err)
	}
	if len(result["ready"].([]any)) != 1 || !strings.Contains(first, `"needs":["verified(api)"]`) {
		t.Fatalf("unexpected tick: %s", first)
	}
	_, second, err := invoke("tick")
	if err != nil || first != second {
		t.Fatalf("tick is not deterministic: %v", err)
	}
	ledger := filepath.Join(root, "proof.ledger.jsonl")
	if _, err := os.Stat(ledger); !os.IsNotExist(err) {
		t.Fatalf("read created ledger: %v", err)
	}
	if _, _, err := invoke("verify", "--node", "api"); err == nil {
		t.Fatal("failed oracle returned success")
	}
	if err := os.WriteFile(filepath.Join(root, "artifact.txt"), []byte("real artifact"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := invoke("verify", "--node", "api"); err != nil {
		t.Fatal(err)
	}
	result, _, err = invoke("tick")
	if err != nil || result["ready"].([]any)[0].(map[string]any)["id"] != "review" {
		t.Fatalf("review not unlocked: %v %v", result, err)
	}
	if _, _, err := invoke("adjudicate", "--node", "review", "--by", "cole", "--result", "pass", "--reason", "inspected the artifact"); err != nil {
		t.Fatal(err)
	}
	result, _, err = invoke("tick")
	if err != nil || result["counts"].(map[string]any)["complete"] != float64(2) {
		t.Fatalf("not complete: %v %v", result, err)
	}
	if _, _, err := invoke("recall"); err != nil {
		t.Fatal(err)
	}
}

func invokePlan(t *testing.T, root, verb, path string, flags ...string) (map[string]any, string, string, error) {
	t.Helper()
	var output, diagnostics bytes.Buffer
	err := run(context.Background(), append([]string{"plan", verb, path, "--format", "json"}, flags...), func() (string, error) { return root, nil }, &output, &diagnostics)
	var result map[string]any
	if !json.Valid(output.Bytes()) {
		t.Fatalf("%s returned invalid JSON %q: %v", verb, output.String(), err)
	}
	if decodeErr := json.Unmarshal(output.Bytes(), &result); decodeErr != nil {
		t.Fatal(decodeErr)
	}
	return result, output.String(), diagnostics.String(), err
}

func writePlanFixture(t *testing.T, root, name, content string) string {
	t.Helper()
	path := filepath.Join(root, name)
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestPlanPklFragmentsAndConfinement(t *testing.T) {
	root := t.TempDir()
	writePlanFixture(t, root, "fragment.pkl", `import "workbench:plan" as Plan
function task(key: String): Plan.Action = new {
  id = key
  owner = "model"
  grant { "src/\(key)/**" }
  oracle = Plan.mechanical("true")
}
`)
	source := `amends "workbench:plan"
import "fragment.pkl"
meta { title = "Composition"; goal = "Import a reusable typed task." }
nodes { fragment.task("api") }
`
	path := writePlanFixture(t, root, "composition.plan.pkl", source)
	result, _, _, err := invokePlan(t, root, "tick", path)
	if err != nil || result["ready"].([]any)[0].(map[string]any)["id"] != "api" {
		t.Fatalf("fragment failed: %v %v", result, err)
	}
	for _, test := range []struct{ name, expression string }{
		{"environment", `read("env:HOME")`}, {"resource", `read("file:/etc/passwd")`},
		{"network", `import("https://example.invalid/remote.pkl").secret`},
		{"outside", `import("../outside.pkl").secret`},
	} {
		t.Run(test.name, func(t *testing.T) {
			denied := strings.Replace(source, `title = "Composition"`, "title = "+test.expression, 1)
			writePlanFixture(t, root, "denied.plan.pkl", denied)
			if _, _, _, err := invokePlan(t, root, "tick", filepath.Join(root, "denied.plan.pkl")); err == nil {
				t.Fatal("ambient read was allowed")
			}
		})
	}
	outside := t.TempDir()
	writePlanFixture(t, outside, "secret.pkl", `secret = "must not be read"`)
	if err := os.Symlink(filepath.Join(outside, "secret.pkl"), filepath.Join(root, "escape.pkl")); err != nil {
		t.Fatal(err)
	}
	writePlanFixture(t, root, "escape.plan.pkl", strings.Replace(source, `title = "Composition"`, `title = import("escape.pkl").secret`, 1))
	if _, _, _, err := invokePlan(t, root, "tick", filepath.Join(root, "escape.plan.pkl")); err == nil {
		t.Fatal("symlink escaped module root")
	}
}

func TestPlanPklTypesAndProbeGating(t *testing.T) {
	root := t.TempDir()
	source := `amends "workbench:plan"
meta { title = "Decision"; goal = "Observe before deciding." }
local census = new Guard {
  id = "probe"; owner = "planner"
  oracle = module.mechanical("true")
}
local decision = new Selector {
  id = "decision"; owner = "cole"
  options { "small"; "large" }
  probe = census
}
nodes { census; decision }
`
	path := writePlanFixture(t, root, "decision.plan.pkl", source)
	if _, _, _, err := invokePlan(t, root, "rule", path, "--node", "decision", "--choice", "small", "--by", "cole"); err == nil {
		t.Fatal("decision bypassed probe")
	}
	if _, _, _, err := invokePlan(t, root, "verify", path); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := invokePlan(t, root, "rule", path, "--node", "decision", "--choice", "small", "--by", "someone"); err == nil {
		t.Fatal("wrong decision owner accepted")
	}
	if _, _, _, err := invokePlan(t, root, "rule", path, "--node", "decision", "--choice", "small", "--by", "cole"); err != nil {
		t.Fatal(err)
	}
	writePlanFixture(t, root, "decision.plan.pkl", strings.Replace(source, `"small"; "large"`, `"new"; "large"`, 1))
	result, _, _, err := invokePlan(t, root, "tick", path)
	if err != nil || result["counts"].(map[string]any)["stale"] != float64(1) {
		t.Fatalf("edited decision did not reopen: %v %v", result, err)
	}
	bad := strings.Replace(source, `probe = census`, `needs { module.produced(census) }`, 1)
	writePlanFixture(t, root, "wrong.plan.pkl", bad)
	if _, _, _, err := invokePlan(t, root, "check", filepath.Join(root, "wrong.plan.pkl")); err == nil {
		t.Fatal("Pkl accepted produced(Guard)")
	}
}

func TestPlanOnceStdoutAndLegacyEvidence(t *testing.T) {
	root := t.TempDir()
	source := `amends "workbench:plan"
meta { title = "Historical"; goal = "Keep an observed historical fact." }
nodes {
  new Guard {
    id = "red"; owner = "testing"
    oracle = (module.mechanical("printf 'oracle output'; touch observed")) { once = true }
  }
}
`
	path := writePlanFixture(t, root, "once.plan.pkl", source)
	_, _, diagnostics, err := invokePlan(t, root, "verify", path, "--node", "red")
	if err != nil || diagnostics != "oracle output" {
		t.Fatalf("oracle output routing: %q %v", diagnostics, err)
	}
	ledger := filepath.Join(root, "once.ledger.jsonl")
	before, err := os.ReadFile(ledger)
	if err != nil {
		t.Fatal(err)
	}
	writePlanFixture(t, root, "once.plan.pkl", strings.Replace(source, "printf 'oracle output'; touch observed", "exit 19", 1))
	_, output, _, err := invokePlan(t, root, "verify", path, "--node", "red")
	after, readErr := os.ReadFile(ledger)
	if err != nil || readErr != nil || !bytes.Equal(before, after) || !strings.Contains(output, "already-observed") {
		t.Fatalf("historical evidence reran: %s %v", output, err)
	}
	// A real legacy Bun.hash event remains fresh under an equivalent Pkl definition.
	command := exec.Command("bun", "-e", `console.log(Bun.hash(JSON.stringify(["Mechanical","true"])).toString(36))`)
	digest, err := command.Output()
	if err != nil {
		t.Fatal(err)
	}
	legacy := strings.Replace(source, "printf 'oracle output'; touch observed", "true", 1)
	legacy = strings.Replace(legacy, "once = true", "once = false", 1)
	writePlanFixture(t, root, "legacy.plan.pkl", legacy)
	writePlanFixture(t, root, "legacy.ledger.jsonl", fmt.Sprintf(`{"_tag":"Evidence","node":"red","result":"pass","by":"old","digest":%q}`, strings.TrimSpace(string(digest))))
	result, _, _, err := invokePlan(t, root, "tick", filepath.Join(root, "legacy.plan.pkl"))
	if err != nil || result["counts"].(map[string]any)["complete"] != float64(1) {
		t.Fatalf("legacy evidence lost: %v %v", result, err)
	}
	writePlanFixture(t, root, "legacy.plan.pkl", strings.Replace(legacy, `mechanical("true")`, `mechanical("false")`, 1))
	result, _, _, err = invokePlan(t, root, "tick", filepath.Join(root, "legacy.plan.pkl"))
	if err != nil || result["counts"].(map[string]any)["stale"] != float64(1) {
		t.Fatalf("legacy edit not stale: %v %v", result, err)
	}
}

func TestPlanCancellationDoesNotMintEvidence(t *testing.T) {
	root := t.TempDir()
	path := writePlanFixture(t, root, "slow.plan.pkl", `amends "workbench:plan"
meta { title = "Cancellation"; goal = "Do not call interrupted verification a result." }
nodes { new Guard { id = "slow"; owner = "testing"; oracle = module.mechanical("touch started; sleep 30") } }
`)
	_, _, _, err := invokePlan(t, root, "verify", path, "--node", "slow", "--timeout", "2s")
	if err == nil {
		t.Fatal("timeout succeeded")
	}
	if _, err := os.Stat(filepath.Join(root, "started")); err != nil {
		t.Fatalf("timeout witness never reached the oracle: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "slow.ledger.jsonl")); !os.IsNotExist(err) {
		t.Fatalf("timeout minted evidence: %v", err)
	}
}

func TestPlanHelpAndUsage(t *testing.T) {
	for _, args := range [][]string{{"plan"}, {"plan", "--help"}, {"plan", "tick", "--help"}} {
		var output, diagnostics bytes.Buffer
		err := run(context.Background(), args, func() (string, error) { t.Fatal("help touched cwd"); return "", nil }, &output, &diagnostics)
		if err != nil || !strings.Contains(output.String(), "one node/event per line") {
			t.Fatalf("help unavailable: %v %s", err, &output)
		}
	}
	root := t.TempDir()
	for _, args := range [][]string{{"plan", "tikk", "x.plan.pkl"}, {"plan", "tick", "x.plan.pkl", "--node", "x"}, {"plan", "verify", "x.plan.pkl", "--timeout", "0s"}} {
		var output, diagnostics bytes.Buffer
		if err := run(context.Background(), append(args, "--format", "json"), func() (string, error) { return root, nil }, &output, &diagnostics); err == nil || !json.Valid(output.Bytes()) {
			t.Fatalf("usage did not return structured failure: %v %s", err, &output)
		}
	}
}

func TestPlanLedgerLockAndMalformedRecovery(t *testing.T) {
	root := t.TempDir()
	path := writePlanFixture(t, root, "lock.plan.pkl", `amends "workbench:plan"
meta { title = "Lock"; goal = "Serialize ledger writers." }
nodes { new Guard { id = "check"; owner = "testing"; oracle = module.mechanical("touch executed") } }
`)
	ledger := filepath.Join(root, "lock.ledger.jsonl")
	file, err := os.OpenFile(ledger+".lock", os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := invokePlan(t, root, "verify", path, "--node", "check"); err == nil {
		t.Fatal("concurrent writer bypassed lock")
	}
	if _, err := os.Stat(filepath.Join(root, "executed")); !os.IsNotExist(err) {
		t.Fatal("refused write ran oracle")
	}
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_UN); err != nil {
		t.Fatal(err)
	}
	// Preserve an existing digest-free final event without a newline.
	writePlanFixture(t, root, "lock.ledger.jsonl", `{"_tag":"Note","text":"legacy","by":"old"}`)
	if _, _, _, err := invokePlan(t, root, "note", path, "--text", "next", "--by", "test"); err != nil {
		t.Fatal(err)
	}
	result, _, _, err := invokePlan(t, root, "recall", path)
	if err != nil || len(result["events"].([]any)) != 2 {
		t.Fatalf("append lost legacy event: %v %v", result, err)
	}
	writePlanFixture(t, root, "lock.ledger.jsonl", `{"_tag":`)
	if _, _, _, err := invokePlan(t, root, "verify", path, "--node", "check"); err == nil {
		t.Fatal("malformed ledger treated as empty")
	}
	if _, err := os.Stat(filepath.Join(root, "executed")); !os.IsNotExist(err) {
		t.Fatal("bad ledger ran oracle")
	}
}

func TestPlanGrantRefusalAndRevocationRecovery(t *testing.T) {
	root := t.TempDir()
	source := `amends "workbench:plan"
meta { title = "Grants"; goal = "Keep parallel work disjoint." }
local a = new Action { id = "a"; owner = "a"; grant { "a/**" }; oracle = module.mechanical("true") }
local b = new Action { id = "b"; owner = "b"; grant { "b/**" }; oracle = module.mechanical("true") }
nodes {
 a; b
 new Guard { id = "join"; owner = "review"; needs { module.verified(a); module.verified(b) }; oracle = module.mechanical("true") }
}
`
	path := writePlanFixture(t, root, "grant.plan.pkl", source)
	if _, _, _, err := invokePlan(t, root, "grant", path, "--node", "a", "--paths", "b/file", "--reason", "collision"); err == nil {
		t.Fatal("colliding grant accepted")
	}
	if _, err := os.Stat(filepath.Join(root, "grant.ledger.jsonl")); !os.IsNotExist(err) {
		t.Fatal("refused grant modified ledger")
	}
	if _, _, _, err := invokePlan(t, root, "grant", path, "--node", "a", "--paths", "shared/**", "--reason", "additional ownership"); err != nil {
		t.Fatal(err)
	}
	writePlanFixture(t, root, "grant.plan.pkl", strings.Replace(source, `grant { "b/**" }`, `grant { "shared/**" }`, 1))
	if _, _, _, err := invokePlan(t, root, "check", path); err == nil {
		t.Fatal("edit did not expose collision")
	}
	if _, _, _, err := invokePlan(t, root, "revoke", path, "--node", "a", "--paths", "shared/**", "--reason", "restore disjoint ownership"); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := invokePlan(t, root, "check", path); err != nil {
		t.Fatal(err)
	}
}

func TestPlanAgentFormatAndJSONInterchange(t *testing.T) {
	root := t.TempDir()
	path := writePlanFixture(t, root, "format.plan.pkl", `amends "workbench:plan"
meta { title = "Readable <task> & proof"; goal = "Preserve literal commands." }
local api = new Action {
 id = "api"; owner = "model"; outcome = "Write <api> & inspect it."
 grant { "api/**" }
 oracle = module.mechanical("test 1 -lt 2 && echo 'a < b & c'")
}
nodes {
 api
 new Guard { id = "review"; owner = "cole"; needs { module.verified(api) }; oracle = module.adjudicated("Inspect <api> & proof.", "cole") }
}
`)
	invoke := func(verb string, flags ...string) (string, error) {
		t.Helper()
		var output, diagnostics bytes.Buffer
		err := run(context.Background(), append([]string{"plan", verb, path}, flags...), func() (string, error) { return root, nil }, &output, &diagnostics)
		return output.String(), err
	}
	for _, verb := range []string{"check", "tick", "recall", "export"} {
		t.Run(verb, func(t *testing.T) {
			flags := []string{}
			if verb == "export" {
				flags = []string{"--format", "agent"}
			}
			agent, err := invoke(verb, flags...)
			if err != nil {
				t.Fatal(err)
			}
			for _, want := range []string{"<workbench-plan>", "</workbench-plan>", "Readable <task> & proof", "test 1 -lt 2 && echo 'a < b & c'", "Evidence missing: verified(api)", "**Ready**", "**Blocked**", "api/**", "cole"} {
				if !strings.Contains(agent, want) {
					t.Fatalf("%s missing %q:\n%s", verb, want, agent)
				}
			}
			again, err := invoke(verb, flags...)
			if err != nil || agent != again {
				t.Fatalf("nondeterministic %s: %v", verb, err)
			}
			machine, err := invoke(verb, "--format", "json")
			var report struct {
				Counts struct{ Ready, Blocked int }
				Ready  []struct{ ID string }
			}
			if err != nil || json.Unmarshal([]byte(machine), &report) != nil || report.Counts.Ready != 1 || report.Counts.Blocked != 1 || report.Ready[0].ID != "api" {
				t.Fatalf("JSON semantics diverged: %s %v", machine, err)
			}
		})
	}
	exported, err := invoke("export")
	explicit, explicitErr := invoke("recall", "--format=json")
	if err != nil || explicitErr != nil || exported != explicit || !json.Valid([]byte(exported)) {
		t.Fatalf("export default is not recall JSON: %v %v\n%s", err, explicitErr, exported)
	}
	if _, err := os.Stat(filepath.Join(root, "format.ledger.jsonl")); !os.IsNotExist(err) {
		t.Fatalf("read created ledger: %v", err)
	}
	note := "literal </workbench-plan> & `code`\nsecond line"
	written, err := invoke("note", "--text", note, "--by", "cole")
	if err != nil || !strings.Contains(written, "<workbench-plan>") || !strings.Contains(written, note) {
		t.Fatalf("note lost literal content: %v\n%s", err, written)
	}
	_, err = invoke("verify", "--node", "api", "--format", "yaml")
	if err == nil {
		t.Fatal("unsupported format accepted")
	}
}

func TestPlanAgentPreservesTypedBlockersAndRecordedEvidence(t *testing.T) {
	root := t.TempDir()
	source := `amends "workbench:plan"
meta { title = "Evidence distinctions"; goal = "Keep artifacts, proof and decisions separate." }
local api = new Action { id = "api"; owner = "model"; grant { "api/**" }; oracle = module.mechanical("echo 'a < b & c'") }
local review = new Guard { id = "review"; owner = "cole"; needs { module.produced(api); module.verified(api) }; oracle = module.adjudicated("Inspect <api> & proof.", "cole") }
local decision = new Selector { id = "decision"; owner = "cole"; options { "ship"; "wait" }; needs { module.verified(review) } }
nodes {
 api; review; decision
 new Guard { id = "done"; owner = "testing"; needs { module.ruled(decision) }; oracle = module.mechanical("true") }
}
`
	path := writePlanFixture(t, root, "typed.plan.pkl", source)
	invoke := func(verb string, flags ...string) string {
		t.Helper()
		var output, diagnostics bytes.Buffer
		if err := run(context.Background(), append([]string{"plan", verb, path}, flags...), func() (string, error) { return root, nil }, &output, &diagnostics); err != nil {
			t.Fatalf("%s: %v\n%s", verb, err, &output)
		}
		if !strings.HasPrefix(output.String(), "<workbench-plan>\n") {
			t.Fatalf("%s did not use agent output: %s", verb, &output)
		}
		if verb == "verify" && strings.Contains(output.String(), "a < b & c") {
			t.Fatal("oracle stdout polluted report")
		}
		return output.String()
	}
	first := invoke("tick")
	for _, want := range []string{"Artifact missing: produced(api)", "Evidence missing: verified(api)", "Ruling missing: ruled(decision)"} {
		if !strings.Contains(first, want) {
			t.Fatalf("missing %q:\n%s", want, first)
		}
	}
	if got := invoke("land", "--node", "api"); !strings.Contains(got, "**Artifact**") {
		t.Fatal(got)
	}
	landed := invoke("tick")
	if strings.Contains(landed, "Artifact missing: produced(api)") || !strings.Contains(landed, "Evidence missing: verified(api)") {
		t.Fatalf("landing changed proof: %s", landed)
	}
	if got := invoke("verify", "--node", "api"); !strings.Contains(got, "**Evidence**") || !strings.Contains(got, "· pass ·") {
		t.Fatal(got)
	}
	writePlanFixture(t, root, "typed.plan.pkl", strings.Replace(source, "echo 'a < b & c'", "true", 1))
	if got := invoke("tick"); !strings.Contains(got, "Evidence stale: verified(api)") || strings.Contains(got, "Artifact stale: produced(api)") {
		t.Fatalf("stale proof conflated with artifact: %s", got)
	}
	invoke("verify", "--node", "api")
	invoke("grant", "--node", "api", "--paths", "extra/**", "--reason", "Expand scope")
	invoke("revoke", "--node", "api", "--paths", "extra/**", "--reason", "Restore scope")
	invoke("adjudicate", "--node", "review", "--result", "pass", "--by", "cole", "--reason", "Inspected <api> & proof")
	invoke("rule", "--node", "decision", "--choice", "ship", "--by", "cole")
	invoke("verify")
	final := invoke("recall")
	if !strings.Contains(final, "4 complete · 0 ready · 0 blocked") || !strings.Contains(final, "**Event history**") || !strings.Contains(final, "Inspected <api> & proof") || !strings.Contains(final, "**ForkRuled**") {
		t.Fatal(final)
	}
}

func TestPlanFormatErrorsAreReadOnlyAndRespectLiterals(t *testing.T) {
	for _, test := range []struct {
		args []string
		json bool
	}{
		{[]string{"plan", "tikk", "x.plan.pkl"}, false},
		{[]string{"plan", "tikk", "x.plan.pkl", "--format", "json"}, true},
		{[]string{"plan", "tick", "x.plan.pkl", "--format", "xml"}, false},
		{[]string{"plan", "export", "x.plan.pkl", "--timeout", "bad"}, true},
		{[]string{"plan", "note", "x.plan.pkl", "--text", "--format=json"}, false},
	} {
		var output bytes.Buffer
		err := run(context.Background(), test.args, func() (string, error) { t.Fatal("invalid invocation observed cwd"); return "", nil }, &output, &bytes.Buffer{})
		if err == nil || json.Valid(output.Bytes()) != test.json {
			t.Fatalf("%v: %v\n%s", test.args, err, &output)
		}
		if !test.json && (!strings.HasPrefix(output.String(), "<workbench-plan>\n") || !strings.Contains(output.String(), "**Invocation:**")) {
			t.Fatalf("missing classified agent error: %s", &output)
		}
	}
}
