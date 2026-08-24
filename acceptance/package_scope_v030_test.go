package acceptance

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

const (
	v030SubjectContract      = "workbench-contract:/0.3.0/WorkbenchSubject.pkl"
	v030PackageScopeContract = "workbench-contract:/0.3.0/PackageScopeRepository.pkl"
	v030RepositoryContract   = "workbench-contract:/0.3.0/Repository.pkl"
	v030AgentContract        = "workbench-contract:/0.3.0/AgentInstructions.pkl"
)

func TestPackageScopeV030KeepsPackagesNestedAndResourcesIndependent(t *testing.T) {
	fixture := newPackageScopeFixture(t)
	createRemote(t, fixture.root, fixture.remotes, "library", fmt.Sprintf(`amends %q

packages {
  ["@workbench-library/shared"] {}
}
`, v030RepositoryContract), map[string]string{
		".gitignore": `/package.json
/tsconfig.json
/dist/
/node_modules/
/bun.lock
/.agents/skills/
`,
		"src/index.ts": `export const sharedMessage: string = "nested library"
`,
		"skills/library-engineering/SKILL.md": `---
name: library-engineering
description: Exercise resource-root skill projection.
metadata:
  domain: engineering
---

# Library engineering
`,
	})
	createRemote(t, fixture.root, fixture.remotes, "entry", fmt.Sprintf(`amends %q

scope = "@workbench-entry"

includes {
  ["phosphorco/library"] {
    skills {
      editing {
        names = Set("library-engineering")
      }
      workbench {
        names = Set("library-engineering")
      }
    }
  }
}

packages {
  ["@workbench-entry/app"] {
    requiredButNotReferenced {
      ["typescript"] = "^5.9.2"
    }
  }
  ["@workbench-entry/tool"] {}
}
`, v030PackageScopeContract), map[string]string{
		".gitignore": `/app/package.json
/app/tsconfig.json
/app/dist/
/app/node_modules/
/tool/package.json
/tool/tsconfig.json
/tool/dist/
/tool/node_modules/
/.agents/skills/
`,
		"app/src/index.ts": `import { sharedMessage } from "@workbench-library/shared"

export const entryMessage = ` + "`entry: ${sharedMessage}`" + `
`,
		"tool/src/index.ts": `export const toolMessage: string = "nested tool"
`,
		"skills/entry-source/SKILL.md": `---
name: entry-source
description: Prove skill source remains at the resource root.
metadata:
  domain: general
---

# Entry source
`,
	})

	workbench := fixture.newWorld(t, "nested", v030SubjectContract, v030AgentContract, "entry")
	first := fixture.runSetup(t, workbench, true)
	if !strings.Contains(first, "Workbench reconciled 2 repositories") {
		t.Fatalf("setup did not assemble both resources:\n%s", first)
	}

	entry := filepath.Join(workbench, "pkg", "@workbench-entry")
	app := filepath.Join(entry, "app")
	tool := filepath.Join(entry, "tool")
	library := filepath.Join(workbench, "repos", "library")
	assertFileContents(t, filepath.Join(app, "src", "index.ts"), `import { sharedMessage } from "@workbench-library/shared"

export const entryMessage = `+"`entry: ${sharedMessage}`"+`
`)
	assertFileContents(t, filepath.Join(tool, "src", "index.ts"), `export const toolMessage: string = "nested tool"
`)
	for _, path := range []string{
		filepath.Join(app, "package.json"), filepath.Join(app, "tsconfig.json"),
		filepath.Join(tool, "package.json"), filepath.Join(tool, "tsconfig.json"),
		filepath.Join(library, "package.json"), filepath.Join(library, "tsconfig.json"),
	} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("expected package-owned projection %q: %v", path, err)
		}
	}
	for _, rootProjection := range []string{filepath.Join(entry, "package.json"), filepath.Join(entry, "tsconfig.json")} {
		if _, err := os.Stat(rootProjection); !os.IsNotExist(err) {
			t.Fatalf("PackageScope resource root acquired package projection %q: %v", rootProjection, err)
		}
	}

	var rootManifest struct {
		Workspaces []string `json:"workspaces"`
	}
	decodePackageScopeJSON(t, filepath.Join(workbench, "package.json"), &rootManifest)
	wantWorkspaces := []string{"pkg/@workbench-entry/app", "pkg/@workbench-entry/tool", "repos/library"}
	if !slices.Equal(rootManifest.Workspaces, wantWorkspaces) {
		t.Fatalf("workspaces = %#v, want %#v", rootManifest.Workspaces, wantWorkspaces)
	}
	var appManifest struct {
		Name         string            `json:"name"`
		Dependencies map[string]string `json:"dependencies"`
	}
	decodePackageScopeJSON(t, filepath.Join(app, "package.json"), &appManifest)
	if appManifest.Name != "@workbench-entry/app" || appManifest.Dependencies["@workbench-library/shared"] != "workspace:*" {
		t.Fatalf("nested app manifest = %#v", appManifest)
	}
	var toolManifest struct {
		Name string `json:"name"`
	}
	decodePackageScopeJSON(t, filepath.Join(tool, "package.json"), &toolManifest)
	if toolManifest.Name != "@workbench-entry/tool" {
		t.Fatalf("nested tool manifest name = %q", toolManifest.Name)
	}

	linked, err := filepath.EvalSymlinks(filepath.Join(app, "node_modules", "@workbench-library", "shared"))
	if err != nil {
		t.Fatalf("resolve nested app workspace link: %v", err)
	}
	wantLinked, err := filepath.EvalSymlinks(library)
	if err != nil {
		t.Fatal(err)
	}
	if linked != wantLinked {
		t.Fatalf("nested app workspace link = %q, want Repository root %q", linked, wantLinked)
	}

	compiler := filepath.Join(app, "node_modules", ".bin", "tsc")
	typecheck := exec.Command(compiler, "-b", filepath.Join(workbench, "tsconfig.json"), "--pretty", "false")
	typecheck.Dir = workbench
	typecheck.Env = fixture.environment
	if output, err := typecheck.CombinedOutput(); err != nil {
		t.Fatalf("cross-shape nested package typecheck: %v\n%s", err, output)
	}
	for _, output := range []string{filepath.Join(app, "dist", "index.js"), filepath.Join(tool, "dist", "index.js"), filepath.Join(library, "dist", "index.js")} {
		if _, err := os.Stat(output); err != nil {
			t.Fatalf("package build output did not remain under its package root %q: %v", output, err)
		}
	}
	if _, err := os.Stat(filepath.Join(entry, "dist")); !os.IsNotExist(err) {
		t.Fatalf("PackageScope resource root acquired relocated build output: %v", err)
	}

	for _, skill := range []string{
		filepath.Join(entry, "skills", "entry-source", "SKILL.md"),
		filepath.Join(entry, ".agents", "skills", "library-engineering", "SKILL.md"),
		filepath.Join(workbench, ".agents", "skills", "library-engineering", "SKILL.md"),
	} {
		if _, err := os.Stat(skill); err != nil {
			t.Fatalf("resource-root skill path %q: %v", skill, err)
		}
	}
	for _, packageSkillProjection := range []string{filepath.Join(app, ".agents"), filepath.Join(tool, ".agents")} {
		if _, err := os.Stat(packageSkillProjection); !os.IsNotExist(err) {
			t.Fatalf("package child acquired resource projection %q: %v", packageSkillProjection, err)
		}
	}
	orientation := readFile(t, filepath.Join(workbench, "AGENTS.md"))
	if !strings.Contains(orientation, "pkg/@workbench-entry") || strings.Contains(orientation, "pkg/@workbench-entry/app") {
		t.Fatalf("orientation did not retain resource-root topology:\n%s", orientation)
	}
	for _, repository := range []string{entry, library} {
		if status := git(t, repository, "status", "--porcelain=v1", "--untracked-files=all"); status != "" {
			t.Fatalf("generated package outputs appear as authored changes in %q:\n%s", repository, status)
		}
	}

	before := digestPackageScopeProjection(t, workbench)
	second := fixture.runSetup(t, workbench, true)
	if !strings.Contains(second, "0 generated paths changed") {
		t.Fatalf("second nested setup did not converge:\n%s", second)
	}
	if after := digestPackageScopeProjection(t, workbench); after != before {
		t.Fatalf("nested projection changed on second setup: before %s after %s", before, after)
	}
}

func TestPackageScopeV030RefusesNonCanonicalLayoutsBeforeGeneratedMutation(t *testing.T) {
	tests := []struct {
		name  string
		files map[string]string
		want  string
	}{
		{name: "missing child", files: map[string]string{}, want: "requires canonical source directory"},
		{name: "root source", files: map[string]string{"src/index.ts": "export const misplaced = true\n"}, want: "non-canonical source layout"},
		{name: "ambiguous canonical and root source", files: map[string]string{
			"app/src/index.ts": "export const canonical = true\n",
			"src/index.ts":     "export const ambiguous = true\n",
		}, want: "non-canonical source layout"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newPackageScopeFixture(t)
			files := map[string]string{".gitignore": "/app/package.json\n/app/tsconfig.json\n/app/node_modules/\n/app/dist/\n"}
			for path, contents := range test.files {
				files[path] = contents
			}
			createRemote(t, fixture.root, fixture.remotes, "entry", fmt.Sprintf(`amends %q

scope = "@workbench-entry"
packages {
  ["@workbench-entry/app"] {}
}
`, v030PackageScopeContract), files)
			workbench := fixture.newWorld(t, "invalid", v030SubjectContract, v030AgentContract, "entry")
			sentinels := map[string]string{
				"package.json":                          "outer manifest must survive\n",
				"tsconfig.json":                         "outer tsconfig must survive\n",
				"AGENTS.md":                             "outer orientation must survive\n",
				".agents/skills/context-owned/SKILL.md": "context skill must survive\n",
			}
			for path, contents := range sentinels {
				writeFile(t, filepath.Join(workbench, filepath.FromSlash(path)), contents)
			}

			output := fixture.runSetup(t, workbench, false)
			if !strings.Contains(output, test.want) || !strings.Contains(output, "@workbench-entry/app") {
				t.Fatalf("wrong topology refusal:\n%s", output)
			}
			for path, contents := range sentinels {
				assertFileContents(t, filepath.Join(workbench, filepath.FromSlash(path)), contents)
			}
			entry := filepath.Join(workbench, "pkg", "@workbench-entry")
			for _, generated := range []string{
				filepath.Join(entry, "package.json"), filepath.Join(entry, "tsconfig.json"),
				filepath.Join(entry, "app", "package.json"), filepath.Join(entry, "app", "tsconfig.json"),
				filepath.Join(entry, ".agents"), filepath.Join(workbench, ".workbench", "world.json"),
				filepath.Join(workbench, "bun.lock"), filepath.Join(workbench, "node_modules"),
			} {
				if _, err := os.Stat(generated); !os.IsNotExist(err) {
					t.Fatalf("refused topology mutated generated path %q: %v", generated, err)
				}
			}
		})
	}
}

func TestPackageScopeHistoricalRootLayoutsRemainVersionBound(t *testing.T) {
	for _, version := range []string{"0.1.0", "0.2.0"} {
		t.Run(version, func(t *testing.T) {
			fixture := newPackageScopeFixture(t)
			createRemote(t, fixture.root, fixture.remotes, "entry", fmt.Sprintf(`amends %q

scope = "@historical"
packages {
  ["@historical/app"] {}
}
`, "workbench-contract:/"+map[string]string{"0.1.0": "", "0.2.0": "0.2.0/"}[version]+"PackageScopeRepository.pkl"), map[string]string{
				".gitignore":   "/package.json\n/tsconfig.json\n/node_modules/\n/dist/\n",
				"src/index.ts": "export const historical = true\n",
			})
			subjectURI := "workbench-contract:/WorkbenchSubject.pkl"
			if version == "0.2.0" {
				subjectURI = "workbench-contract:/0.2.0/WorkbenchSubject.pkl"
			}
			workbench := fixture.newWorld(t, "historical", subjectURI, "", "entry")
			fixture.runSetup(t, workbench, true)
			checkout := filepath.Join(workbench, "pkg", "@historical")
			for _, generated := range []string{filepath.Join(checkout, "package.json"), filepath.Join(checkout, "tsconfig.json")} {
				if _, err := os.Stat(generated); err != nil {
					t.Fatalf("%s historical root projection %q: %v", version, generated, err)
				}
			}
			if _, err := os.Stat(filepath.Join(checkout, "app", "package.json")); !os.IsNotExist(err) {
				t.Fatalf("%s historical package was relocated: %v", version, err)
			}
		})
	}
}

type packageScopeFixture struct {
	root        string
	remotes     string
	binary      string
	environment []string
}

func newPackageScopeFixture(t *testing.T) packageScopeFixture {
	t.Helper()
	root := t.TempDir()
	remotes := filepath.Join(root, "remotes")
	if err := os.MkdirAll(remotes, 0o700); err != nil {
		t.Fatal(err)
	}
	temporary := filepath.Join(root, "tmp")
	if err := os.MkdirAll(temporary, 0o700); err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(root, "workbench")
	build := exec.Command("go", "build", "-trimpath", "-o", binary, "./cmd/workbench")
	build.Dir = filepath.Clean("..")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build Workbench CLI: %v\n%s", err, output)
	}
	environment := append(os.Environ(),
		"BUN_INSTALL_CACHE_DIR="+filepath.Join(root, "bun-cache"),
		"GIT_ALLOW_PROTOCOL=file",
		"GIT_CONFIG_COUNT=1",
		"GIT_CONFIG_KEY_0=url.file://"+filepath.ToSlash(remotes)+"/.insteadOf",
		"GIT_CONFIG_VALUE_0=https://github.com/phosphorco/",
		"GIT_TERMINAL_PROMPT=0",
		"TMPDIR="+temporary,
	)
	return packageScopeFixture{root: root, remotes: remotes, binary: binary, environment: environment}
}

func (fixture packageScopeFixture) newWorld(t *testing.T, name, subjectContract, agentContract, entry string) string {
	t.Helper()
	root := filepath.Join(fixture.root, name)
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(root, ".gitignore"), `workbench-subject.pkl
AGENTS.md
.agents/skills/
.workbench/
bun.lock
node_modules/
package.json
tsconfig.json
pkg/
repos/
`)
	writeFile(t, filepath.Join(root, "workbench-subject.pkl"), fmt.Sprintf(`amends %q

workLine {
  branch = "local/package-scope-v030"
  baseBranch = "main"
}
entrypoints {
  %q
}
`, subjectContract, "https://github.com/phosphorco/"+entry))
	if agentContract != "" {
		writeFile(t, filepath.Join(root, "AGENTS.pkl"), fmt.Sprintf("amends %q\n\nprose = \"PackageScope acceptance orientation.\"\n", agentContract))
	}
	return root
}

func (fixture packageScopeFixture) runSetup(t *testing.T, root string, wantSuccess bool) string {
	t.Helper()
	command := exec.Command(fixture.binary, "setup")
	command.Dir = root
	command.Env = fixture.environment
	output, err := command.CombinedOutput()
	if wantSuccess && err != nil {
		t.Fatalf("Workbench setup: %v\n%s", err, output)
	}
	if !wantSuccess && err == nil {
		t.Fatalf("invalid PackageScope topology unexpectedly reconciled:\n%s", output)
	}
	return string(output)
}

func decodePackageScopeJSON(t *testing.T, path string, target any) {
	t.Helper()
	encoded, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(encoded, target); err != nil {
		t.Fatalf("decode %q: %v", path, err)
	}
}

func assertFileContents(t *testing.T, path, want string) {
	t.Helper()
	if got := readFile(t, path); got != want {
		t.Fatalf("%s changed:\nwant %q\n got %q", path, want, got)
	}
}

func digestPackageScopeProjection(t *testing.T, root string) string {
	t.Helper()
	var paths []string
	for _, relative := range []string{
		"AGENTS.md", "package.json", "tsconfig.json", ".agents/skills",
		"pkg/@workbench-entry/app/package.json", "pkg/@workbench-entry/app/tsconfig.json",
		"pkg/@workbench-entry/tool/package.json", "pkg/@workbench-entry/tool/tsconfig.json",
		"pkg/@workbench-entry/.agents/skills",
		"repos/library/package.json", "repos/library/tsconfig.json",
	} {
		path := filepath.Join(root, filepath.FromSlash(relative))
		info, err := os.Stat(path)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		if !info.IsDir() {
			paths = append(paths, path)
			continue
		}
		if err := filepath.WalkDir(path, func(current string, entry os.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if !entry.IsDir() {
				paths = append(paths, current)
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	slices.Sort(paths)
	hash := sha256.New()
	for _, path := range paths {
		relative, err := filepath.Rel(root, path)
		if err != nil {
			t.Fatal(err)
		}
		hash.Write([]byte(filepath.ToSlash(relative)))
		hash.Write([]byte{0})
		contents, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		hash.Write(contents)
		hash.Write([]byte{0})
	}
	return hex.EncodeToString(hash.Sum(nil))
}
