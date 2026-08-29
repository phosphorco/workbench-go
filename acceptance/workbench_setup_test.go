package acceptance

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestWorkbenchLocalMeaningfulSlice(t *testing.T) {
	moduleRoot, err := filepath.Abs("..")
	if err != nil {
		t.Fatal(err)
	}

	schemaURI := func(name string) string {
		return "workbench-contract:/0.6.0/" + name
	}

	fixtureRoot := t.TempDir()
	remotes := filepath.Join(fixtureRoot, "remotes")
	if err := os.MkdirAll(remotes, 0o700); err != nil {
		t.Fatal(err)
	}

	createRemote(t, fixtureRoot, remotes, "community-packages", fmt.Sprintf(`amends %q

scope = "@phosphorco"
packages {
  ["@phosphorco/math"] {
    exports {
      ["."] = "./src/index.ts"
    }
  }
}
`, schemaURI("PackageScopeRepository.pkl")), map[string]string{
		".gitignore":        "math/package.json\nmath/tsconfig.json\nmath/dist/\n.agents/skills/\n",
		"math/src/index.ts": "export const answer = 42\n",
		"skills/domain-skill/SKILL.md": `---
name: domain-skill
description: Exercise one engineering-domain resource skill and its explicit composition edge.
metadata:
  domain: engineering
---

Compose [` + "`$composition-dependency`" + `](../composition-dependency/SKILL.md).
`,
		"skills/composition-dependency/SKILL.md": `---
name: composition-dependency
description: Supply the explicit composition dependency used by the domain skill.
metadata:
  domain: general
---

Required composition support.
`,
	})
	createRemote(t, fixtureRoot, remotes, "basindb", fmt.Sprintf(`amends %q

scope = "@basindb"

includes {
	["phosphorco/community-packages"] {
    skills {
      editing {
        domains = Set("engineering")
      }
    }
  }
}

packages {
  ["@basindb/client"] {
    dependencies {
      ["kleur"] = "4.1.5"
    }
    devDependencies {
      ["typescript"] = "5.9.3"
    }
  }
}
`, schemaURI("PackageScopeRepository.pkl")), map[string]string{
		".gitignore": "client/package.json\nclient/tsconfig.json\nclient/dist/\n.agents/skills/\n",
		"client/src/index.ts": `import { answer } from "@phosphorco/math"
export const basinAnswer = answer
`,
		"client/test/index.test.js": `import { expect, test } from "bun:test"
import { basinAnswer } from "../src/index.ts"

test("assembled package", () => expect(basinAnswer).toBe(42))
`,
	})

	workbench := filepath.Join(fixtureRoot, "workbench")
	if err := os.Mkdir(workbench, 0o700); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(workbench, ".gitignore"), `workbench-subject.pkl
AGENTS.md
.workbench/
bun.lock
node_modules/
package.json
tsconfig.json
pkg/
repos/
`)
	git(t, workbench, "init", "-b", "main")
	git(t, workbench, "config", "user.email", "workbench-acceptance@example.invalid")
	git(t, workbench, "config", "user.name", "Workbench Acceptance")
	git(t, workbench, "add", ".gitignore")
	git(t, workbench, "commit", "-m", "initialize local workbench context")
	writeFile(t, filepath.Join(workbench, "workbench-subject.pkl"), fmt.Sprintf(`amends %q

workLine {
  branch = "local/meaningful-slice"
  baseBranch = "main"
}

entrypoints {
  "https://github.com/phosphorco/basindb"
}
`, schemaURI("WorkbenchSubject.pkl")))

	binary := filepath.Join(t.TempDir(), "workbench")
	build := exec.Command("go", "build", "-trimpath", "-o", binary, "./cmd/workbench")
	build.Dir = moduleRoot
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build public workbench CLI: %v\n%s", err, output)
	}

	setup := exec.Command(binary, "setup")
	setup.Dir = workbench
	setup.Env = append(os.Environ(),
		"BUN_INSTALL_CACHE_DIR="+filepath.Join(fixtureRoot, "bun-cache"),
		"GIT_ALLOW_PROTOCOL=file",
		"GIT_CONFIG_COUNT=1",
		"GIT_CONFIG_KEY_0=url.file://"+filepath.ToSlash(remotes)+"/.insteadOf",
		"GIT_CONFIG_VALUE_0=https://github.com/phosphorco/",
		"GIT_TERMINAL_PROMPT=0",
		"MISE_CACHE_DIR="+filepath.Join(fixtureRoot, "mise-cache"),
		"MISE_CONFIG_FILE="+filepath.Join(moduleRoot, "mise.toml"),
		"MISE_LOG_LEVEL=error",
		"MISE_STATE_DIR="+filepath.Join(fixtureRoot, "mise-state"),
		"MISE_TRUSTED_CONFIG_PATHS="+filepath.Join(moduleRoot, "mise.toml"),
		"TMPDIR="+filepath.Join(fixtureRoot, "tmp"),
	)
	if err := os.MkdirAll(filepath.Join(fixtureRoot, "tmp"), 0o700); err != nil {
		t.Fatal(err)
	}
	if output, err := setup.CombinedOutput(); err != nil {
		t.Fatalf("workbench setup through the public CLI: %v\n%s", err, output)
	}

	for _, checkout := range []string{"pkg/@basindb", "pkg/@phosphorco"} {
		checkoutPath := filepath.Join(workbench, filepath.FromSlash(checkout))
		if branch := strings.TrimSpace(git(t, checkoutPath, "branch", "--show-current")); branch != "local/meaningful-slice" {
			t.Errorf("%s branch = %q, want %q", checkout, branch, "local/meaningful-slice")
		}
	}
	basinManifest := readFile(t, filepath.Join(workbench, "pkg/@basindb/client/package.json"))
	if !strings.Contains(basinManifest, `"@phosphorco/math": "workspace:*"`) {
		t.Fatalf("BasinDB manifest lacks derived workspace adjacency:\n%s", basinManifest)
	}
	rootManifest := readFile(t, filepath.Join(workbench, "package.json"))
	if !strings.Contains(rootManifest, `"typescript": "5.9.3"`) {
		t.Fatalf("root manifest lacks derived TypeScript tool authority:\n%s", rootManifest)
	}
	if !strings.Contains(rootManifest, `"kleur": "4.1.5"`) {
		t.Fatalf("root manifest lacks reassembled external dependency:\n%s", rootManifest)
	}
	for _, skill := range []string{"domain-skill", "composition-dependency"} {
		if _, err := os.Stat(filepath.Join(workbench, ".agents/skills", skill, "SKILL.md")); err != nil {
			t.Fatalf("projected skill %q: %v", skill, err)
		}
	}
	linkedPackage, err := filepath.EvalSymlinks(filepath.Join(workbench, "pkg/@basindb/client/node_modules/@phosphorco/math"))
	if err != nil {
		t.Fatalf("resolve linked cross-repository package: %v", err)
	}
	wantPackage := filepath.Join(workbench, "pkg/@phosphorco/math")
	if linkedPackage != wantPackage {
		t.Fatalf("linked package = %q, want %q", linkedPackage, wantPackage)
	}
	spawnedScript := filepath.Join(workbench, "repos/.alchemy-local/generated/deploy.ts")
	writeFile(t, spawnedScript, `import kleur from "kleur"
console.log(kleur.green("spawned-root-resolution"))
`)
	spawned := exec.Command("bun", spawnedScript)
	spawned.Dir = workbench
	spawned.Env = setup.Env
	spawnedOutput, err := spawned.CombinedOutput()
	if err != nil || !strings.Contains(string(spawnedOutput), "spawned-root-resolution") {
		t.Fatalf("spawned root script external resolution: %v\n%s", err, spawnedOutput)
	}

	sourceChange := filepath.Join(workbench, "pkg/@basindb/pre-existing-source-change.ts")
	writeFile(t, sourceChange, "export const preserved = true\n")
	before := git(t, filepath.Join(workbench, "pkg/@basindb"), "status", "--porcelain=v1", "--untracked-files=all")
	second := exec.Command(binary, "check")
	second.Dir = workbench
	second.Env = setup.Env
	secondOutput, err := second.CombinedOutput()
	if err != nil {
		t.Fatalf("one-command checkout-to-test workflow: %v\n%s", err, secondOutput)
	}
	if !strings.Contains(string(secondOutput), "0 generated paths changed") {
		t.Fatalf("check setup phase did not report convergence:\n%s", secondOutput)
	}
	if !strings.Contains(string(secondOutput), "Code health passed: typecheck and test.") {
		t.Fatalf("check did not report code-health success:\n%s", secondOutput)
	}
	after := git(t, filepath.Join(workbench, "pkg/@basindb"), "status", "--porcelain=v1", "--untracked-files=all")
	if before != after || !strings.Contains(after, "pre-existing-source-change.ts") {
		t.Fatalf("Git-owned source status changed: before %q after %q", before, after)
	}
}

func createRemote(t *testing.T, fixtureRoot string, remotes string, name string, resource string, files map[string]string) {
	t.Helper()
	seed := filepath.Join(fixtureRoot, "seeds", name)
	if err := os.MkdirAll(seed, 0o700); err != nil {
		t.Fatal(err)
	}
	git(t, seed, "init", "-b", "main")
	git(t, seed, "config", "user.email", "workbench-acceptance@example.invalid")
	git(t, seed, "config", "user.name", "Workbench Acceptance")
	writeFile(t, filepath.Join(seed, "workbench.pkl"), resource)
	for path, contents := range files {
		writeFile(t, filepath.Join(seed, filepath.FromSlash(path)), contents)
	}
	git(t, seed, "add", ".")
	git(t, seed, "commit", "-m", "declare local workbench resource")
	git(t, fixtureRoot, "clone", "--bare", seed, filepath.Join(remotes, name))
}

func git(t *testing.T, workingDirectory string, arguments ...string) string {
	t.Helper()
	command := exec.Command("git", arguments...)
	command.Dir = workingDirectory
	command.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_TERMINAL_PROMPT=0")
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(arguments, " "), err, output)
	}
	return string(output)
}

func writeFile(t *testing.T, path string, contents string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(contents)
}
