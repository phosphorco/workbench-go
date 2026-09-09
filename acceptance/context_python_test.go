package acceptance

import (
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestContextPythonAcceptance(t *testing.T) {
	moduleRoot, err := filepath.Abs("..")
	if err != nil {
		t.Fatal(err)
	}
	binary := buildContextAcceptanceBinary(t, moduleRoot)
	root := t.TempDir()
	paths := newAcceptanceContextPaths(root)
	registerContextDaemonCleanup(t, paths)
	environment := contextAcceptanceEnvironment(root)

	writeAcceptanceFile(t, filepath.Join(root, "nested", "README.md"), "fixture nested README\n", 0o600)
	writeAcceptanceFile(t, filepath.Join(root, "ai-context.md"), "---\nroot: true\ndocs:\n  - files: [\"nested/README.md\"]\n    message: "+contextOfferText+"\n---\n", 0o600)
	writeAcceptanceFile(t, filepath.Join(root, ".workbench", "context.json"), `{"schemaVersion":1,"optIn":true,"includeChildren":true}`, 0o600)
	writeAcceptanceFile(t, paths.homeConfig, `{"schemaVersion":1,"runtime":{"idleTTLMs":5000}}`, 0o600)

	positive := pythonContextHookPayload(root, "python-session", "python-turn", `python3 -c 'from pathlib import Path; p=Path("README.md"); p.read_text()'`, "nested", map[string]any{"exit_code": 0})
	stdout, stderr, err := runContextProcess(t, binary, root, environment, positive, contextHookArguments(paths, "codex"))
	if err != nil || len(stderr) != 0 {
		t.Fatalf("Python hook failed: %v stderr=%q", err, stderr)
	}
	assertContextHookOffer(t, stdout, contextOfferText)

	history := waitForContextHistory(t, binary, root, environment, paths, 2*time.Second)
	var contributionID uint64
	var offered, confirmed bool
	for _, record := range history.Records {
		if record.ContributionID != 0 {
			contributionID = record.ContributionID
		}
		offered = offered || record.Kind == contextTraceOffered
		confirmed = confirmed || record.Kind == contextTraceConfirmed
	}
	if contributionID == 0 || !offered || !confirmed {
		t.Fatalf("history lacks Python offer/confirmation evidence: %+v", history.Records)
	}
	inspectStdout, inspectStderr, err := runContextProcess(t, binary, root, environment, nil, contextInspectContributionArguments(paths, contributionID))
	if err != nil || len(inspectStderr) != 0 || !strings.Contains(string(inspectStdout), contextOfferText) {
		t.Fatalf("Python contribution was not inspectable: err=%v stderr=%q stdout=%q", err, inspectStderr, inspectStdout)
	}

	failed := pythonContextHookPayload(root, "python-failed-session", "python-failed-turn", `python3 -c 'from pathlib import Path; Path("README.md").write_text("fixture")'`, "nested", map[string]any{"exit_code": 1})
	failedStdout, failedStderr, err := runContextProcess(t, binary, root, environment, failed, contextHookArguments(paths, "codex"))
	if err != nil || len(failedStderr) != 0 {
		t.Fatalf("failed Python intent hook errored: %v stderr=%q", err, failedStderr)
	}
	assertContextHookOffer(t, failedStdout, contextOfferText)

	unrelated := pythonContextHookPayload(root, "python-unrelated-session", "python-unrelated-turn", `python3 -c 'print("nested/README.md")'`, "nested", map[string]any{"exit_code": 0})
	unrelatedStdout, unrelatedStderr, err := runContextProcess(t, binary, root, environment, unrelated, contextHookArguments(paths, "codex"))
	if err != nil || len(unrelatedStdout) != 0 || len(unrelatedStderr) != 0 {
		t.Fatalf("Python string-only negative produced context: err=%v stdout=%q stderr=%q", err, unrelatedStdout, unrelatedStderr)
	}
}

func pythonContextHookPayload(root, session, turn, command, workdir string, response map[string]any) []byte {
	payload := map[string]any{
		"session_id":      session,
		"turn_id":         turn,
		"transcript_path": nil,
		"cwd":             root,
		"hook_event_name": "PostToolUse",
		"tool_name":       "exec_command",
		"tool_input":      map[string]any{"cmd": command, "workdir": workdir},
		"tool_response":   response,
		"tool_use_id":     turn,
	}
	return mustMarshalAcceptanceJSON(payload)
}
