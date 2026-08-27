package acceptance

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestWorkbenchSkillsCheckPublicCommand(t *testing.T) {
	moduleRoot, err := filepath.Abs("..")
	if err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(t.TempDir(), "workbench")
	build := exec.Command("go", "build", "-trimpath", "-o", binary, "./cmd/workbench")
	build.Dir = moduleRoot
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build public Workbench CLI: %v\n%s", err, output)
	}

	t.Run("valid catalog", func(t *testing.T) {
		root := t.TempDir()
		writeSkillCheckSkill(t, root, "shared-tool", "general", "General shared tool.", "")
		initializeSkillsCheckRepository(t, root)
		before := skillCheckDirectoryDigest(t, root)
		result := runSkillsCheckWithoutToolchain(t, binary, root, "skills", "check")
		if result.exitCode != 0 {
			t.Fatalf("skills check exit = %d\nstderr:\n%s", result.exitCode, result.stderr)
		}
		if got, want := result.stdout, "1 skills · 0 composition edges · domain, link, and skill-reference contracts valid\n"; got != want {
			t.Fatalf("skills check stdout = %q, want %q", got, want)
		}
		if result.stderr != "" {
			t.Fatalf("valid skills check stderr = %q", result.stderr)
		}
		if after := skillCheckDirectoryDigest(t, root); after != before {
			t.Fatalf("successful skills check mutated its subject: before %s after %s", before, after)
		}
	})

	t.Run("warnings remain nonblocking", func(t *testing.T) {
		root := t.TempDir()
		writeSkillCheckSkill(t, root, "engineer", "engineering", "Engineering tool.", "Compose [`$planner`](../planner/SKILL.md).\n")
		writeSkillCheckSkill(t, root, "planner", "orchestration", "Planning tool.", "")
		initializeSkillsCheckRepository(t, root)
		before := skillCheckDirectoryDigest(t, root)
		result := runSkillsCheckWithoutToolchain(t, binary, root, "skills", "check")
		if result.exitCode != 0 {
			t.Fatalf("warning-only skills check exit = %d\nstderr:\n%s", result.exitCode, result.stderr)
		}
		if got, want := result.stdout, "2 skills · 1 composition edges · domain, link, and skill-reference contracts valid · 1 warning\n"; got != want {
			t.Fatalf("warning-only stdout = %q, want %q", got, want)
		}
		if !strings.Contains(result.stderr, "engineer/SKILL.md:7: warning:") {
			t.Fatalf("warning lacks source-relative location: %q", result.stderr)
		}
		if after := skillCheckDirectoryDigest(t, root); after != before {
			t.Fatalf("warning-only skills check mutated its subject: before %s after %s", before, after)
		}
	})

	for _, test := range []struct {
		name      string
		prepare   func(*testing.T, string)
		wantPath  string
		wantIssue string
		wantCount int
	}{
		{
			name: "malformed frontmatter",
			prepare: func(t *testing.T, root string) {
				writeFile(t, filepath.Join(root, ".agents", "skills", "invalid", "SKILL.md"), "---\nname: invalid\nmetadata:\n  domain: invented\n---\n")
			},
			wantPath:  "invalid/SKILL.md:1:",
			wantIssue: "frontmatter must declare name and a valid metadata.domain",
			wantCount: 1,
		},
		{
			name: "composition label",
			prepare: func(t *testing.T, root string) {
				writeSkillCheckSkill(t, root, "engineer", "engineering", "Engineering tool.", "Compose [planner](../planner/SKILL.md).\n")
				writeSkillCheckSkill(t, root, "planner", "general", "Planning support.", "")
			},
			wantPath:  "engineer/SKILL.md:7:",
			wantIssue: "composition link to \"planner\" must name \"$planner\" in its label",
			wantCount: 2,
		},
		{
			name: "missing local link",
			prepare: func(t *testing.T, root string) {
				writeSkillCheckSkill(t, root, "engineer", "engineering", "Engineering tool.", "Read [the detailed guide](references/missing.md).\n")
			},
			wantPath:  "engineer/SKILL.md:7:",
			wantIssue: "missing link target references/missing.md",
			wantCount: 1,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			test.prepare(t, root)
			initializeSkillsCheckRepository(t, root)
			before := skillCheckDirectoryDigest(t, root)
			result := runSkillsCheckWithoutToolchain(t, binary, root, "skills", "check")
			if result.exitCode != 1 {
				t.Fatalf("invalid skills check exit = %d\nstdout:\n%s\nstderr:\n%s", result.exitCode, result.stdout, result.stderr)
			}
			if result.stdout != "" {
				t.Fatalf("invalid skills check stdout = %q", result.stdout)
			}
			if !strings.Contains(result.stderr, test.wantPath) || !strings.Contains(result.stderr, test.wantIssue) {
				t.Fatalf("diagnostic lacks source repair fact:\n%s", result.stderr)
			}
			repair := fmt.Sprintf("Fix the %d listed skill contract %s, then rerun 'workbench skills check'.", test.wantCount, pluralAcceptance(test.wantCount, "violation", "violations"))
			if !strings.Contains(result.stderr, repair) {
				t.Fatalf("diagnostic lacks repair action:\n%s", result.stderr)
			}
			if strings.Contains(result.stderr, "workbench: skills check:") {
				t.Fatalf("subject failure repeated a generic diagnostic tail:\n%s", result.stderr)
			}
			if after := skillCheckDirectoryDigest(t, root); after != before {
				t.Fatalf("skills check mutated its subject: before %s after %s", before, after)
			}
		})
	}

	t.Run("warnings precede issues and ordering is stable", func(t *testing.T) {
		root := t.TempDir()
		writeSkillCheckSkill(t, root, "alpha", "engineering", "Alpha tool.", "Compose [`$zeta`](../zeta/SKILL.md).\nRead [missing](missing.md).\n")
		writeSkillCheckSkill(t, root, "zeta", "orchestration", "Zeta tool.", "")
		first := runSkillsCheckWithoutToolchain(t, binary, root, "skills", "check")
		second := runSkillsCheckWithoutToolchain(t, binary, root, "skills", "check")
		if first.exitCode != 1 || second.exitCode != 1 {
			t.Fatalf("ordered invalid checks exited %d and %d", first.exitCode, second.exitCode)
		}
		if first.stderr != second.stderr {
			t.Fatalf("diagnostic order changed:\nfirst:\n%s\nsecond:\n%s", first.stderr, second.stderr)
		}
		warning := strings.Index(first.stderr, ": warning:")
		issue := strings.Index(first.stderr, "missing link target missing.md")
		if warning < 0 || issue < 0 || warning > issue {
			t.Fatalf("warnings did not precede issues:\n%s", first.stderr)
		}
	})

	t.Run("flags are rejected before command authority", func(t *testing.T) {
		root := t.TempDir()
		writeSkillCheckSkill(t, root, "shared-tool", "general", "General shared tool.", "")
		initializeSkillsCheckRepository(t, root)
		before := skillCheckDirectoryDigest(t, root)
		result := runSkillsCheckWithoutToolchain(t, binary, root, "skills", "check", "--root", ".agents/skills")
		if result.exitCode != 1 || result.stdout != "" {
			t.Fatalf("flagged invocation = exit %d stdout %q stderr %q", result.exitCode, result.stdout, result.stderr)
		}
		if !strings.Contains(result.stderr, "usage: workbench") || !strings.Contains(result.stderr, "skills check") {
			t.Fatalf("flagged invocation lacks exact usage: %q", result.stderr)
		}
		if after := skillCheckDirectoryDigest(t, root); after != before {
			t.Fatalf("invalid invocation mutated its subject: before %s after %s", before, after)
		}
	})
}

func pluralAcceptance(count int, singular, plural string) string {
	if count == 1 {
		return singular
	}
	return plural
}

func initializeSkillsCheckRepository(t *testing.T, root string) {
	t.Helper()
	git(t, root, "init", "-b", "main")
	git(t, root, "config", "user.name", "Workbench Skills Check")
	git(t, root, "config", "user.email", "workbench-skills@example.invalid")
	git(t, root, "add", ".agents/skills")
	git(t, root, "commit", "-m", "Add skill catalog")
}

type skillsCheckResult struct {
	stdout   string
	stderr   string
	exitCode int
}

func runSkillsCheckWithoutToolchain(t *testing.T, binary, root string, arguments ...string) skillsCheckResult {
	t.Helper()
	command := exec.Command(binary, arguments...)
	command.Dir = root
	command.Env = []string{"PATH=/workbench-no-ambient-toolchain"}
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	err := command.Run()
	if err == nil {
		return skillsCheckResult{stdout: stdout.String(), stderr: stderr.String()}
	}
	var exitError *exec.ExitError
	if !errors.As(err, &exitError) {
		t.Fatalf("run Workbench skills check: %v", err)
	}
	return skillsCheckResult{stdout: stdout.String(), stderr: stderr.String(), exitCode: exitError.ExitCode()}
}

func writeSkillCheckSkill(t *testing.T, root, name, domain, description, body string) {
	t.Helper()
	writeFile(t, filepath.Join(root, ".agents", "skills", name, "SKILL.md"), fmt.Sprintf(`---
name: %s
description: %s
metadata:
  domain: %s
---
%s`, name, description, domain, body))
}

func skillCheckDirectoryDigest(t *testing.T, root string) string {
	t.Helper()
	hash := sha256.New()
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		fmt.Fprintf(hash, "%s\x00%s\x00", filepath.ToSlash(relative), info.Mode())
		if entry.Type()&os.ModeSymlink != 0 {
			target, err := os.Readlink(path)
			if err != nil {
				return err
			}
			hash.Write([]byte(target))
			return nil
		}
		if entry.IsDir() {
			return nil
		}
		contents, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		hash.Write(contents)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return hex.EncodeToString(hash.Sum(nil))
}
