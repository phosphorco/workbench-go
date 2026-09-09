package acceptance

import (
	"bytes"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const (
	contextDeclarationProjectFile = "workbench-context.pkl"
	contextDeclarationSchemaURI   = "workbench:context"
	contextDeclarationBuiltin     = "Explicit Pkl builtin activation."
)

func TestContextDeclarationPublicWitnesses(t *testing.T) {
	moduleRoot, err := filepath.Abs("..")
	if err != nil {
		t.Fatal(err)
	}
	binary := contextDeclarationAcceptanceBinary(t, moduleRoot)

	t.Run("no source is silent and creates no context artifacts", func(t *testing.T) {
		root := t.TempDir()
		paths := contextDeclarationPaths(root)
		registerContextDaemonCleanup(t, paths)
		environment := contextAcceptanceEnvironment(root)

		stdout, stderr, runErr := runContextProcess(t, binary, root, environment,
			claudeContextHookPayload(root, "no-source-session", "no-source-turn", ""),
			contextHookArguments(paths, "claude"))
		if runErr != nil || len(stdout) != 0 || len(stderr) != 0 {
			t.Fatalf("no-source hook: err=%v stdout=%q stderr=%q", runErr, stdout, stderr)
		}
		for _, path := range contextDeclarationArtifacts(root, paths) {
			if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
				t.Fatalf("no-source hook created context artifact %q: %v", path, statErr)
			}
		}
	})

	t.Run("explicit builtin Pkl declaration activates the real hook", func(t *testing.T) {
		root := t.TempDir()
		copyContextDeclarationFixture(t, moduleRoot, "pkl-builtin", root)
		paths := contextDeclarationPaths(root)
		registerContextDaemonCleanup(t, paths)
		environment := contextAcceptanceEnvironment(root)

		stdout, stderr, runErr := runContextProcess(t, binary, root, environment,
			claudeContextHookPayload(root, "pkl-session", "pkl-turn", ""),
			contextHookArguments(paths, "claude"))
		if runErr != nil || len(stderr) != 0 {
			t.Fatalf("Pkl builtin hook failed: err=%v stderr=%q stdout=%q", runErr, stderr, stdout)
		}
		if !bytes.Contains(stdout, []byte(contextDeclarationBuiltin)) {
			t.Fatalf("Pkl builtin declaration did not activate guidance: stdout=%q", stdout)
		}
	})

	t.Run("legacy JSON alone is inert even with matching guidance", func(t *testing.T) {
		root := t.TempDir()
		copyContextDeclarationFixture(t, moduleRoot, "legacy-json-only", root)
		paths := contextDeclarationPaths(root)
		registerContextDaemonCleanup(t, paths)
		environment := contextAcceptanceEnvironment(root)
		legacyPath := filepath.Join(root, ".workbench", "context.json")
		legacyBefore := readAcceptanceFile(t, legacyPath)

		stdout, stderr, runErr := runContextProcess(t, binary, root, environment,
			claudeContextHookPayload(root, "legacy-json-session", "legacy-json-turn", ""),
			contextHookArguments(paths, "claude"))
		if runErr != nil || len(stderr) != 0 || len(stdout) != 0 {
			t.Fatalf("legacy JSON was not inert: err=%v stdout=%q stderr=%q", runErr, stdout, stderr)
		}
		if got := readAcceptanceFile(t, legacyPath); got != legacyBefore {
			t.Fatalf("legacy JSON was rewritten: before=%q after=%q", legacyBefore, got)
		}
	})

	for _, boundary := range []struct {
		name   string
		module string
	}{
		{name: "disabled nearest declaration", module: "disabled"},
		{name: "empty nearest declaration", module: "empty"},
		{name: "directory-only nearest declaration", module: "directory-only"},
	} {
		boundary := boundary
		t.Run(boundary.name+" blocks ancestor selection", func(t *testing.T) {
			root := t.TempDir()
			ancestor := filepath.Join(root, "ancestor")
			child := filepath.Join(ancestor, "child")
			workingDirectory := child
			if boundary.module == "directory-only" {
				workingDirectory = filepath.Join(child, "nested")
			}
			if err := os.MkdirAll(workingDirectory, 0o700); err != nil {
				t.Fatal(err)
			}
			relativeWorkingDirectory, err := filepath.Rel(ancestor, workingDirectory)
			if err != nil {
				t.Fatal(err)
			}
			observedPath := filepath.ToSlash(filepath.Join(relativeWorkingDirectory, "README.md"))
			writeAcceptanceFile(t, filepath.Join(ancestor, ".workbench", "context.json"), `{"schemaVersion":1,"optIn":true,"includeChildren":true}`, 0o600)
			writeAcceptanceFile(t, filepath.Join(ancestor, "ai-context.md"), fmt.Sprintf("---\nroot: true\ndocs:\n  - files: [%q]\n    message: Legacy ancestor selection must be blocked.\n---\n", observedPath), 0o600)
			writeAcceptanceFile(t, filepath.Join(workingDirectory, "README.md"), "boundary witness\n", 0o600)
			writeAcceptanceFile(t, filepath.Join(child, contextDeclarationProjectFile), boundaryModule(boundary.module), 0o600)

			paths := contextDeclarationPaths(root)
			registerContextDaemonCleanup(t, paths)
			environment := contextAcceptanceEnvironment(root)
			stdout, stderr, runErr := runContextProcess(t, binary, workingDirectory, environment,
				claudeContextHookPayload(workingDirectory, "boundary-session-"+boundary.module, "boundary-turn-"+boundary.module, ""),
				contextHookArguments(paths, "claude"))
			if runErr != nil || len(stderr) != 0 || len(stdout) != 0 {
				t.Fatalf("ancestor selection crossed nearest %s boundary: err=%v stdout=%q stderr=%q", boundary.module, runErr, stdout, stderr)
			}
		})
	}

	for _, boundary := range []struct {
		name   string
		module string
	}{
		{name: "disabled nearest Pkl declaration", module: "disabled"},
		{name: "empty nearest Pkl declaration", module: "empty"},
		{name: "directory-only nearest Pkl declaration", module: "directory-only"},
	} {
		boundary := boundary
		t.Run(boundary.name+" owns the complete selection", func(t *testing.T) {
			root := t.TempDir()
			ancestor := filepath.Join(root, "ancestor")
			child := filepath.Join(ancestor, "child")
			workingDirectory := child
			if boundary.module == "directory-only" {
				workingDirectory = filepath.Join(child, "nested")
			}
			if err := os.MkdirAll(workingDirectory, 0o700); err != nil {
				t.Fatal(err)
			}
			relativeWorkingDirectory, err := filepath.Rel(ancestor, workingDirectory)
			if err != nil {
				t.Fatal(err)
			}
			observedPath := filepath.ToSlash(filepath.Join(relativeWorkingDirectory, "README.md"))
			writeAcceptanceFile(t, filepath.Join(ancestor, contextDeclarationProjectFile), explicitBuiltinModule(), 0o600)
			writeAcceptanceFile(t, filepath.Join(ancestor, "ai-context.md"), fmt.Sprintf("---\nroot: true\ndocs:\n  - files: [%q]\n    message: Ancestor Pkl selection must be blocked.\n---\n", observedPath), 0o600)
			writeAcceptanceFile(t, filepath.Join(workingDirectory, "README.md"), "Pkl boundary witness\n", 0o600)
			writeAcceptanceFile(t, filepath.Join(child, contextDeclarationProjectFile), boundaryModule(boundary.module), 0o600)

			paths := contextDeclarationPaths(root)
			registerContextDaemonCleanup(t, paths)
			environment := contextAcceptanceEnvironment(root)

			parentStatusStdout, parentStatusStderr, parentStatusErr := runContextProcess(t, binary, ancestor, environment, nil, contextStatusArguments(paths))
			if parentStatusErr != nil || len(parentStatusStderr) != 0 {
				t.Fatalf("enabled ancestor status failed: err=%v stderr=%q stdout=%q", parentStatusErr, parentStatusStderr, parentStatusStdout)
			}
			assertContextDeclarationStatus(t, parentStatusStdout, "enabled", ancestor, "")
			childStatusStdout, childStatusStderr, childStatusErr := runContextProcess(t, binary, workingDirectory, environment, nil, contextStatusArguments(paths))
			if childStatusErr != nil || len(childStatusStderr) != 0 {
				t.Fatalf("blocked child status failed: err=%v stderr=%q stdout=%q", childStatusErr, childStatusStderr, childStatusStdout)
			}
			assertContextDeclarationStatus(t, childStatusStdout, "inactive", child, boundary.module)

			stdout, stderr, runErr := runContextProcess(t, binary, workingDirectory, environment,
				claudeContextHookPayload(workingDirectory, "pkl-boundary-session-"+boundary.module, "pkl-boundary-turn-"+boundary.module, ""),
				contextHookArguments(paths, "claude"))
			if runErr != nil || len(stderr) != 0 || len(stdout) != 0 {
				t.Fatalf("ancestor Pkl selection crossed nearest %s boundary: err=%v stdout=%q stderr=%q", boundary.module, runErr, stdout, stderr)
			}
		})
	}

	t.Run("init creates the project Pkl declaration", func(t *testing.T) {
		root := t.TempDir()
		paths := contextDeclarationPaths(root)
		environment := contextAcceptanceEnvironment(root)

		stdout, stderr, runErr := runContextProcess(t, binary, root, environment, nil, contextInitArguments(paths, root))
		if runErr != nil || len(stderr) != 0 {
			t.Fatalf("Pkl init failed: err=%v stderr=%q stdout=%q", runErr, stderr, stdout)
		}
		pklPath := filepath.Join(root, contextDeclarationProjectFile)
		contents, readErr := os.ReadFile(pklPath)
		if readErr != nil {
			t.Fatalf("init did not create %s: %v; stdout=%q", pklPath, readErr, stdout)
		}
		if !strings.Contains(string(contents), `amends "`+contextDeclarationSchemaURI+`"`) ||
			!strings.Contains(string(contents), `scope = "subtree"`) ||
			!strings.Contains(string(contents), `["project-guidance"] = new AiContext {}`) {
			t.Fatalf("init declaration has wrong public grammar: %q", contents)
		}
		if _, statErr := os.Stat(filepath.Join(root, ".workbench", "context.json")); !os.IsNotExist(statErr) {
			t.Fatalf("init created legacy JSON activation file: %v", statErr)
		}
	})

	t.Run("init ignores legacy JSON and preserves it", func(t *testing.T) {
		root := t.TempDir()
		legacyPath := filepath.Join(root, ".workbench", "context.json")
		legacy := `{"schemaVersion":1,"optIn":true,"includeChildren":true}`
		writeAcceptanceFile(t, legacyPath, legacy, 0o600)
		paths := contextDeclarationPaths(root)
		environment := contextAcceptanceEnvironment(root)

		stdout, stderr, runErr := runContextProcess(t, binary, root, environment, nil, contextInitArguments(paths, root))
		if runErr != nil || len(stderr) != 0 {
			t.Fatalf("init with legacy JSON failed: err=%v stderr=%q stdout=%q", runErr, stderr, stdout)
		}
		if _, err := os.Stat(filepath.Join(root, contextDeclarationProjectFile)); err != nil {
			t.Fatalf("init with legacy JSON did not create Pkl declaration: %v; stdout=%q", err, stdout)
		}
		if got := readAcceptanceFile(t, legacyPath); got != legacy {
			t.Fatalf("init rewrote legacy JSON: before=%q after=%q", legacy, got)
		}
	})
}

// TestContextDeclarationExpandedWitnesses is deliberately separate from the
// frozen red instrument above. It uses the real private-toolchain layout from
// buildContextWorkbench and exercises the remaining public behavior matrix.
func TestContextDeclarationExpandedWitnesses(t *testing.T) {
	moduleRoot, err := filepath.Abs("..")
	if err != nil {
		t.Fatal(err)
	}
	binary := buildContextWorkbench(t, moduleRoot)
	privatePkl := filepath.Join(filepath.Dir(filepath.Dir(binary)), "libexec", "workbench", "pkl")
	privateBytes, err := os.ReadFile(privatePkl)
	if err != nil {
		t.Fatalf("read staged private Pkl: %v", err)
	}

	t.Run("no source remains silent when private tools are absent", func(t *testing.T) {
		if err := os.Remove(privatePkl); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			_ = os.WriteFile(privatePkl, privateBytes, 0o755)
		})
		root := t.TempDir()
		paths := contextDeclarationPaths(root)
		registerContextDaemonCleanup(t, paths)
		stdout, stderr, runErr := runContextProcess(t, binary, root, contextAcceptanceEnvironment(root),
			claudeContextHookPayload(root, "missing-private-no-source", "turn", ""),
			contextHookArguments(paths, "claude"))
		if runErr != nil || len(stdout) != 0 || len(stderr) != 0 {
			t.Fatalf("no-source hook with missing private Pkl: err=%v stdout=%q stderr=%q", runErr, stdout, stderr)
		}
		for _, path := range contextDeclarationArtifacts(root, paths) {
			if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
				t.Fatalf("missing-private no-source hook created %q: %v", path, statErr)
			}
		}
	})

	t.Run("explicit source fails closed without private tools despite ambient Pkl", func(t *testing.T) {
		if err := os.Remove(privatePkl); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			_ = os.WriteFile(privatePkl, privateBytes, 0o755)
		})
		root := t.TempDir()
		writeAcceptanceFile(t, filepath.Join(root, contextDeclarationProjectFile), explicitBuiltinModule(), 0o600)
		paths := contextDeclarationPaths(root)
		registerContextDaemonCleanup(t, paths)
		environment := contextAcceptanceEnvironment(root)
		status, stderr, runErr := runContextProcess(t, binary, root, environment, nil, contextStatusArguments(paths))
		if runErr != nil || len(stderr) != 0 {
			t.Fatalf("missing-private status failed instead of reporting bounded invalid activation: err=%v stderr=%q stdout=%q", runErr, stderr, status)
		}
		assertContextDeclarationStatus(t, status, "invalid", "", "pkl")
		stdout, hookStderr, hookErr := runContextProcess(t, binary, root, environment,
			claudeContextHookPayload(root, "missing-private-source", "turn", ""), contextHookArguments(paths, "claude"))
		if hookErr != nil || len(stdout) != 0 || len(hookStderr) != 0 {
			t.Fatalf("missing-private source was not fail-open and silent: err=%v stdout=%q stderr=%q", hookErr, stdout, hookStderr)
		}
	})

	t.Run("home-only policy is inactive without a covered directory and stays warm-silent", func(t *testing.T) {
		root := t.TempDir()
		paths := contextDeclarationPaths(root)
		writeAcceptanceFile(t, paths.homeConfig, `amends "workbench:context-home"

limits {}
`, 0o600)
		registerContextDaemonCleanup(t, paths)
		environment := contextAcceptanceEnvironment(root)
		for index := 0; index < 2; index++ {
			stdout, stderr, runErr := runContextProcess(t, binary, root, environment,
				claudeContextHookPayload(root, fmt.Sprintf("home-only-%d", index), "turn", ""), contextHookArguments(paths, "claude"))
			if runErr != nil || len(stdout) != 0 || len(stderr) != 0 {
				t.Fatalf("home-only hook %d: err=%v stdout=%q stderr=%q", index, runErr, stdout, stderr)
			}
		}
		status, stderr, runErr := runContextProcess(t, binary, root, environment, nil, contextStatusArguments(paths))
		if runErr != nil || len(stderr) != 0 {
			t.Fatalf("home-only status: err=%v stderr=%q stdout=%q", runErr, stderr, status)
		}
		assertContextDeclarationStatus(t, status, "inactive", "", "home")
		if pids := contextDaemonPIDs(paths.socket); len(pids) != 0 {
			t.Fatalf("home-only inactive policy started daemon processes: %v", pids)
		}
		if _, err := os.Stat(paths.socket); !os.IsNotExist(err) {
			t.Fatalf("home-only inactive policy created runtime socket: %v", err)
		}
	})

	t.Run("import edits, deletion, restore, and snapshot disposal follow current bytes", func(t *testing.T) {
		root := t.TempDir()
		paths := contextDeclarationPaths(root)
		registerContextDaemonCleanup(t, paths)
		environment := contextAcceptanceEnvironment(root)
		writeAcceptanceFile(t, filepath.Join(root, "README.md"), "import witness\n", 0o600)
		writeAcceptanceFile(t, filepath.Join(root, "ai-context.md"), contextDeclarationDocument("Import enabled."), 0o600)
		writeAcceptanceFile(t, filepath.Join(root, "rules.pkl"), importedRules(true), 0o600)
		writeAcceptanceFile(t, filepath.Join(root, contextDeclarationProjectFile), importedProjectDeclaration("rules.pkl"), 0o600)

		stdout, stderr, runErr := runContextProcess(t, binary, root, environment,
			claudeContextHookPayload(root, "import-enabled", "turn", ""), contextHookArguments(paths, "claude"))
		if runErr != nil || len(stderr) != 0 {
			t.Fatalf("initial imported hook: err=%v stderr=%q", runErr, stderr)
		}
		assertContextHookOffer(t, stdout, "Import enabled.")
		initialStatus, stderr, runErr := runContextProcess(t, binary, root, environment, nil, contextStatusArguments(paths))
		if runErr != nil || len(stderr) != 0 {
			t.Fatalf("initial imported status: err=%v stderr=%q", runErr, stderr)
		}
		initialDigest := contextDeclarationStatusDigest(t, initialStatus)

		falseRules := importedRules(false)
		if len(falseRules) != len(importedRules(true)) {
			t.Fatalf("same-length import edit changed size: true=%d false=%d", len(importedRules(true)), len(falseRules))
		}
		writeAcceptanceFile(t, filepath.Join(root, "rules.pkl"), falseRules, 0o600)
		stdout, stderr, runErr = runContextProcess(t, binary, root, environment,
			claudeContextHookPayload(root, "import-disabled", "turn", ""), contextHookArguments(paths, "claude"))
		if runErr != nil || len(stdout) != 0 || len(stderr) != 0 {
			t.Fatalf("same-length disabled import was not silent: err=%v stdout=%q stderr=%q", runErr, stdout, stderr)
		}
		disabledStatus, stderr, runErr := runContextProcess(t, binary, root, environment, nil, contextStatusArguments(paths))
		if runErr != nil || len(stderr) != 0 {
			t.Fatalf("disabled imported status: err=%v stderr=%q", runErr, stderr)
		}
		assertContextDeclarationStatus(t, disabledStatus, "inactive", "", "disabled")
		if initialDigest == contextDeclarationStatusDigest(t, disabledStatus) {
			t.Fatalf("same-length imported edit retained stale status digest %q", initialDigest)
		}

		if err := os.Remove(filepath.Join(root, "rules.pkl")); err != nil {
			t.Fatal(err)
		}
		missingStatus, stderr, runErr := runContextProcess(t, binary, root, environment, nil, contextStatusArguments(paths))
		if runErr != nil || len(stderr) != 0 {
			t.Fatalf("deleted import status: err=%v stderr=%q", runErr, stderr)
		}
		assertContextDeclarationStatus(t, missingStatus, "invalid", "", "module")
		writeAcceptanceFile(t, filepath.Join(root, "rules.pkl"), importedRules(true), 0o600)
		stdout, stderr, runErr = runContextProcess(t, binary, root, environment,
			claudeContextHookPayload(root, "import-restored", "turn", ""), contextHookArguments(paths, "claude"))
		if runErr != nil || len(stderr) != 0 {
			t.Fatalf("restored imported hook: err=%v stderr=%q", runErr, stderr)
		}
		assertContextHookOffer(t, stdout, "Import enabled.")

		snapshot := contextDeclarationSnapshot(t, paths.cacheDir)
		contents, err := os.ReadFile(snapshot)
		if err != nil || len(contents) == 0 {
			t.Fatalf("read published declaration snapshot %q: %v", snapshot, err)
		}
		contents[len(contents)-1] ^= 0x01
		if err := os.WriteFile(snapshot, contents, 0o600); err != nil {
			t.Fatal(err)
		}
		stdout, stderr, runErr = runContextProcess(t, binary, root, environment,
			claudeContextHookPayload(root, "snapshot-corrupt", "turn", ""), contextHookArguments(paths, "claude"))
		if runErr != nil || len(stderr) != 0 {
			t.Fatalf("corrupt snapshot hook: err=%v stderr=%q", runErr, stderr)
		}
		assertContextHookOffer(t, stdout, "Import enabled.")
		if err := os.Remove(snapshot); err != nil {
			t.Fatal(err)
		}
		stdout, stderr, runErr = runContextProcess(t, binary, root, environment,
			claudeContextHookPayload(root, "snapshot-lost", "turn", ""), contextHookArguments(paths, "claude"))
		if runErr != nil || len(stderr) != 0 {
			t.Fatalf("lost snapshot hook: err=%v stderr=%q", runErr, stderr)
		}
		assertContextHookOffer(t, stdout, "Import enabled.")
	})

	t.Run("retargeted equal-size import changes selection", func(t *testing.T) {
		root := t.TempDir()
		paths := contextDeclarationPaths(root)
		registerContextDaemonCleanup(t, paths)
		environment := contextAcceptanceEnvironment(root)
		writeAcceptanceFile(t, filepath.Join(root, "README.md"), "retarget witness\n", 0o600)
		writeAcceptanceFile(t, filepath.Join(root, "ai-context.md"), contextDeclarationDocument("Retarget enabled."), 0o600)
		writeAcceptanceFile(t, filepath.Join(root, "rules-a.pkl"), importedRules(true), 0o600)
		writeAcceptanceFile(t, filepath.Join(root, "rules-b.pkl"), importedRules(false), 0o600)
		if len(importedRules(true)) != len(importedRules(false)) {
			t.Fatal("retarget modules are not equal-size")
		}
		if err := os.Symlink("rules-a.pkl", filepath.Join(root, "selected.pkl")); err != nil {
			t.Fatal(err)
		}
		writeAcceptanceFile(t, filepath.Join(root, contextDeclarationProjectFile), importedProjectDeclaration("selected.pkl"), 0o600)
		stdout, stderr, runErr := runContextProcess(t, binary, root, environment,
			claudeContextHookPayload(root, "retarget-a", "turn", ""), contextHookArguments(paths, "claude"))
		if runErr != nil || len(stderr) != 0 {
			t.Fatalf("initial retarget hook: err=%v stderr=%q", runErr, stderr)
		}
		assertContextHookOffer(t, stdout, "Retarget enabled.")
		if err := os.Remove(filepath.Join(root, "selected.pkl")); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink("rules-b.pkl", filepath.Join(root, "selected.pkl")); err != nil {
			t.Fatal(err)
		}
		stdout, stderr, runErr = runContextProcess(t, binary, root, environment,
			claudeContextHookPayload(root, "retarget-b", "turn", ""), contextHookArguments(paths, "claude"))
		if runErr != nil || len(stdout) != 0 || len(stderr) != 0 {
			t.Fatalf("retargeted disabled import was not silent: err=%v stdout=%q stderr=%q", runErr, stdout, stderr)
		}
	})

	t.Run("imported home exclusion changes activation", func(t *testing.T) {
		root := t.TempDir()
		paths := contextDeclarationPaths(root)
		registerContextDaemonCleanup(t, paths)
		environment := contextAcceptanceEnvironment(root)
		writeAcceptanceFile(t, filepath.Join(root, "README.md"), "home policy witness\n", 0o600)
		writeAcceptanceFile(t, filepath.Join(root, "ai-context.md"), contextDeclarationDocument("Home policy enabled."), 0o600)
		writeAcceptanceFile(t, filepath.Join(root, contextDeclarationProjectFile), explicitBuiltinModule(), 0o600)
		other := filepath.Join(root, "other")
		if err := os.MkdirAll(other, 0o700); err != nil {
			t.Fatal(err)
		}
		homeRulesPath := filepath.Join(filepath.Dir(paths.homeConfig), "home-rules.pkl")
		writeAcceptanceFile(t, homeRulesPath, homeRules(other), 0o600)
		writeAcceptanceFile(t, paths.homeConfig, importedHomeDeclaration(), 0o600)
		stdout, stderr, runErr := runContextProcess(t, binary, root, environment,
			claudeContextHookPayload(root, "home-allowed", "turn", ""), contextHookArguments(paths, "claude"))
		if runErr != nil || len(stderr) != 0 {
			t.Fatalf("home policy initial hook: err=%v stderr=%q", runErr, stderr)
		}
		if !bytes.Contains(stdout, []byte("Home policy enabled.")) {
			status, statusStderr, statusErr := runContextProcess(t, binary, root, environment, nil, contextStatusArguments(paths))
			t.Fatalf("home policy initial hook offered no guidance: err=%v stderr=%q stdout=%q statusErr=%v statusStderr=%q status=%q", runErr, stderr, stdout, statusErr, statusStderr, status)
		}
		writeAcceptanceFile(t, homeRulesPath, homeRules(root), 0o600)
		stdout, stderr, runErr = runContextProcess(t, binary, root, environment,
			claudeContextHookPayload(root, "home-excluded", "turn", ""), contextHookArguments(paths, "claude"))
		if runErr != nil || len(stdout) != 0 || len(stderr) != 0 {
			t.Fatalf("imported home exclusion leaked guidance: err=%v stdout=%q stderr=%q", runErr, stdout, stderr)
		}
		status, stderr, runErr := runContextProcess(t, binary, root, environment, nil, contextStatusArguments(paths))
		if runErr != nil || len(stderr) != 0 {
			t.Fatalf("home exclusion status: err=%v stderr=%q stdout=%q", runErr, stderr, status)
		}
		assertContextDeclarationStatus(t, status, "inactive", "", "exclu")
	})

	t.Run("reduced home cache cap becomes authoritative after prior population", func(t *testing.T) {
		root := t.TempDir()
		paths := contextDeclarationPaths(root)
		registerContextDaemonCleanup(t, paths)
		environment := contextAcceptanceEnvironment(root)
		writeAcceptanceFile(t, filepath.Join(root, "README.md"), "reduced cap witness\n", 0o600)
		writeAcceptanceFile(t, filepath.Join(root, "ai-context.md"), contextDeclarationDocument("Reduced cap guidance."), 0o600)
		writeAcceptanceFile(t, filepath.Join(root, contextDeclarationProjectFile), explicitBuiltinModule(), 0o600)
		writeAcceptanceFile(t, paths.homeConfig, homeCacheDeclaration(262144), 0o600)
		stdout, stderr, runErr := runContextProcess(t, binary, root, environment,
			claudeContextHookPayload(root, "reduced-cap-large", "turn", ""), contextHookArguments(paths, "claude"))
		if runErr != nil || len(stderr) != 0 {
			t.Fatalf("large-cap hook: err=%v stderr=%q", runErr, stderr)
		}
		assertContextHookOffer(t, stdout, "Reduced cap guidance.")
		largeStatus, stderr, runErr := runContextProcess(t, binary, root, environment, nil, contextStatusArguments(paths))
		if runErr != nil || len(stderr) != 0 || contextDeclarationCacheDiskCap(t, largeStatus) != 262144 {
			t.Fatalf("large cache policy was not effective: err=%v stderr=%q status=%q", runErr, stderr, largeStatus)
		}
		writeAcceptanceFile(t, paths.homeConfig, homeCacheDeclaration(65536), 0o600)
		smallStatus, stderr, runErr := runContextProcess(t, binary, root, environment, nil, contextStatusArguments(paths))
		if runErr != nil || len(stderr) != 0 || contextDeclarationCacheDiskCap(t, smallStatus) != 65536 {
			t.Fatalf("reduced cache policy was not authoritative: err=%v stderr=%q status=%q", runErr, stderr, smallStatus)
		}
		stdout, stderr, runErr = runContextProcess(t, binary, root, environment,
			claudeContextHookPayload(root, "reduced-cap-small", "turn", ""), contextHookArguments(paths, "claude"))
		if runErr != nil || len(stderr) != 0 {
			t.Fatalf("reduced-cap hook failed after policy reload: err=%v stderr=%q stdout=%q", runErr, stderr, stdout)
		}
		assertContextHookOffer(t, stdout, "Reduced cap guidance.")
	})

	t.Run("named builtins retain distinct identities after one is removed", func(t *testing.T) {
		root := t.TempDir()
		paths := contextDeclarationPaths(root)
		registerContextDaemonCleanup(t, paths)
		environment := contextAcceptanceEnvironment(root)
		writeAcceptanceFile(t, filepath.Join(root, "README.md"), "named builtin witness\n", 0o600)
		writeAcceptanceFile(t, filepath.Join(root, "ai-context.md"), contextDeclarationDocument("Named builtin guidance."), 0o600)
		declaration := namedBuiltinDeclaration(true)
		writeAcceptanceFile(t, filepath.Join(root, contextDeclarationProjectFile), declaration, 0o600)
		stdout, stderr, runErr := runContextProcess(t, binary, root, environment,
			claudeContextHookPayload(root, "named-first", "turn", ""), contextHookArguments(paths, "claude"))
		if runErr != nil || len(stderr) != 0 {
			t.Fatalf("named builtin hook: err=%v stderr=%q", runErr, stderr)
		}
		assertContextHookOffer(t, stdout, "Named builtin guidance.")
		status, stderr, runErr := runContextProcess(t, binary, root, environment, nil, contextStatusArguments(paths))
		if runErr != nil || len(stderr) != 0 || !strings.Contains(string(status), "guidance-alpha") || !strings.Contains(string(status), "guidance-beta") {
			t.Fatalf("named builtin identities were not exposed distinctly: err=%v stderr=%q status=%q", runErr, stderr, status)
		}
		writeAcceptanceFile(t, filepath.Join(root, contextDeclarationProjectFile), namedBuiltinDeclaration(false), 0o600)
		stdout, stderr, runErr = runContextProcess(t, binary, root, environment,
			claudeContextHookPayload(root, "named-after-remove", "turn", ""), contextHookArguments(paths, "claude"))
		if runErr != nil || len(stderr) != 0 {
			t.Fatalf("named builtin post-edit hook: err=%v stderr=%q", runErr, stderr)
		}
		assertContextHookOffer(t, stdout, "Named builtin guidance.")
		status, stderr, runErr = runContextProcess(t, binary, root, environment, nil, contextStatusArguments(paths))
		if runErr != nil || len(stderr) != 0 || !strings.Contains(string(status), "guidance-alpha") || strings.Contains(string(status), "guidance-beta") {
			t.Fatalf("named builtin removal did not change effective identities: err=%v stderr=%q status=%q", runErr, stderr, status)
		}
	})

	t.Run("profile-only executable contributes profile facts but receives no contribute call", func(t *testing.T) {
		root := t.TempDir()
		paths := contextDeclarationPaths(root)
		registerContextDaemonCleanup(t, paths)
		environment := contextAcceptanceEnvironment(root)
		profileProvider := filepath.Join(moduleRoot, "examples", "context", "profile-provider.py")
		writeAcceptanceFile(t, filepath.Join(root, contextDeclarationProjectFile), profileProjectDeclaration(profileProvider), 0o600)
		writeAcceptanceFile(t, paths.homeConfig, homeRuntimeDeclaration(5000), 0o600)
		stdout, stderr, runErr := runContextProcess(t, binary, root, environment,
			codexContextHookPayload(root, "profile-only", "turn", ""), contextHookArguments(paths, "codex"))
		if runErr != nil || len(stdout) != 0 || len(stderr) != 0 {
			t.Fatalf("profile-only hook emitted contribution or failed: err=%v stdout=%q stderr=%q", runErr, stdout, stderr)
		}
		status, stderr, runErr := runContextProcess(t, binary, root, environment, nil, contextStatusArguments(paths))
		if runErr != nil || len(stderr) != 0 || !strings.Contains(string(status), "example-profile") || !strings.Contains(string(status), "maintainer") {
			t.Fatalf("profile-only facts/provenance missing: err=%v stderr=%q status=%q", runErr, stderr, status)
		}
		if strings.Contains(string(status), "context.contribute") {
			t.Fatalf("profile-only status exposed a contribute dispatch: %s", status)
		}
	})

	t.Run("init follows fresh inherited legacy and authored-state rules", func(t *testing.T) {
		t.Run("fresh creates the public Pkl declaration", func(t *testing.T) {
			root := t.TempDir()
			paths := contextDeclarationPaths(root)
			stdout, stderr, runErr := runContextProcess(t, binary, root, contextAcceptanceEnvironment(root), nil, contextInitArguments(paths, root))
			if runErr != nil || len(stderr) != 0 {
				t.Fatalf("fresh init: err=%v stderr=%q stdout=%q", runErr, stderr, stdout)
			}
			var report struct {
				Created    bool `json:"created"`
				Activation struct {
					State string `json:"state"`
				} `json:"activation"`
			}
			decodeAcceptanceJSON(t, stdout, &report)
			if !report.Created || report.Activation.State != "enabled" {
				t.Fatalf("fresh init report = %s", stdout)
			}
		})

		t.Run("inherited ancestor is preserved", func(t *testing.T) {
			root := t.TempDir()
			ancestor := filepath.Join(root, "ancestor")
			child := filepath.Join(ancestor, "child")
			if err := os.MkdirAll(child, 0o700); err != nil {
				t.Fatal(err)
			}
			ancestorPath := filepath.Join(ancestor, contextDeclarationProjectFile)
			ancestorBytes := explicitBuiltinModule()
			writeAcceptanceFile(t, ancestorPath, ancestorBytes, 0o600)
			paths := contextDeclarationPaths(root)
			stdout, stderr, runErr := runContextProcess(t, binary, child, contextAcceptanceEnvironment(root), nil, contextInitArguments(paths, child))
			if runErr != nil || len(stderr) != 0 {
				t.Fatalf("inherited init: err=%v stderr=%q stdout=%q", runErr, stderr, stdout)
			}
			var report struct {
				Created bool `json:"created"`
			}
			decodeAcceptanceJSON(t, stdout, &report)
			if report.Created {
				t.Fatalf("inherited init created child declaration: %s", stdout)
			}
			if _, err := os.Stat(filepath.Join(child, contextDeclarationProjectFile)); !os.IsNotExist(err) {
				t.Fatalf("inherited init child declaration state: %v", err)
			}
			if got := readAcceptanceFile(t, ancestorPath); got != ancestorBytes {
				t.Fatalf("inherited init rewrote ancestor declaration: %q", got)
			}
		})

		t.Run("legacy-only state is inert and preserved", func(t *testing.T) {
			root := t.TempDir()
			legacyPath := filepath.Join(root, ".workbench", "context.json")
			legacy := `{"schemaVersion":1,"optIn":true,"includeChildren":true}`
			writeAcceptanceFile(t, legacyPath, legacy, 0o600)
			paths := contextDeclarationPaths(root)
			stdout, stderr, runErr := runContextProcess(t, binary, root, contextAcceptanceEnvironment(root), nil, contextInitArguments(paths, root))
			if runErr != nil || len(stderr) != 0 {
				t.Fatalf("legacy-only init: err=%v stderr=%q stdout=%q", runErr, stderr, stdout)
			}
			if got := readAcceptanceFile(t, legacyPath); got != legacy {
				t.Fatalf("legacy-only init rewrote JSON: %q", got)
			}
			if _, err := os.Stat(filepath.Join(root, contextDeclarationProjectFile)); err != nil {
				t.Fatalf("legacy-only init did not create Pkl declaration: %v", err)
			}
		})

		for _, state := range []struct {
			name string
			body string
		}{
			{name: "disabled", body: boundaryModule("disabled") + "// authored disabled\n"},
			{name: "empty", body: boundaryModule("empty") + "// authored empty\n"},
			{name: "invalid", body: "amends \"workbench:context\"\nthis is not Pkl\n"},
		} {
			state := state
			t.Run("preserves "+state.name, func(t *testing.T) {
				root := t.TempDir()
				path := filepath.Join(root, contextDeclarationProjectFile)
				writeAcceptanceFile(t, path, state.body, 0o600)
				paths := contextDeclarationPaths(root)
				stdout, stderr, runErr := runContextProcess(t, binary, root, contextAcceptanceEnvironment(root), nil, contextInitArguments(paths, root))
				if runErr != nil || len(stderr) != 0 {
					t.Fatalf("%s init: err=%v stderr=%q stdout=%q", state.name, runErr, stderr, stdout)
				}
				if got := readAcceptanceFile(t, path); got != state.body {
					t.Fatalf("%s init clobbered authored bytes: got=%q want=%q", state.name, got, state.body)
				}
			})
		}

		t.Run("home exclusion prevents local creation", func(t *testing.T) {
			root := t.TempDir()
			paths := contextDeclarationPaths(root)
			homeRulesPath := filepath.Join(filepath.Dir(paths.homeConfig), "home-rules.pkl")
			writeAcceptanceFile(t, homeRulesPath, homeRules(root), 0o600)
			writeAcceptanceFile(t, paths.homeConfig, importedHomeDeclaration(), 0o600)
			stdout, stderr, runErr := runContextProcess(t, binary, root, contextAcceptanceEnvironment(root), nil, contextInitArguments(paths, root))
			if runErr != nil || len(stderr) != 0 {
				t.Fatalf("home-excluded init: err=%v stderr=%q stdout=%q", runErr, stderr, stdout)
			}
			if _, err := os.Stat(filepath.Join(root, contextDeclarationProjectFile)); !os.IsNotExist(err) {
				t.Fatalf("home-excluded init created local declaration: %v", err)
			}
		})
	})

	t.Run("history retains the configured contributor across declaration revision", func(t *testing.T) {
		root := t.TempDir()
		paths := contextDeclarationPaths(root)
		registerContextDaemonCleanup(t, paths)
		environment := contextAcceptanceEnvironment(root)
		writeAcceptanceFile(t, filepath.Join(root, "README.md"), "history witness\n", 0o600)
		writeAcceptanceFile(t, filepath.Join(root, "ai-context.md"), contextDeclarationDocument("Historical guidance."), 0o600)
		writeAcceptanceFile(t, filepath.Join(root, contextDeclarationProjectFile), namedHistoryDeclaration(true), 0o600)
		stdout, stderr, runErr := runContextProcess(t, binary, root, environment,
			claudeContextHookPayload(root, "history-before", "turn", ""), contextHookArguments(paths, "claude"))
		if runErr != nil || len(stderr) != 0 {
			t.Fatalf("historical offer: err=%v stderr=%q", runErr, stderr)
		}
		assertContextHookOffer(t, stdout, "Historical guidance.")
		history := waitForContextHistory(t, binary, root, environment, paths, 3*time.Second)
		var contributionID uint64
		for _, record := range history.Records {
			if record.ContributionID != 0 {
				contributionID = record.ContributionID
				break
			}
		}
		if contributionID == 0 {
			t.Fatalf("history has no contribution identity: %+v", history.Records)
		}
		writeAcceptanceFile(t, filepath.Join(root, contextDeclarationProjectFile), namedHistoryDeclaration(false), 0o600)
		current, stderr, runErr := runContextProcess(t, binary, root, environment, nil, contextHistoryArguments(paths))
		if runErr != nil || len(stderr) != 0 {
			t.Fatalf("historical post-edit query: err=%v stderr=%q", runErr, stderr)
		}
		if !strings.Contains(string(current), "history-guidance") || !strings.Contains(string(current), "Historical guidance.") {
			t.Fatalf("history lost configured contributor/reason after revision: %s", current)
		}
		inspect, stderr, runErr := runContextProcess(t, binary, root, environment, nil, contextInspectContributionArguments(paths, contributionID))
		if runErr != nil || len(stderr) != 0 || !strings.Contains(string(inspect), "history-guidance") {
			t.Fatalf("historical inspect lost contributor identity: err=%v stderr=%q output=%q", runErr, stderr, inspect)
		}
	})
}

func contextDeclarationAcceptanceBinary(t *testing.T, moduleRoot string) string {
	t.Helper()
	if binary := os.Getenv("WORKBENCH_CONTEXT_DECLARATION_BINARY"); binary != "" {
		if _, err := os.Stat(binary); err != nil {
			t.Fatalf("WORKBENCH_CONTEXT_DECLARATION_BINARY %q is unavailable: %v", binary, err)
		}
		return binary
	}
	return buildContextWorkbench(t, moduleRoot)
}

func contextDeclarationPaths(root string) acceptanceContextPaths {
	paths := newAcceptanceContextPaths(root)
	paths.homeConfig = filepath.Join(root, "home", "workbench-context.pkl")
	return paths
}

func contextDeclarationArtifacts(root string, paths acceptanceContextPaths) []string {
	return []string{
		paths.homeConfig,
		paths.runtimeDir,
		paths.socket,
		paths.startLock,
		paths.serverLock,
		paths.cacheDir,
		filepath.Join(root, contextDeclarationProjectFile),
		filepath.Join(root, ".workbench", "context.json"),
	}
}

func copyContextDeclarationFixture(t *testing.T, moduleRoot, name, destination string) {
	t.Helper()
	source := filepath.Join(moduleRoot, "acceptance", "testdata", "context-declaration", name)
	err := filepath.WalkDir(source, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		target := filepath.Join(destination, relative)
		if entry.IsDir() {
			return os.MkdirAll(target, 0o700)
		}
		contents, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
			return err
		}
		return os.WriteFile(target, contents, info.Mode().Perm())
	})
	if err != nil {
		t.Fatalf("copy context declaration fixture %q: %v", name, err)
	}
}

func boundaryModule(name string) string {
	switch name {
	case "disabled":
		return fmt.Sprintf("amends %q\n\nenabled = false\nscope = \"subtree\"\ncontributors {\n  [\"project-guidance\"] = new AiContext {}\n}\n", contextDeclarationSchemaURI)
	case "empty":
		return fmt.Sprintf("amends %q\n\nscope = \"subtree\"\n", contextDeclarationSchemaURI)
	case "directory-only":
		return fmt.Sprintf("amends %q\n\nscope = \"directory\"\ncontributors {\n  [\"project-guidance\"] = new AiContext {}\n}\n", contextDeclarationSchemaURI)
	default:
		panic("unknown context declaration boundary " + name)
	}
}

func assertContextDeclarationStatus(t *testing.T, output []byte, wantState, wantRoot, wantReason string) {
	t.Helper()
	var report struct {
		Activation struct {
			State string `json:"state"`
			Scope struct {
				CanonicalRoot string `json:"canonicalRoot"`
			} `json:"scope"`
			Reasons []struct {
				Summary string `json:"summary"`
			} `json:"reasons"`
		} `json:"activation"`
	}
	decodeAcceptanceJSON(t, output, &report)
	if report.Activation.State != wantState {
		t.Fatalf("status state = %q, want %q; output=%s", report.Activation.State, wantState, output)
	}
	if wantRoot != "" && report.Activation.Scope.CanonicalRoot != wantRoot {
		t.Fatalf("status blocking origin = %q, want %q; output=%s", report.Activation.Scope.CanonicalRoot, wantRoot, output)
	}
	if wantReason != "" {
		for _, reason := range report.Activation.Reasons {
			if strings.Contains(strings.ToLower(reason.Summary), wantReason) {
				return
			}
		}
		t.Fatalf("status reasons lack %q; output=%s", wantReason, output)
	}
}

func explicitBuiltinModule() string {
	return fmt.Sprintf("amends %q\n\nscope = \"subtree\"\ncontributors {\n  [\"project-guidance\"] = new AiContext {}\n}\n", contextDeclarationSchemaURI)
}

func contextDeclarationDocument(message string) string {
	return fmt.Sprintf("---\nroot: true\ndocs:\n  - files: [\"README.md\"]\n    message: %q\n---\n", message)
}

func importedRules(enabled bool) string {
	if enabled {
		return "module context.rules\n\nenabled: Boolean = true\n//x\n"
	}
	return "module context.rules\n\nenabled: Boolean = false\n//\n"
}

func importedProjectDeclaration(module string) string {
	return fmt.Sprintf("amends %q\nimport \"context-local:/%s\" as policy\n\nenabled = policy.enabled\nscope = \"subtree\"\ncontributors {\n  [\"project-guidance\"] = new AiContext {}\n}\n", contextDeclarationSchemaURI, module)
}

func importedHomeDeclaration() string {
	return `amends "workbench:context-home"
import "context-local:/home-rules.pkl" as policy

exclusions {
  new Exclusion {
    root = policy.excludedRoot
    scope = "subtree"
  }
}
`
}

func homeRules(root string) string {
	return fmt.Sprintf("module context.homeRules\n\nexcludedRoot: String = %q\n", root)
}

func homeCacheDeclaration(capBytes uint64) string {
	return fmt.Sprintf(`amends "workbench:context-home"

limits {
  cache {
    diskCapBytes = %d
  }
}
`, capBytes)
}

func namedBuiltinDeclaration(includeBeta bool) string {
	contributors := "  [\"guidance-alpha\"] = new AiContext {}\n"
	if includeBeta {
		contributors += "  [\"guidance-beta\"] = new AiContext {}\n"
	}
	return fmt.Sprintf("amends %q\n\nscope = \"subtree\"\ncontributors {\n%s}\n", contextDeclarationSchemaURI, contributors)
}

func namedHistoryDeclaration(includeOld bool) string {
	name := "history-guidance-new"
	if includeOld {
		name = "history-guidance"
	}
	return fmt.Sprintf("amends %q\n\ncontributors {\n  [%q] = new AiContext {}\n}\n", contextDeclarationSchemaURI, name)
}

func contextDeclarationStatusDigest(t *testing.T, output []byte) string {
	t.Helper()
	var report struct {
		Activation struct {
			Effective struct {
				ConfigDigest string `json:"configDigest"`
			} `json:"effective"`
		} `json:"activation"`
	}
	decodeAcceptanceJSON(t, output, &report)
	return report.Activation.Effective.ConfigDigest
}

func contextDeclarationCacheDiskCap(t *testing.T, output []byte) uint64 {
	t.Helper()
	var report struct {
		Activation struct {
			Effective struct {
				Cache struct {
					DiskCapBytes uint64 `json:"diskCapBytes"`
				} `json:"cache"`
			} `json:"effective"`
		} `json:"activation"`
	}
	decodeAcceptanceJSON(t, output, &report)
	return report.Activation.Effective.Cache.DiskCapBytes
}

func contextDeclarationSnapshot(t *testing.T, cacheDir string) string {
	t.Helper()
	var snapshot string
	err := filepath.WalkDir(cacheDir, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() || filepath.Base(filepath.Dir(path)) != "snapshots" || !strings.HasSuffix(entry.Name(), ".snap") {
			return nil
		}
		snapshot = path
		return filepath.SkipDir
	})
	if err != nil {
		t.Fatalf("scan declaration snapshots: %v", err)
	}
	if snapshot == "" {
		t.Fatalf("no declaration snapshot found below %q", cacheDir)
	}
	return snapshot
}
