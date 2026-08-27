package setup

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/phosphorco/workbench-go/internal/contract"
	"github.com/phosphorco/workbench-go/internal/evaluate"
	"github.com/phosphorco/workbench-go/internal/skills"
)

func TestGeneratedLocalContractsMatchPublishedSourceCandidates(t *testing.T) {
	for path, generated := range map[string]string{
		"../../pkl/WorkbenchSubject.pkl":       localSubjectContract,
		"../../pkl/PackageScopeRepository.pkl": localRepositoryContract,
	} {
		contents, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if string(contents) != generated {
			t.Fatalf("generated contract is stale for %s", path)
		}
	}
}

func TestSchemaForSourceDiscriminatesReleasedAndCurrentPackageScopeContracts(t *testing.T) {
	for _, test := range []struct {
		uri     string
		version string
	}{
		{uri: localV020PackageScopeURI, version: "0.2.0"},
		{uri: localV030PackageScopeURI, version: "0.3.0"},
		{uri: localV040PackageScopeURI, version: "0.4.0"},
		{uri: localV050PackageScopeURI, version: "0.5.0"},
	} {
		source := []byte(fmt.Sprintf("amends %q\n", test.uri))
		if _, version, err := schemaForSource(source, "PackageScopeRepository.pkl"); err != nil {
			t.Fatalf("%s schema: %v", test.version, err)
		} else if version != test.version {
			t.Fatalf("%s URI selected %q", test.version, version)
		}
	}
	if _, version, err := schemaForSource([]byte(`amends "package://github.com/phosphorco/workbench-go/releases/download/0.3.0/workbench@0.3.0#/PackageScopeRepository.pkl"`), "PackageScopeRepository.pkl"); err != nil {
		t.Fatal(err)
	} else if version != "0.3.0" {
		t.Fatalf("released 0.3 package selected %q", version)
	}
	if _, version, err := schemaForSource([]byte(`amends "package://github.com/phosphorco/workbench-go/releases/download/0.4.0/workbench@0.4.0#/PackageScopeRepository.pkl"`), "PackageScopeRepository.pkl"); err != nil {
		t.Fatal(err)
	} else if version != "0.4.0" {
		t.Fatalf("released 0.4 package selected %q", version)
	}
	if _, version, err := schemaForSource([]byte(`amends "package://github.com/phosphorco/workbench-go/releases/download/0.5.0/workbench@0.5.0#/PackageScopeRepository.pkl"`), "PackageScopeRepository.pkl"); err != nil {
		t.Fatal(err)
	} else if version != "0.5.0" {
		t.Fatalf("released 0.5 package selected %q", version)
	}
	if _, _, err := schemaForSource([]byte(`amends "package://example.invalid/releases/download/0.4.0/workbench@0.4.0#/PackageScopeRepository.pkl"`), "PackageScopeRepository.pkl"); err == nil {
		t.Fatal("foreign package URI was accepted by release-shaped substring")
	}
	if _, version, err := schemaForSource([]byte(`amends "workbench-contract:/0.5.0/WorkbenchSubject.pkl"`), "WorkbenchSubject.pkl"); err != nil {
		t.Fatal(err)
	} else if version != "0.5.0" {
		t.Fatalf("local current Subject selected %q", version)
	}
}

func TestObservePackagesV030AndLaterUseOneNestedLawForSingletonAndMultiplePackages(t *testing.T) {
	root := t.TempDir()
	resourceRoot := filepath.Join(root, "pkg", "@workbench-entry")
	write(t, filepath.Join(resourceRoot, "app", "src", "index.ts"), "export const app = true\n")
	write(t, filepath.Join(resourceRoot, "tool", "src", "index.ts"), "export const tool = true\n")

	for _, version := range []string{"0.3.0", "0.4.0", "0.5.0"} {
		resource := Resource{
			Identity:      "@workbench-entry",
			CanonicalPath: "pkg/@workbench-entry",
			Shape:         contract.ResourceShape{Kind: contract.PackageScopeShape, Scope: "@workbench-entry"},
			Packages:      map[string]contract.PackagePolicy{"@workbench-entry/app": {}},
		}
		singleton, err := observePackages(root, []Resource{resource}, version)
		if err != nil {
			t.Fatal(err)
		}
		if len(singleton) != 1 || filepath.ToSlash(singleton[0].Directory) != "pkg/@workbench-entry/app" {
			t.Fatalf("%s singleton packages = %#v", version, singleton)
		}

		resource.Packages["@workbench-entry/tool"] = contract.PackagePolicy{}
		multiple, err := observePackages(root, []Resource{resource}, version)
		if err != nil {
			t.Fatal(err)
		}
		got := make(map[string]string, len(multiple))
		for _, pkg := range multiple {
			got[pkg.Name] = filepath.ToSlash(pkg.Directory)
		}
		want := map[string]string{
			"@workbench-entry/app":  "pkg/@workbench-entry/app",
			"@workbench-entry/tool": "pkg/@workbench-entry/tool",
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("%s multiple package directories = %#v, want %#v", version, got, want)
		}
		if got["@workbench-entry/app"] != filepath.ToSlash(singleton[0].Directory) {
			t.Fatalf("%s adding a package relocated app from %q to %q", version, singleton[0].Directory, got["@workbench-entry/app"])
		}
	}
}

func TestObservePackagesRoutesPackageScopePlacementByExactContractVersion(t *testing.T) {
	root := t.TempDir()
	resourceRoot := filepath.Join(root, "pkg", "@workbench-entry")
	write(t, filepath.Join(resourceRoot, "src", "index.ts"), "export const historical = true\n")
	resource := Resource{
		Identity:      "@workbench-entry",
		CanonicalPath: "pkg/@workbench-entry",
		Shape:         contract.ResourceShape{Kind: contract.PackageScopeShape, Scope: "@workbench-entry"},
		Packages:      map[string]contract.PackagePolicy{"@workbench-entry/app": {}},
	}
	for _, version := range []string{"0.1.0", "0.2.0"} {
		packages, err := observePackages(root, []Resource{resource}, version)
		if err != nil {
			t.Fatalf("%s historical placement: %v", version, err)
		}
		if got := filepath.ToSlash(packages[0].Directory); got != "pkg/@workbench-entry" {
			t.Fatalf("%s package directory = %q, want historical resource root", version, got)
		}
	}
	if _, err := observePackages(root, []Resource{resource}, "0.3.0"); err == nil || !strings.Contains(err.Error(), `has non-canonical source layout(s) ["src"]; requires "app/src"`) {
		t.Fatalf("0.3.0 root-layout refusal = %v", err)
	}
}

func TestObservePackagesV030RetainsDistinctRepositoryPlacement(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "repos", "workbench-fixture-library", "src", "index.ts"), "export const library = true\n")
	resource := Resource{
		Identity:      "phosphorco/workbench-fixture-library",
		CanonicalPath: "repos/workbench-fixture-library",
		Shape:         contract.ResourceShape{Kind: contract.RepositoryShape},
		Packages:      map[string]contract.PackagePolicy{"@workbench-library/shared": {}},
	}
	packages, err := observePackages(root, []Resource{resource}, "0.3.0")
	if err != nil {
		t.Fatal(err)
	}
	if got := filepath.ToSlash(packages[0].Directory); got != "repos/workbench-fixture-library" {
		t.Fatalf("Repository package directory = %q", got)
	}
}

func TestObservePackagesV030RefusesNonCanonicalLayoutsWithoutGeneratedMutation(t *testing.T) {
	tests := []struct {
		name       string
		sourceDirs []string
		want       string
	}{
		{name: "missing", want: `PackageScope package "@workbench-entry/app" requires canonical source directory "app/src"`},
		{name: "root", sourceDirs: []string{"src"}, want: `PackageScope package "@workbench-entry/app" has non-canonical source layout(s) ["src"]; requires "app/src"`},
		{name: "guessed packages directory", sourceDirs: []string{"packages/app/src"}, want: `PackageScope package "@workbench-entry/app" has non-canonical source layout(s) ["packages/app/src"]; requires "app/src"`},
		{name: "ambiguous", sourceDirs: []string{"app/src", "src", "packages/app/src"}, want: `PackageScope package "@workbench-entry/app" has non-canonical source layout(s) ["src" "packages/app/src"]; requires "app/src"`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			resourceRoot := filepath.Join(root, "pkg", "@workbench-entry")
			if err := os.MkdirAll(resourceRoot, 0o755); err != nil {
				t.Fatal(err)
			}
			for _, sourceDir := range test.sourceDirs {
				write(t, filepath.Join(resourceRoot, filepath.FromSlash(sourceDir), "index.ts"), "export const source = true\n")
			}
			write(t, filepath.Join(root, "package.json"), "preserve root manifest\n")
			write(t, filepath.Join(resourceRoot, "package.json"), "preserve resource manifest\n")
			beforeRoot := mustRead(t, filepath.Join(root, "package.json"))
			beforeResource := mustRead(t, filepath.Join(resourceRoot, "package.json"))
			resource := Resource{
				Identity:      "@workbench-entry",
				CanonicalPath: "pkg/@workbench-entry",
				Shape:         contract.ResourceShape{Kind: contract.PackageScopeShape, Scope: "@workbench-entry"},
				Packages:      map[string]contract.PackagePolicy{"@workbench-entry/app": {}},
			}
			_, err := observePackages(root, []Resource{resource}, "0.3.0")
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want %q", err, test.want)
			}
			if after := mustRead(t, filepath.Join(root, "package.json")); !slices.Equal(after, beforeRoot) {
				t.Fatal("refusal changed root generated output")
			}
			if after := mustRead(t, filepath.Join(resourceRoot, "package.json")); !slices.Equal(after, beforeResource) {
				t.Fatal("refusal changed resource generated output")
			}
		})
	}
}

func TestRunWithV030RefusesRootPackageLayoutBeforeGeneratedMutation(t *testing.T) {
	root := t.TempDir()
	remotes := filepath.Join(root, "remotes")
	if err := os.MkdirAll(remotes, 0o755); err != nil {
		t.Fatal(err)
	}
	createRemote(t, root, remotes, "workbench-fixture-entry", fmt.Sprintf("amends %q\n\nscope = \"@workbench-entry\"\npackages { [\"@workbench-entry/app\"] {} }\n", localV030PackageScopeURI), map[string]string{
		"src/index.ts": "export const rootShortcut = true\n",
	})
	t.Setenv("GIT_ALLOW_PROTOCOL", "file")
	t.Setenv("GIT_CONFIG_COUNT", "1")
	t.Setenv("GIT_CONFIG_KEY_0", "url.file://"+filepath.ToSlash(remotes)+"/.insteadOf")
	t.Setenv("GIT_CONFIG_VALUE_0", "https://github.com/phosphorco/")
	workbench := filepath.Join(root, "workbench")
	if err := os.MkdirAll(workbench, 0o755); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(workbench, "workbench-subject.pkl"), fmt.Sprintf("amends %q\n\nworkLine { branch = \"workbench/v030-invalid\"; baseBranch = \"main\" }\nentrypoints { \"https://github.com/phosphorco/workbench-fixture-entry\" }\n", localV030SubjectURI))
	write(t, filepath.Join(workbench, "package.json"), "preserve root projection\n")
	before := mustRead(t, filepath.Join(workbench, "package.json"))

	pkl, err := exec.LookPath("pkl")
	if err != nil {
		t.Skip("pkl unavailable")
	}
	bun, err := exec.LookPath("bun")
	if err != nil {
		t.Skip("bun unavailable")
	}
	evaluator, err := evaluate.NewEvaluator(pkl)
	if err != nil {
		t.Fatal(err)
	}
	_, err = RunWith(context.Background(), workbench, NewToolchain(evaluator, bun))
	if err == nil || !strings.Contains(err.Error(), `has non-canonical source layout(s) ["src"]; requires "app/src"`) {
		t.Fatalf("root shortcut refusal = %v", err)
	}
	if after := mustRead(t, filepath.Join(workbench, "package.json")); !slices.Equal(after, before) {
		t.Fatal("invalid PackageScope topology changed root generated projection")
	}
	for _, path := range []string{
		"pkg/@workbench-entry/app/package.json",
		"pkg/@workbench-entry/app/tsconfig.json",
		"tsconfig.json",
	} {
		if _, err := os.Stat(filepath.Join(workbench, filepath.FromSlash(path))); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("invalid PackageScope topology created %s: %v", path, err)
		}
	}
}

func TestRunWithV030ReturnsEvaluatedContractVersion(t *testing.T) {
	root := t.TempDir()
	temporary := filepath.Join(root, "tmp")
	if err := os.MkdirAll(temporary, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TMPDIR", temporary)
	t.Setenv("BUN_INSTALL_CACHE_DIR", filepath.Join(root, "bun-cache"))
	remotes := filepath.Join(root, "remotes")
	if err := os.MkdirAll(remotes, 0o755); err != nil {
		t.Fatal(err)
	}
	createRemote(t, root, remotes, "workbench-fixture-entry", fmt.Sprintf("amends %q\n\nscope = \"@workbench-entry\"\npackages { [\"@workbench-entry/app\"] {} }\n", localV030PackageScopeURI), map[string]string{
		".gitignore":       "app/package.json\napp/tsconfig.json\napp/dist/\n.agents/skills/\n",
		"app/src/index.ts": "export const nested = true\n",
	})
	t.Setenv("GIT_ALLOW_PROTOCOL", "file")
	t.Setenv("GIT_CONFIG_COUNT", "1")
	t.Setenv("GIT_CONFIG_KEY_0", "url.file://"+filepath.ToSlash(remotes)+"/.insteadOf")
	t.Setenv("GIT_CONFIG_VALUE_0", "https://github.com/phosphorco/")
	workbench := filepath.Join(root, "workbench")
	if err := os.MkdirAll(workbench, 0o755); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(workbench, "workbench-subject.pkl"), fmt.Sprintf("amends %q\n\nworkLine { branch = \"workbench/v030\"; baseBranch = \"main\" }\nentrypoints { \"https://github.com/phosphorco/workbench-fixture-entry\" }\n", localV030SubjectURI))
	write(t, filepath.Join(workbench, "AGENTS.pkl"), fmt.Sprintf("amends %q\n\nprose = \"Version witness.\"\n", localV030AgentInstructionsURI))
	pkl, err := exec.LookPath("pkl")
	if err != nil {
		t.Skip("pkl unavailable")
	}
	bun, err := exec.LookPath("bun")
	if err != nil {
		t.Skip("bun unavailable")
	}
	evaluator, err := evaluate.NewEvaluator(pkl)
	if err != nil {
		t.Fatal(err)
	}
	result, err := RunWith(context.Background(), workbench, NewToolchain(evaluator, bun))
	if err != nil {
		t.Fatal(err)
	}
	if result.ContractVersion != "0.3.0" {
		t.Fatalf("contract version = %q, want 0.3.0", result.ContractVersion)
	}
	if _, err := os.Stat(filepath.Join(workbench, "pkg", "@workbench-entry", "app", "tsconfig.json")); err != nil {
		t.Fatalf("nested package projection: %v", err)
	}
}

func TestObservePackagesV030RefusesSymlinkedCanonicalDirectoriesWithoutOutsideMutation(t *testing.T) {
	tests := []struct {
		name string
		link func(t *testing.T, resourceRoot, outside string)
		want string
	}{
		{
			name: "package leaf",
			link: func(t *testing.T, resourceRoot, outside string) {
				t.Helper()
				if err := os.Symlink(outside, filepath.Join(resourceRoot, "app")); err != nil {
					t.Fatal(err)
				}
			},
			want: `canonical package directory "app" must be a real directory, not a symlink`,
		},
		{
			name: "source directory",
			link: func(t *testing.T, resourceRoot, outside string) {
				t.Helper()
				if err := os.Mkdir(filepath.Join(resourceRoot, "app"), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(filepath.Join(outside, "src"), filepath.Join(resourceRoot, "app", "src")); err != nil {
					t.Fatal(err)
				}
			},
			want: `canonical source directory "app/src" must be a real directory, not a symlink`,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			resourceRoot := filepath.Join(root, "pkg", "@workbench-entry")
			if err := os.MkdirAll(resourceRoot, 0o755); err != nil {
				t.Fatal(err)
			}
			outside := t.TempDir()
			write(t, filepath.Join(outside, "src", "index.ts"), "export const outside = true\n")
			write(t, filepath.Join(outside, "sentinel.txt"), "outside must remain byte-identical\n")
			test.link(t, resourceRoot, outside)
			beforeOutside := readTree(t, outside)
			resource := Resource{
				Identity:      "@workbench-entry",
				CanonicalPath: "pkg/@workbench-entry",
				Shape:         contract.ResourceShape{Kind: contract.PackageScopeShape, Scope: "@workbench-entry"},
				Packages:      map[string]contract.PackagePolicy{"@workbench-entry/app": {}},
			}
			_, err := observePackages(root, []Resource{resource}, "0.3.0")
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("symlink refusal = %v, want %q", err, test.want)
			}
			if afterOutside := readTree(t, outside); !reflect.DeepEqual(afterOutside, beforeOutside) {
				t.Fatalf("symlink refusal mutated outside tree:\nbefore=%#v\nafter=%#v", beforeOutside, afterOutside)
			}
			for _, generated := range []string{"package.json", "tsconfig.json"} {
				if _, err := os.Stat(filepath.Join(outside, generated)); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("symlink refusal created outside %s: %v", generated, err)
				}
			}
		})
	}
}

func TestRunAssemblesClosureConvergesAndPreservesSource(t *testing.T) {
	fixture := newSetupFixture(t)
	first, err := Run(context.Background(), fixture.workbench)
	if err != nil {
		t.Fatal(err)
	}
	if first.ContractVersion != "0.1.0" {
		t.Fatalf("contract version = %q, want 0.1.0", first.ContractVersion)
	}
	if len(first.Resources) != 2 {
		t.Fatalf("resources = %#v", first.Resources)
	}
	for _, path := range []string{"pkg/@basindb", "pkg/@phosphorco"} {
		if branch := git(t, filepath.Join(fixture.workbench, filepath.FromSlash(path)), "branch", "--show-current"); branch != "local/meaningful-slice" {
			t.Fatalf("%s branch = %q", path, branch)
		}
	}
	sourcePath := filepath.Join(fixture.workbench, "pkg/@basindb/source-owned.txt")
	if err := os.WriteFile(sourcePath, []byte("preserve me\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	before := git(t, filepath.Join(fixture.workbench, "pkg/@basindb"), "status", "--porcelain=v1", "--untracked-files=all")
	second, err := Run(context.Background(), fixture.workbench)
	if err != nil {
		t.Fatal(err)
	}
	if len(second.ChangedPaths) != 0 {
		t.Fatalf("second setup changed %#v", second.ChangedPaths)
	}
	after := git(t, filepath.Join(fixture.workbench, "pkg/@basindb"), "status", "--porcelain=v1", "--untracked-files=all")
	if before != after || !strings.Contains(after, "source-owned.txt") {
		t.Fatalf("source status changed: before %q after %q", before, after)
	}
}

func TestRunRefusesDirtyOtherBranchWithoutCanonicalGitMutation(t *testing.T) {
	fixture := newSetupFixture(t)
	if _, err := Run(context.Background(), fixture.workbench); err != nil {
		t.Fatal(err)
	}
	basindb := filepath.Join(fixture.workbench, "pkg/@basindb")
	git(t, basindb, "checkout", "main")
	if err := os.WriteFile(filepath.Join(basindb, "dirty.txt"), []byte("keep\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	before := gitSnapshot(t, basindb)
	if _, err := Run(context.Background(), fixture.workbench); err == nil || !strings.Contains(err.Error(), "dirty checkout") {
		t.Fatalf("unsafe setup error = %v", err)
	}
	after := gitSnapshot(t, basindb)
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("canonical Git state mutated:\nbefore=%#v\nafter=%#v", before, after)
	}
}

func TestReadManagedCheckoutsRejectsTamperedDeletionProvenance(t *testing.T) {
	tests := map[string]string{
		"trailing JSON":            `{"version":1,"resources":[]} {}`,
		"shape identity mismatch":  `{"version":1,"resources":[{"identity":"someone/else","github":"phosphorco/workbench-fixture-entry","shape":{"kind":"repository"},"canonicalPath":"repos/workbench-fixture-entry","createdByWorkbench":true}]}`,
		"duplicate canonical path": `{"version":1,"resources":[{"identity":"phosphorco/one","github":"phosphorco/one","shape":{"kind":"repository"},"canonicalPath":"repos/one","createdByWorkbench":true},{"identity":"another/one","github":"another/one","shape":{"kind":"repository"},"canonicalPath":"repos/one","createdByWorkbench":true}]}`,
	}
	for name, encoded := range tests {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			if err := os.Mkdir(filepath.Join(root, ".workbench"), 0o700); err != nil {
				t.Fatal(err)
			}
			write(t, filepath.Join(root, ".workbench", "managed-checkouts.json"), encoded)
			if _, err := ReadManagedCheckouts(root); err == nil {
				t.Fatal("tampered Workbench-created provenance was accepted")
			}
		})
	}
}

func TestRunWithV020RoutesClosedShapesAndConvergesOrientation(t *testing.T) {
	root := t.TempDir()
	remotes := filepath.Join(root, "remotes")
	if err := os.MkdirAll(remotes, 0o755); err != nil {
		t.Fatal(err)
	}
	createRemote(t, root, remotes, "workbench-fixture-library", fmt.Sprintf("amends %q\n", localV020RepositoryURI), map[string]string{
		".gitignore":                      ".agents/skills/\n",
		"skills/library-edit/SKILL.md":    "---\nname: library-edit\ndescription: Library editing skill.\nmetadata:\n  domain: engineering\n---\n\nCompose [`$library-support`](../library-support/SKILL.md).\n",
		"skills/library-support/SKILL.md": "---\nname: library-support\ndescription: Library support skill.\nmetadata:\n  domain: general\n---\n",
	})
	createRemote(t, root, remotes, "workbench-fixture-entry", fmt.Sprintf("amends %q\n\nscope = \"@workbench-entry\"\nincludes { [\"phosphorco/workbench-fixture-library\"] { skills { editing { names = Set(\"library-edit\") }; workbench { names = Set(\"library-edit\") } } } }\n", localV020PackageScopeURI), map[string]string{
		".gitignore":                   ".agents/skills/\n",
		"skills/entry-export/SKILL.md": "---\nname: entry-export\ndescription: Entry export skill.\nmetadata:\n  domain: general\n---\n\nEntry-owned source skill.\n",
	})
	workbench := filepath.Join(root, "workbench")
	if err := os.MkdirAll(workbench, 0o755); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(workbench, "workbench-subject.pkl"), fmt.Sprintf("amends %q\n\nworkLine { branch = \"workbench/v020\"; baseBranch = \"main\" }\nentrypoints { \"https://github.com/phosphorco/workbench-fixture-entry\" }\n", localV020SubjectURI))
	write(t, filepath.Join(workbench, "AGENTS.pkl"), fmt.Sprintf("amends %q\n\nprose = \"Fixture orientation.\"\n", localV020AgentInstructionsURI))
	t.Setenv("GIT_ALLOW_PROTOCOL", "file")
	t.Setenv("GIT_CONFIG_COUNT", "1")
	t.Setenv("GIT_CONFIG_KEY_0", "url.file://"+filepath.ToSlash(remotes)+"/.insteadOf")
	t.Setenv("GIT_CONFIG_VALUE_0", "https://github.com/phosphorco/")
	pkl, err := exec.LookPath("pkl")
	if err != nil {
		t.Skip("pkl unavailable")
	}
	bun, err := exec.LookPath("bun")
	if err != nil {
		t.Skip("bun unavailable")
	}
	evaluator, err := evaluate.NewEvaluator(pkl)
	if err != nil {
		t.Fatal(err)
	}

	first, err := RunWith(context.Background(), workbench, NewToolchain(evaluator, bun))
	if err != nil {
		t.Fatal(err)
	}
	if first.ContractVersion != "0.2.0" {
		t.Fatalf("contract version = %q, want 0.2.0", first.ContractVersion)
	}
	if len(first.Resources) != 2 {
		t.Fatalf("resources = %#v", first.Resources)
	}
	identities := []string{first.Resources[0].Identity, first.Resources[1].Identity}
	slices.Sort(identities)
	wantIdentities := []string{"@workbench-entry", "phosphorco/workbench-fixture-library"}
	if !slices.Equal(identities, wantIdentities) {
		t.Fatalf("identities = %v, want %v", identities, wantIdentities)
	}
	for _, path := range []string{"pkg/@workbench-entry", "repos/workbench-fixture-library"} {
		if _, err := os.Stat(filepath.Join(workbench, filepath.FromSlash(path))); err != nil {
			t.Fatalf("canonical checkout %s: %v", path, err)
		}
	}
	entryRoot := filepath.Join(workbench, "pkg", "@workbench-entry")
	entrySourceBefore, err := os.ReadFile(filepath.Join(entryRoot, "skills", "entry-export", "SKILL.md"))
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{
		"pkg/@workbench-entry/.agents/skills/library-edit/SKILL.md",
		"pkg/@workbench-entry/.agents/skills/library-support/SKILL.md",
		".agents/skills/library-edit/SKILL.md",
		".agents/skills/library-support/SKILL.md",
	} {
		if _, err := os.Stat(filepath.Join(workbench, filepath.FromSlash(path))); err != nil {
			t.Fatalf("projected selected skill %s: %v", path, err)
		}
	}
	for _, path := range []string{"pkg/@workbench-entry", "repos/workbench-fixture-library"} {
		if status := git(t, filepath.Join(workbench, filepath.FromSlash(path)), "status", "--porcelain=v1", "--untracked-files=all"); status != "" {
			t.Fatalf("generated skill projection entered Git-owned status for %s: %q", path, status)
		}
	}
	agentsBefore, err := os.ReadFile(filepath.Join(workbench, "AGENTS.md"))
	if err != nil {
		t.Fatal(err)
	}
	for _, fact := range []string{"@workbench-entry", "phosphorco/workbench-fixture-entry", "repos/workbench-fixture-library", "workbench/v020", "pkg/@workbench-entry/skills", "repos/workbench-fixture-library/skills", ".agents/skills"} {
		if !strings.Contains(string(agentsBefore), fact) {
			t.Errorf("AGENTS.md lacks %q", fact)
		}
	}

	second, err := RunWith(context.Background(), workbench, NewToolchain(evaluator, bun))
	if err != nil {
		t.Fatal(err)
	}
	if len(second.ChangedPaths) != 0 {
		t.Fatalf("second setup changed %#v", second.ChangedPaths)
	}
	agentsAfter, err := os.ReadFile(filepath.Join(workbench, "AGENTS.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(agentsBefore, agentsAfter) {
		t.Fatal("AGENTS.md did not byte-converge")
	}
	entrySourceAfter, err := os.ReadFile(filepath.Join(entryRoot, "skills", "entry-export", "SKILL.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(entrySourceBefore, entrySourceAfter) {
		t.Fatal("Git-owned skill source changed while its repository received an editing projection")
	}
	if status := git(t, entryRoot, "status", "--porcelain=v1", "--untracked-files=all"); status != "" {
		t.Fatalf("converged projection entered source Git status: %q", status)
	}
}

func TestRunWithV020RefusesLateForeignSkillCollisionBeforeGeneratedMutation(t *testing.T) {
	root := t.TempDir()
	remotes := filepath.Join(root, "remotes")
	if err := os.MkdirAll(remotes, 0o755); err != nil {
		t.Fatal(err)
	}
	createRemote(t, root, remotes, "workbench-fixture-entry", fmt.Sprintf("amends %q\n\nscope = \"@workbench-entry\"\nincludes { [\"phosphorco/workbench-fixture-library\"] {} }\n", localV020PackageScopeURI), map[string]string{
		".gitignore":                   ".agents/skills/\n",
		"skills/entry-export/SKILL.md": "---\nname: entry-export\ndescription: Entry export skill.\nmetadata:\n  domain: engineering\n---\n\nEntry source.\n",
	})
	createRemote(t, root, remotes, "workbench-fixture-library", fmt.Sprintf("amends %q\n\nincludes { [\"phosphorco/workbench-fixture-entry\"] { skills { editing { names = Set(\"entry-export\") } } } }\n", localV020RepositoryURI), map[string]string{
		".gitignore": ".agents/skills/\n",
	})
	t.Setenv("GIT_ALLOW_PROTOCOL", "file")
	t.Setenv("GIT_CONFIG_COUNT", "1")
	t.Setenv("GIT_CONFIG_KEY_0", "url.file://"+filepath.ToSlash(remotes)+"/.insteadOf")
	t.Setenv("GIT_CONFIG_VALUE_0", "https://github.com/phosphorco/")
	workbench := filepath.Join(root, "workbench")
	if err := os.MkdirAll(workbench, 0o755); err != nil {
		t.Fatal(err)
	}
	branch := "workbench/skill-collision"
	write(t, filepath.Join(workbench, "workbench-subject.pkl"), fmt.Sprintf("amends %q\n\nworkLine { branch = %q; baseBranch = \"main\" }\nentrypoints { \"https://github.com/phosphorco/workbench-fixture-entry\" }\n", localV020SubjectURI, branch))
	for _, checkout := range []struct {
		github string
		path   string
	}{
		{"workbench-fixture-entry", "pkg/@workbench-entry"},
		{"workbench-fixture-library", "repos/workbench-fixture-library"},
	} {
		target := filepath.Join(workbench, filepath.FromSlash(checkout.path))
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			t.Fatal(err)
		}
		git(t, root, "clone", "https://github.com/phosphorco/"+checkout.github, target)
		git(t, target, "checkout", "-b", branch)
	}
	entry := filepath.Join(workbench, "pkg", "@workbench-entry")
	library := filepath.Join(workbench, "repos", "workbench-fixture-library")
	write(t, filepath.Join(library, ".agents", "skills", "entry-export", "SKILL.md"), "foreign bytes\n")
	if status := git(t, library, "status", "--porcelain=v1", "--untracked-files=all"); status != "" {
		t.Fatalf("collision fixture is not ignored: %q", status)
	}

	pkl, err := exec.LookPath("pkl")
	if err != nil {
		t.Skip("pkl unavailable")
	}
	bun, err := exec.LookPath("bun")
	if err != nil {
		t.Skip("bun unavailable")
	}
	evaluator, err := evaluate.NewEvaluator(pkl)
	if err != nil {
		t.Fatal(err)
	}
	beforeEntry := exactGitSnapshot(t, entry)
	beforeLibrary := exactGitSnapshot(t, library)
	beforeVisible := readVisibleTree(t, workbench)
	if _, err := RunWith(context.Background(), workbench, NewToolchain(evaluator, bun)); err == nil || !strings.Contains(err.Error(), "foreign projection") {
		t.Fatalf("late foreign projection error = %v", err)
	}
	afterEntry := exactGitSnapshot(t, entry)
	afterLibrary := exactGitSnapshot(t, library)
	afterVisible := readVisibleTree(t, workbench)
	if !reflect.DeepEqual(beforeEntry, afterEntry) || !reflect.DeepEqual(beforeLibrary, afterLibrary) || !reflect.DeepEqual(beforeVisible, afterVisible) {
		t.Fatalf("late foreign collision mutated setup state:\nentry before=%#v\nentry after=%#v\nlibrary before=%#v\nlibrary after=%#v", beforeEntry, afterEntry, beforeLibrary, afterLibrary)
	}
}

func TestRunRefusesLaterInvalidCatalogBeforeAnyCanonicalMutation(t *testing.T) {
	root := t.TempDir()
	remotes := filepath.Join(root, "remotes")
	if err := os.MkdirAll(remotes, 0o755); err != nil {
		t.Fatal(err)
	}
	createRemote(t, root, remotes, "entry", fmt.Sprintf("amends %q\n\nscope = \"@entry\"\nincludes { [\"phosphorco/library\"] {} }\n", localV020PackageScopeURI), map[string]string{
		"skills/entry-skill/SKILL.md": "---\nname: entry-skill\ndescription: Entry.\nmetadata:\n  domain: general\n---\n",
	})
	createRemote(t, root, remotes, "library", fmt.Sprintf("amends %q\n", localV020RepositoryURI), map[string]string{
		"skills/broken/SKILL.md": "---\nname: broken\ndescription: Broken.\nmetadata:\n  domain: engineering\n---\n\nRead [missing](references/missing.md).\n",
	})
	workbench := filepath.Join(root, "workbench")
	if err := os.MkdirAll(workbench, 0o755); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(workbench, "workbench-subject.pkl"), fmt.Sprintf("amends %q\n\nworkLine { branch = \"workbench/catalog-preflight\"; baseBranch = \"main\" }\nentrypoints { \"https://github.com/phosphorco/entry\" }\n", localV020SubjectURI))
	t.Setenv("GIT_ALLOW_PROTOCOL", "file")
	t.Setenv("GIT_CONFIG_COUNT", "1")
	t.Setenv("GIT_CONFIG_KEY_0", "url.file://"+filepath.ToSlash(remotes)+"/.insteadOf")
	t.Setenv("GIT_CONFIG_VALUE_0", "https://github.com/phosphorco/")
	pkl, err := exec.LookPath("pkl")
	if err != nil {
		t.Skip("pkl unavailable")
	}
	evaluator, err := evaluate.NewEvaluator(pkl)
	if err != nil {
		t.Fatal(err)
	}
	before := readVisibleTree(t, workbench)
	_, err = RunWith(context.Background(), workbench, NewToolchain(evaluator, "/must/not/run/bun"))
	if err == nil {
		t.Fatal("invalid later catalog was accepted")
	}
	for _, fact := range []string{"phosphorco/library:broken/SKILL.md", "missing link target references/missing.md"} {
		if !strings.Contains(err.Error(), fact) {
			t.Fatalf("catalog refusal %q omits %q", err, fact)
		}
	}
	if after := readVisibleTree(t, workbench); !reflect.DeepEqual(after, before) {
		t.Fatalf("invalid catalog mutated Workbench root: before=%#v after=%#v", before, after)
	}
	for _, target := range []string{"pkg/@entry", "repos/library", ".workbench"} {
		if _, statErr := os.Lstat(filepath.Join(workbench, filepath.FromSlash(target))); !errors.Is(statErr, os.ErrNotExist) {
			t.Fatalf("invalid catalog created target %q: %v", target, statErr)
		}
	}
}

func TestRunCarriesCatalogWarningsWithoutBlockingSetup(t *testing.T) {
	root := t.TempDir()
	remotes := filepath.Join(root, "remotes")
	if err := os.MkdirAll(remotes, 0o755); err != nil {
		t.Fatal(err)
	}
	createRemote(t, root, remotes, "entry", fmt.Sprintf("amends %q\n\nscope = \"@entry\"\nincludes { [\"phosphorco/library\"] { skills { workbench { names = Set(\"warning-skill\") } } } }\n", localV020PackageScopeURI), map[string]string{
		"skills/alpha-warning/SKILL.md": "---\nname: alpha-warning\ndescription: \"\"\nmetadata:\n  domain: general\n---\n",
	})
	createRemote(t, root, remotes, "library", fmt.Sprintf("amends %q\n", localV020RepositoryURI), map[string]string{
		"skills/warning-skill/SKILL.md": "---\nname: warning-skill\ndescription: \"\"\nmetadata:\n  domain: general\n---\n",
	})
	workbench := filepath.Join(root, "workbench")
	if err := os.MkdirAll(workbench, 0o755); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(workbench, "workbench-subject.pkl"), fmt.Sprintf("amends %q\n\nworkLine { branch = \"workbench/warnings\"; baseBranch = \"main\" }\nentrypoints { \"https://github.com/phosphorco/entry\" }\n", localV020SubjectURI))
	t.Setenv("GIT_ALLOW_PROTOCOL", "file")
	t.Setenv("GIT_CONFIG_COUNT", "1")
	t.Setenv("GIT_CONFIG_KEY_0", "url.file://"+filepath.ToSlash(remotes)+"/.insteadOf")
	t.Setenv("GIT_CONFIG_VALUE_0", "https://github.com/phosphorco/")
	pkl, err := exec.LookPath("pkl")
	if err != nil {
		t.Skip("pkl unavailable")
	}
	bun, err := exec.LookPath("bun")
	if err != nil {
		t.Skip("bun unavailable")
	}
	evaluator, err := evaluate.NewEvaluator(pkl)
	if err != nil {
		t.Fatal(err)
	}
	result, err := RunWith(context.Background(), workbench, NewToolchain(evaluator, bun))
	if err != nil {
		t.Fatal(err)
	}
	want := []skills.Diagnostic{
		{Source: "phosphorco/entry", Path: "alpha-warning/SKILL.md", Message: "frontmatter description is missing or empty"},
		{Source: "phosphorco/library", Path: "warning-skill/SKILL.md", Message: "frontmatter description is missing or empty"},
	}
	if !reflect.DeepEqual(result.SkillWarnings, want) {
		t.Fatalf("setup warnings = %#v, want %#v", result.SkillWarnings, want)
	}
}

func TestRunUsesDirtyExistingSubjectSkillSourceWithoutRemoteSubstitution(t *testing.T) {
	root := t.TempDir()
	remotes := filepath.Join(root, "remotes")
	if err := os.MkdirAll(remotes, 0o755); err != nil {
		t.Fatal(err)
	}
	createRemote(t, root, remotes, "entry", fmt.Sprintf("amends %q\n\nscope = \"@entry\"\nincludes { [\"phosphorco/library\"] { skills { workbench { names = Set(\"local-skill\") } } } }\n", localV020PackageScopeURI), nil)
	createRemote(t, root, remotes, "library", fmt.Sprintf("amends %q\n", localV020RepositoryURI), map[string]string{
		"skills/local-skill/SKILL.md": "---\nname: local-skill\ndescription: Local skill.\nmetadata:\n  domain: general\n---\n\nRemote bytes.\n",
	})
	workbench := filepath.Join(root, "workbench")
	if err := os.MkdirAll(workbench, 0o755); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(workbench, "workbench-subject.pkl"), fmt.Sprintf("amends %q\n\nworkLine { branch = \"workbench/local-skill\"; baseBranch = \"main\" }\nentrypoints { \"https://github.com/phosphorco/entry\" }\n", localV020SubjectURI))
	t.Setenv("GIT_ALLOW_PROTOCOL", "file")
	t.Setenv("GIT_CONFIG_COUNT", "1")
	t.Setenv("GIT_CONFIG_KEY_0", "url.file://"+filepath.ToSlash(remotes)+"/.insteadOf")
	t.Setenv("GIT_CONFIG_VALUE_0", "https://github.com/phosphorco/")
	pkl, err := exec.LookPath("pkl")
	if err != nil {
		t.Skip("pkl unavailable")
	}
	bun, err := exec.LookPath("bun")
	if err != nil {
		t.Skip("bun unavailable")
	}
	evaluator, err := evaluate.NewEvaluator(pkl)
	if err != nil {
		t.Fatal(err)
	}
	toolchain := NewToolchain(evaluator, bun)
	if _, err := RunWith(context.Background(), workbench, toolchain); err != nil {
		t.Fatal(err)
	}
	library := filepath.Join(workbench, "repos", "library")
	source := filepath.Join(library, "skills", "local-skill", "SKILL.md")
	committedBytes := "---\nname: local-skill\ndescription: Local skill.\nmetadata:\n  domain: general\n---\n\nLocal-only Subject commit.\n"
	write(t, source, committedBytes)
	git(t, library, "add", "skills/local-skill/SKILL.md")
	git(t, library, "-c", "user.email=setup@example.invalid", "-c", "user.name=Setup Test", "commit", "-m", "local Subject skill")
	localHead := git(t, library, "rev-parse", "HEAD")
	git(t, library, "checkout", "main")
	if err := os.Remove(filepath.Join(workbench, ".workbench", "managed-checkouts.json")); err != nil {
		t.Fatal(err)
	}
	if _, err := RunWith(context.Background(), workbench, toolchain); err != nil {
		t.Fatal(err)
	}
	if branch := git(t, library, "branch", "--show-current"); branch != "workbench/local-skill" {
		t.Fatalf("selected branch = %q", branch)
	}
	if head := git(t, library, "rev-parse", "HEAD"); head != localHead {
		t.Fatalf("selected local Subject HEAD = %q, want %q", head, localHead)
	}
	if projected := string(mustRead(t, filepath.Join(workbench, ".agents", "skills", "local-skill", "SKILL.md"))); projected != committedBytes {
		t.Fatalf("projected local-only commit = %q", projected)
	}

	localBytes := "---\nname: local-skill\ndescription: Local skill.\nmetadata:\n  domain: general\n---\n\nDirty local Subject bytes.\n"
	write(t, source, localBytes)
	statusBefore := git(t, library, "status", "--porcelain=v1", "--untracked-files=all")
	headBefore := git(t, library, "rev-parse", "HEAD")
	if _, err := RunWith(context.Background(), workbench, toolchain); err != nil {
		t.Fatal(err)
	}
	projected := mustRead(t, filepath.Join(workbench, ".agents", "skills", "local-skill", "SKILL.md"))
	if string(projected) != localBytes {
		t.Fatalf("projected bytes = %q, want dirty local Subject bytes", projected)
	}
	if statusAfter := git(t, library, "status", "--porcelain=v1", "--untracked-files=all"); statusAfter != statusBefore {
		t.Fatalf("dirty source status changed: before %q after %q", statusBefore, statusAfter)
	}
	if headAfter := git(t, library, "rev-parse", "HEAD"); headAfter != headBefore {
		t.Fatalf("dirty source HEAD changed: before %q after %q", headBefore, headAfter)
	}
}

func TestProjectSkillsV020ReconcilesOwnedClosureWithoutConsumingSourceOrContext(t *testing.T) {
	root := t.TempDir()
	entry := filepath.Join(root, "pkg", "@workbench-entry")
	library := filepath.Join(root, "repos", "workbench-fixture-library")
	for _, repository := range []struct {
		path   string
		github string
	}{{entry, "phosphorco/workbench-fixture-entry"}, {library, "phosphorco/workbench-fixture-library"}} {
		if err := os.MkdirAll(repository.path, 0o755); err != nil {
			t.Fatal(err)
		}
		git(t, repository.path, "init", "-b", "main")
		git(t, repository.path, "config", "user.email", "setup@example.invalid")
		git(t, repository.path, "config", "user.name", "Setup Test")
		git(t, repository.path, "remote", "add", "origin", "https://github.com/"+repository.github)
		write(t, filepath.Join(repository.path, ".gitignore"), ".agents/skills/\n")
	}
	entrySource := "---\nname: entry-export\ndescription: Entry export skill.\nmetadata:\n  domain: general\n---\n\nEntry-owned source skill.\n"
	write(t, filepath.Join(entry, "skills", "entry-export", "SKILL.md"), entrySource)
	write(t, filepath.Join(library, "skills", "library-edit", "SKILL.md"), "---\nname: library-edit\ndescription: Library editing skill.\nmetadata:\n  domain: engineering\n---\n\nCompose [`$library-support`](../library-support/SKILL.md).\n")
	write(t, filepath.Join(library, "skills", "library-support", "SKILL.md"), "---\nname: library-support\ndescription: Library support skill.\nmetadata:\n  domain: general\n---\n")
	for _, repository := range []string{entry, library} {
		git(t, repository, "add", ".")
		git(t, repository, "commit", "-m", "author source skills")
	}
	contextSibling := filepath.Join(entry, ".agents", "skills", "context-owned", "SKILL.md")
	write(t, contextSibling, "context-owned sibling\n")
	retiredContextSibling := filepath.Join(library, ".agents", "skills", "library-context", "SKILL.md")
	write(t, retiredContextSibling, "library context-owned sibling\n")
	selection := contract.SkillSelection{Names: []string{"library-edit"}}
	entryExportSelection := contract.SkillSelection{Names: []string{"entry-export"}}
	resources := []Resource{
		{
			Identity: "@workbench-entry", CanonicalPath: "pkg/@workbench-entry",
			Includes: []contract.SkillPolicy{{Editing: &selection, Workbench: &selection}},
		},
		{Identity: "phosphorco/workbench-fixture-library", CanonicalPath: "repos/workbench-fixture-library", Includes: []contract.SkillPolicy{{Editing: &entryExportSelection}}},
	}

	plan, err := planSkills(root, resources, managedCheckoutReceipt{Version: 1})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := plan.Apply(); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{
		"pkg/@workbench-entry/.agents/skills/library-edit/SKILL.md",
		"pkg/@workbench-entry/.agents/skills/library-support/SKILL.md",
		"repos/workbench-fixture-library/.agents/skills/entry-export/SKILL.md",
		".agents/skills/library-edit/SKILL.md",
		".agents/skills/library-support/SKILL.md",
	} {
		if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(path))); err != nil {
			t.Fatalf("selected composition closure %s: %v", path, err)
		}
	}
	if got := string(mustRead(t, filepath.Join(entry, "skills", "entry-export", "SKILL.md"))); got != entrySource {
		t.Fatalf("Git-owned source changed: %q", got)
	}

	remaining := []Resource{{Identity: "@workbench-entry", CanonicalPath: "pkg/@workbench-entry"}}
	previous := managedCheckoutReceipt{Version: 1, Resources: []receiptResource{
		{Identity: "@workbench-entry", GitHub: "phosphorco/workbench-fixture-entry", Shape: contract.ResourceShape{Kind: contract.PackageScopeShape, Scope: "@workbench-entry"}, CanonicalPath: "pkg/@workbench-entry"},
		{Identity: "phosphorco/workbench-fixture-library", GitHub: "phosphorco/workbench-fixture-library", Shape: contract.ResourceShape{Kind: contract.RepositoryShape}, CanonicalPath: "repos/workbench-fixture-library"},
	}}
	plan, err = planSkills(root, remaining, previous)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := plan.Apply(); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{
		"pkg/@workbench-entry/.agents/skills/library-edit",
		"pkg/@workbench-entry/.agents/skills/library-support",
		"repos/workbench-fixture-library/.agents/skills/entry-export",
		".agents/skills/library-edit",
		".agents/skills/library-support",
	} {
		if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(path))); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("stale Workbench-owned projection %s remains: %v", path, err)
		}
	}
	if got := string(mustRead(t, contextSibling)); got != "context-owned sibling\n" {
		t.Fatalf("context-owned sibling changed: %q", got)
	}
	if got := string(mustRead(t, retiredContextSibling)); got != "library context-owned sibling\n" {
		t.Fatalf("retired member context-owned sibling changed: %q", got)
	}
	if got := string(mustRead(t, filepath.Join(library, "skills", "library-edit", "SKILL.md"))); !strings.Contains(got, "library-support") {
		t.Fatalf("retired member Git-owned source changed: %q", got)
	}
	if info, err := os.Stat(library); err != nil || !info.IsDir() {
		t.Fatalf("ordinary member removal deleted retired checkout: %v", err)
	}
	if got := string(mustRead(t, filepath.Join(entry, "skills", "entry-export", "SKILL.md"))); got != entrySource {
		t.Fatalf("deselection consumed Git-owned source: %q", got)
	}
}

func TestProjectSkillsV020RefusesSymlinkedRetiredCheckoutWithoutMutation(t *testing.T) {
	root := t.TempDir()
	entry := filepath.Join(root, "pkg", "@workbench-entry")
	library := filepath.Join(root, "repos", "workbench-fixture-library")
	write(t, filepath.Join(entry, "skills", "entry-export", "SKILL.md"), "---\nname: entry-export\ndescription: Entry export skill.\nmetadata:\n  domain: engineering\n---\n\nEntry source.\n")
	selection := contract.SkillSelection{Names: []string{"entry-export"}}
	resources := []Resource{
		{Identity: "@workbench-entry", CanonicalPath: "pkg/@workbench-entry"},
		{Identity: "phosphorco/workbench-fixture-library", CanonicalPath: "repos/workbench-fixture-library", Includes: []contract.SkillPolicy{{Editing: &selection}}},
	}
	plan, err := planSkills(root, resources, managedCheckoutReceipt{Version: 1})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := plan.Apply(); err != nil {
		t.Fatal(err)
	}
	external := filepath.Join(t.TempDir(), "external-library")
	if err := os.Rename(library, external); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(external, library); err != nil {
		t.Fatal(err)
	}
	previous := managedCheckoutReceipt{Version: 1, Resources: []receiptResource{{
		Identity: "phosphorco/workbench-fixture-library", GitHub: "phosphorco/workbench-fixture-library",
		Shape: contract.ResourceShape{Kind: contract.RepositoryShape}, CanonicalPath: "repos/workbench-fixture-library",
	}}}
	beforeExternal := readTree(t, external)
	beforeLink, err := os.Readlink(library)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := planSkills(root, resources[:1], previous); err == nil || !strings.Contains(err.Error(), "not a real directory") {
		t.Fatalf("symlinked retired checkout error = %v", err)
	}
	afterExternal := readTree(t, external)
	afterLink, err := os.Readlink(library)
	if err != nil {
		t.Fatal(err)
	}
	if beforeLink != afterLink || !reflect.DeepEqual(beforeExternal, afterExternal) {
		t.Fatal("symlinked retired checkout refusal mutated the link target")
	}
}

func TestProjectSkillsV020RefusesRetiredCheckoutWithWrongOriginWithoutMutation(t *testing.T) {
	root := t.TempDir()
	entry := filepath.Join(root, "pkg", "@workbench-entry")
	library := filepath.Join(root, "repos", "workbench-fixture-library")
	write(t, filepath.Join(entry, "skills", "entry-export", "SKILL.md"), "---\nname: entry-export\ndescription: Entry export skill.\nmetadata:\n  domain: engineering\n---\n\nEntry source.\n")
	if err := os.MkdirAll(library, 0o755); err != nil {
		t.Fatal(err)
	}
	git(t, library, "init", "-b", "main")
	git(t, library, "config", "user.email", "setup@example.invalid")
	git(t, library, "config", "user.name", "Setup Test")
	git(t, library, "remote", "add", "origin", "https://github.com/someone-else/workbench-fixture-library")
	write(t, filepath.Join(library, ".gitignore"), ".agents/skills/\n")
	git(t, library, "add", ".gitignore")
	git(t, library, "commit", "-m", "initialize wrong checkout")
	selection := contract.SkillSelection{Names: []string{"entry-export"}}
	resources := []Resource{
		{Identity: "@workbench-entry", CanonicalPath: "pkg/@workbench-entry"},
		{Identity: "phosphorco/workbench-fixture-library", CanonicalPath: "repos/workbench-fixture-library", Includes: []contract.SkillPolicy{{Editing: &selection}}},
	}
	plan, err := planSkills(root, resources, managedCheckoutReceipt{Version: 1})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := plan.Apply(); err != nil {
		t.Fatal(err)
	}
	previous := managedCheckoutReceipt{Version: 1, Resources: []receiptResource{{
		Identity: "phosphorco/workbench-fixture-library", GitHub: "phosphorco/workbench-fixture-library",
		Shape: contract.ResourceShape{Kind: contract.RepositoryShape}, CanonicalPath: "repos/workbench-fixture-library",
	}}}
	beforeTree := readTree(t, root)
	beforeGit := exactGitSnapshot(t, library)
	if _, err := planSkills(root, resources[:1], previous); err == nil || !strings.Contains(err.Error(), "has origin") {
		t.Fatalf("wrong-origin retired checkout error = %v", err)
	}
	afterGit := exactGitSnapshot(t, library)
	afterTree := readTree(t, root)
	if !reflect.DeepEqual(beforeGit, afterGit) || !reflect.DeepEqual(beforeTree, afterTree) {
		t.Fatal("wrong-origin retired checkout refusal mutated state")
	}
}

func TestProjectSkillsV020RefusesForeignSelectedNameWithoutMutation(t *testing.T) {
	root := t.TempDir()
	entry := filepath.Join(root, "pkg", "@workbench-entry")
	library := filepath.Join(root, "repos", "workbench-fixture-library")
	write(t, filepath.Join(entry, "skills", "entry-export", "SKILL.md"), "---\nname: entry-export\ndescription: Entry export skill.\nmetadata:\n  domain: engineering\n---\n\nEntry source.\n")
	write(t, filepath.Join(library, ".agents", "skills", "entry-export", "SKILL.md"), "foreign bytes\n")
	selection := contract.SkillSelection{Names: []string{"entry-export"}}
	resources := []Resource{
		{Identity: "@workbench-entry", CanonicalPath: "pkg/@workbench-entry"},
		{Identity: "phosphorco/workbench-fixture-library", CanonicalPath: "repos/workbench-fixture-library", Includes: []contract.SkillPolicy{{Editing: &selection}}},
	}
	before := readTree(t, root)
	if _, err := planSkills(root, resources, managedCheckoutReceipt{Version: 1}); err == nil || !strings.Contains(err.Error(), "foreign projection") {
		t.Fatalf("foreign selected-name error = %v", err)
	}
	after := readTree(t, root)
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("foreign collision mutated the Workbench:\nbefore=%#v\nafter=%#v", before, after)
	}
}

func TestProjectSkillsV020RefusesTrackedForgedOwnershipWithoutMutation(t *testing.T) {
	root := t.TempDir()
	entry := filepath.Join(root, "pkg", "@workbench-entry")
	library := filepath.Join(root, "repos", "workbench-fixture-library")
	for _, repository := range []string{entry, library} {
		if err := os.MkdirAll(repository, 0o755); err != nil {
			t.Fatal(err)
		}
		git(t, repository, "init", "-b", "main")
		git(t, repository, "config", "user.email", "setup@example.invalid")
		git(t, repository, "config", "user.name", "Setup Test")
		write(t, filepath.Join(repository, ".gitignore"), ".agents/skills/\n")
	}
	write(t, filepath.Join(library, "skills", "library-edit", "SKILL.md"), "---\nname: library-edit\ndescription: Library editing skill.\nmetadata:\n  domain: engineering\n---\n\nLibrary source.\n")
	for _, repository := range []string{entry, library} {
		git(t, repository, "add", ".")
		git(t, repository, "commit", "-m", "declare resource skill source")
	}
	selection := contract.SkillSelection{Names: []string{"library-edit"}}
	selectedResources := []Resource{
		{Identity: "@workbench-entry", CanonicalPath: "pkg/@workbench-entry", Includes: []contract.SkillPolicy{{Editing: &selection}}},
		{Identity: "phosphorco/workbench-fixture-library", CanonicalPath: "repos/workbench-fixture-library"},
	}
	plan, err := planSkills(root, selectedResources, managedCheckoutReceipt{Version: 1})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := plan.Apply(); err != nil {
		t.Fatal(err)
	}
	git(t, entry, "add", "-f", ".agents/skills/.workbench-owned.json", ".agents/skills/library-edit")
	git(t, entry, "commit", "-m", "forge tracked Workbench ownership")

	beforeTree := readTree(t, root)
	beforeGit := exactGitSnapshot(t, entry)
	deselectedResources := []Resource{
		{Identity: "@workbench-entry", CanonicalPath: "pkg/@workbench-entry"},
		{Identity: "phosphorco/workbench-fixture-library", CanonicalPath: "repos/workbench-fixture-library"},
	}
	if _, err := planSkills(root, deselectedResources, managedCheckoutReceipt{Version: 1}); err == nil || !strings.Contains(err.Error(), "tracked") {
		t.Fatalf("tracked forged ownership error = %v", err)
	}
	afterGit := exactGitSnapshot(t, entry)
	afterTree := readTree(t, root)
	if !reflect.DeepEqual(beforeGit, afterGit) || !reflect.DeepEqual(beforeTree, afterTree) {
		t.Fatalf("tracked forged ownership refusal mutated state:\nGit before=%#v\nGit after=%#v", beforeGit, afterGit)
	}
}

func TestRetiredV010SkillSourceRefusesWithRecreationGuidance(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, ".agents", "skills", "retired", "SKILL.md"), "legacy source\n")
	resources := []Resource{{Identity: "@entry", GitHub: "phosphorco/entry", CanonicalPath: "pkg/@entry"}}
	err := rejectRetiredV010SkillSources(resources, map[string]string{"@entry": root})
	if err == nil {
		t.Fatal("retired 0.1 source was consumed")
	}
	for _, fact := range []string{"phosphorco/entry:.agents/skills/retired/SKILL.md", "recreate", "skills/retired/SKILL.md"} {
		if !strings.Contains(err.Error(), fact) {
			t.Fatalf("legacy refusal %q omits %q", err, fact)
		}
	}
}

func TestCatalogReobservationRefusesStaleBytes(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "skills", "stable", "SKILL.md"), "before\n")
	digest, _, err := observeCatalogTree(filepath.Join(root, "skills"))
	if err != nil {
		t.Fatal(err)
	}
	observation := catalogObservation{identity: "phosphorco/resource", root: filepath.Join(root, "skills"), digest: digest}
	write(t, filepath.Join(root, "skills", "stable", "SKILL.md"), "after\n")
	if err := reobserveCatalogs([]catalogObservation{observation}); err == nil || !strings.Contains(err.Error(), "changed after preflight") {
		t.Fatalf("stale catalog refusal = %v", err)
	}
}

func TestRunWithRefusesAmbientBunAuthority(t *testing.T) {
	evaluator, err := evaluate.NewEvaluator("/does/not/need/to/run/pkl")
	if err != nil {
		t.Fatal(err)
	}
	_, err = RunWith(context.Background(), t.TempDir(), NewToolchain(evaluator, "bun"))
	if err == nil || !strings.Contains(err.Error(), "exact absolute path") {
		t.Fatalf("RunWith relative Bun error = %v", err)
	}
}

func TestManagedCheckoutReceiptKeepsOrphanProvenanceWithoutDeleting(t *testing.T) {
	root := t.TempDir()
	orphanPath := filepath.Join(root, "repos", "retired")
	if err := os.MkdirAll(orphanPath, 0o755); err != nil {
		t.Fatal(err)
	}
	previous := managedCheckoutReceipt{Version: 1, Resources: []receiptResource{{
		Identity: "phosphorco/retired", GitHub: "phosphorco/retired",
		Shape:         contract.ResourceShape{Kind: contract.RepositoryShape},
		CanonicalPath: "repos/retired", CreatedByWorkbench: true,
	}}}
	if _, err := writeManagedCheckoutReceipt(root, nil, previous, nil); err != nil {
		t.Fatal(err)
	}
	loadedPlan, err := preflightManagedCheckoutMigration(root)
	if err != nil {
		t.Fatal(err)
	}
	loaded := loadedPlan.receipt
	orphans, err := reportOrphans(root, nil, loaded)
	if err != nil {
		t.Fatal(err)
	}
	if len(orphans) != 1 || orphans[0].Path != orphanPath {
		t.Fatalf("orphans = %#v", orphans)
	}
	if _, err := os.Stat(orphanPath); err != nil {
		t.Fatalf("ordinary receipt/report removed checkout: %v", err)
	}
	managed, err := ReadManagedCheckouts(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(managed) != 1 || !managed[0].CreatedByWorkbench {
		t.Fatalf("managed provenance = %#v", managed)
	}
}

type setupFixture struct {
	workbench string
}

func newSetupFixture(t *testing.T) setupFixture {
	t.Helper()
	root := t.TempDir()
	remotes := filepath.Join(root, "remotes")
	if err := os.MkdirAll(remotes, 0o755); err != nil {
		t.Fatal(err)
	}
	createRemote(t, root, remotes, "community-packages", fmt.Sprintf("amends %q\n\nscope = \"@phosphorco\"\n", localRepositoryURI))
	createRemote(t, root, remotes, "basindb", fmt.Sprintf("amends %q\n\nscope = \"@basindb\"\nincludes { [\"@phosphorco\"] { github = \"phosphorco/community-packages\" } }\n", localRepositoryURI))
	workbench := filepath.Join(root, "workbench")
	if err := os.MkdirAll(workbench, 0o755); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(workbench, "workbench-subject.pkl"), fmt.Sprintf("amends %q\n\nworkLine { branch = \"local/meaningful-slice\"; baseBranch = \"main\" }\nentrypoints { \"https://github.com/phosphorco/basindb\" }\n", localSubjectURI))
	t.Setenv("GIT_ALLOW_PROTOCOL", "file")
	t.Setenv("GIT_CONFIG_COUNT", "1")
	t.Setenv("GIT_CONFIG_KEY_0", "url.file://"+filepath.ToSlash(remotes)+"/.insteadOf")
	t.Setenv("GIT_CONFIG_VALUE_0", "https://github.com/phosphorco/")
	return setupFixture{workbench: workbench}
}

func createRemote(t *testing.T, root string, remotes string, name string, declaration string, fileSets ...map[string]string) {
	t.Helper()
	seed := filepath.Join(root, "seeds", name)
	if err := os.MkdirAll(seed, 0o755); err != nil {
		t.Fatal(err)
	}
	git(t, seed, "init", "-b", "main")
	git(t, seed, "config", "user.email", "setup@example.invalid")
	git(t, seed, "config", "user.name", "Setup Test")
	write(t, filepath.Join(seed, "workbench.pkl"), declaration)
	for _, files := range fileSets {
		for path, contents := range files {
			write(t, filepath.Join(seed, filepath.FromSlash(path)), contents)
		}
	}
	git(t, seed, "add", ".")
	git(t, seed, "commit", "-m", "declare resource")
	git(t, root, "clone", "--bare", seed, filepath.Join(remotes, name))
}

type snapshot struct {
	head   string
	branch string
	refs   string
	status string
}

type exactSnapshot struct {
	Git   snapshot
	Index string
}

func exactGitSnapshot(t *testing.T, root string) exactSnapshot {
	t.Helper()
	return exactSnapshot{
		Git:   gitSnapshot(t, root),
		Index: git(t, root, "ls-files", "--stage"),
	}
}

func gitSnapshot(t *testing.T, root string) snapshot {
	t.Helper()
	return snapshot{
		head:   git(t, root, "rev-parse", "HEAD"),
		branch: git(t, root, "branch", "--show-current"),
		refs:   git(t, root, "show-ref"),
		status: git(t, root, "status", "--porcelain=v1", "--untracked-files=all"),
	}
}

func git(t *testing.T, root string, arguments ...string) string {
	t.Helper()
	command := exec.Command("git", arguments...)
	command.Dir = root
	command.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_TERMINAL_PROMPT=0")
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(arguments, " "), err, output)
	}
	return strings.TrimSpace(string(output))
}

func write(t *testing.T, path string, contents string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
}

func mustRead(t *testing.T, path string) []byte {
	t.Helper()
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return contents
}

func readTree(t *testing.T, root string) map[string]string {
	t.Helper()
	result := make(map[string]string)
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		contents, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		result[filepath.ToSlash(relative)] = string(contents)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func readVisibleTree(t *testing.T, root string) map[string]string {
	t.Helper()
	result := make(map[string]string)
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() && entry.Name() == ".git" {
			return filepath.SkipDir
		}
		if entry.IsDir() {
			return nil
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		contents, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		result[filepath.ToSlash(relative)] = string(contents)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return result
}
