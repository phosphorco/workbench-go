package acceptance

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// This fixture is intentionally version-scoped: the exact released 0.2 and
// 0.3 snapshot identity below is immutable compatibility input, not current
// Workbench vocabulary.
func TestCurrentBinaryReproducesExactReleasedV020V030Snapshots(t *testing.T) {
	for _, release := range []struct {
		version              string
		subjectContract      string
		packageScopeContract string
		canonicalSource      string
	}{
		{
			version:              "0.2.0",
			subjectContract:      "workbench-contract:/0.2.0/WorkbenchSubject.pkl",
			packageScopeContract: "workbench-contract:/0.2.0/PackageScopeRepository.pkl",
			canonicalSource:      "src/index.ts",
		},
		{
			version:              "0.3.0",
			subjectContract:      legacyV030SubjectContract,
			packageScopeContract: legacyV030PackageScopeContract,
			canonicalSource:      "app/src/index.ts",
		},
	} {
		t.Run(release.version, func(t *testing.T) {
			fixture := newPackageScopeFixture(t)
			createRemote(t, fixture.root, fixture.remotes, "entry", fmt.Sprintf(`amends %q

scope = "@workbench-entry"
packages {
  ["@workbench-entry/app"] {}
}
`, release.packageScopeContract), map[string]string{
				".gitignore":            "/package.json\n/tsconfig.json\n/app/package.json\n/app/tsconfig.json\n/node_modules/\n/dist/\n/app/node_modules/\n/app/dist/\n",
				release.canonicalSource: "export const compatible = true\n",
			})
			workbench := fixture.newWorkbench(t, "released-snapshot-"+release.version, release.subjectContract, "", "entry")
			fixture.runSetup(t, workbench, true)
			checkout := filepath.Join(workbench, "pkg", "@workbench-entry")
			git(t, checkout, "remote", "set-url", "origin", "https://github.com/phosphorco/entry")
			head := strings.TrimSpace(git(t, checkout, "rev-parse", "HEAD"))
			legacySnapshot := filepath.Join(workbench, "released-snapshot.pkl")
			contents := fmt.Sprintf(`amends "package://github.com/phosphorco/workbench-go/releases/download/%s/workbench@%s#/WorkbenchWorldSnapshot.pkl"

resources {
  ["@workbench-entry"] {
    shape = new PackageScopeShape { scope = "@workbench-entry" }
    github = "phosphorco/entry"
    canonicalPath = "pkg/@workbench-entry"
    commit = %q
  }
}
`, release.version, release.version, head)
			writeFile(t, legacySnapshot, contents)

			command := exec.Command(fixture.binary, "snapshot", "reproduce", "released-snapshot.pkl")
			command.Dir = workbench
			command.Env = withoutFixtureRemoteRewrite(fixture.environment)
			output, err := command.CombinedOutput()
			if err != nil {
				t.Fatalf("reproduce released %s snapshot: %v\n%s", release.version, err, output)
			}
			if !strings.Contains(string(output), "Reproduced and verified 1 exact repository") {
				t.Fatalf("released %s snapshot report:\n%s", release.version, output)
			}
			after, err := os.ReadFile(legacySnapshot)
			if err != nil {
				t.Fatal(err)
			}
			if string(after) != contents {
				t.Fatalf("released %s user snapshot was rewritten", release.version)
			}
		})
	}
}

func withoutFixtureRemoteRewrite(environment []string) []string {
	filtered := make([]string, 0, len(environment))
	for _, variable := range environment {
		if strings.HasPrefix(variable, "GIT_CONFIG_COUNT=") ||
			strings.HasPrefix(variable, "GIT_CONFIG_KEY_0=") ||
			strings.HasPrefix(variable, "GIT_CONFIG_VALUE_0=") {
			continue
		}
		filtered = append(filtered, variable)
	}
	return filtered
}
