package main

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"os/exec"
	pathpkg "path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/phosphorco/workbench-go/internal/change"
	"github.com/phosphorco/workbench-go/internal/contract"
	"github.com/phosphorco/workbench-go/internal/evaluate"
	"github.com/phosphorco/workbench-go/internal/legacy/v020v030snapshot"
	"github.com/phosphorco/workbench-go/internal/orphan"
	workbenchruntime "github.com/phosphorco/workbench-go/internal/runtime"
	"github.com/phosphorco/workbench-go/internal/setup"
	"github.com/phosphorco/workbench-go/internal/snapshot"
	"github.com/phosphorco/workbench-go/internal/version"
)

const generatedPolicyID = "workbench-0.2-generated-projections-v2"

const currentContractVersion = "0.4.0"

var releasedAmendsPattern = regexp.MustCompile(`^\s*amends\s+"([^"\r\n]+)"`)

type commandEnvironment struct {
	evaluator evaluate.Evaluator
	setup     setupApplication
}

type environmentProvider func() (commandEnvironment, error)

func releasedEnvironment() environmentProvider {
	var once sync.Once
	var environment commandEnvironment
	var environmentErr error
	return func() (commandEnvironment, error) {
		once.Do(func() {
			toolchain, err := workbenchruntime.Installed()
			if err != nil {
				environmentErr = err
				return
			}
			evaluator, err := evaluate.NewEvaluator(toolchain.PklPath())
			if err != nil {
				environmentErr = err
				return
			}
			environment = commandEnvironment{
				evaluator: evaluator,
				setup: func(ctx context.Context, root string) (setup.Result, error) {
					return setup.RunWith(ctx, root, setup.NewToolchain(evaluator, toolchain.BunPath()))
				},
			}
		})
		return environment, environmentErr
	}
}

func developmentEnvironment() environmentProvider {
	var once sync.Once
	var environment commandEnvironment
	var environmentErr error
	return func() (commandEnvironment, error) {
		once.Do(func() {
			pklPath, err := exec.LookPath("pkl")
			if err != nil {
				environmentErr = fmt.Errorf("resolve development Pkl: %w", err)
				return
			}
			bunPath, err := exec.LookPath("bun")
			if err != nil {
				environmentErr = fmt.Errorf("resolve development Bun: %w", err)
				return
			}
			pklPath, err = filepath.Abs(pklPath)
			if err != nil {
				environmentErr = fmt.Errorf("resolve development Pkl path: %w", err)
				return
			}
			bunPath, err = filepath.Abs(bunPath)
			if err != nil {
				environmentErr = fmt.Errorf("resolve development Bun path: %w", err)
				return
			}
			evaluator, err := evaluate.NewEvaluator(pklPath)
			if err != nil {
				environmentErr = err
				return
			}
			environment = commandEnvironment{
				evaluator: evaluator,
				setup: func(ctx context.Context, root string) (setup.Result, error) {
					return setup.RunWith(ctx, root, setup.NewToolchain(evaluator, bunPath))
				},
			}
		})
		return environment, environmentErr
	}
}

func applicationsForEnvironment(provider environmentProvider) applications {
	return applications{
		setup: func(ctx context.Context, root string) (setup.Result, error) {
			environment, err := provider()
			if err != nil {
				return setup.Result{}, err
			}
			return environment.setup(ctx, root)
		},
		commit: func(ctx context.Context, root, plan string) (string, error) {
			environment, err := provider()
			if err != nil {
				return "", err
			}
			return runCommit(ctx, root, plan, environment)
		},
		snapshotRecord: func(ctx context.Context, root, output string) (string, error) {
			environment, err := provider()
			if err != nil {
				return "", err
			}
			return recordSnapshot(ctx, root, output, environment)
		},
		snapshotReproduce: func(ctx context.Context, root, input string) (string, error) {
			environment, err := provider()
			if err != nil {
				return "", err
			}
			return reproduceSnapshot(ctx, root, input, environment.evaluator)
		},
		prune: func(ctx context.Context, root string, identities []string) (string, error) {
			environment, err := provider()
			if err != nil {
				return "", err
			}
			return pruneCheckouts(ctx, root, identities, environment)
		},
		version: version.Current,
	}
}

func runCommit(ctx context.Context, root, planName string, environment commandEnvironment) (string, error) {
	result, err := environment.setup(ctx, root)
	if err != nil {
		return "", err
	}
	planPath, err := workbenchPath(root, planName)
	if err != nil {
		return "", err
	}
	source, err := os.ReadFile(planPath)
	if err != nil {
		return "", fmt.Errorf("read commit plan: %w", err)
	}
	schema, err := releasedCommitPlanContract(source, result.ContractVersion)
	if err != nil {
		return "", err
	}
	plan, err := environment.evaluator.EvaluateWorkbenchCommitPlan(ctx, source, schema)
	if err != nil {
		return "", err
	}
	subject, err := evaluateSubject(ctx, root, environment.evaluator, result.ContractVersion)
	if err != nil {
		return "", err
	}
	resources := make(map[string]setup.Resource, len(result.Resources))
	for _, resource := range result.Resources {
		resources[resource.Identity] = resource
	}
	identities := make([]string, 0, len(plan.Commits))
	for identity := range plan.Commits {
		identities = append(identities, identity)
	}
	sort.Strings(identities)
	requests := make([]change.Request, 0, len(identities))
	for _, identity := range identities {
		resource, exists := resources[identity]
		if !exists {
			return "", fmt.Errorf("commit plan resource %q is not a participating repository", identity)
		}
		selection := plan.Commits[identity]
		requests = append(requests, change.Request{
			ResourceID: identity, Repository: filepath.Join(root, filepath.FromSlash(resource.CanonicalPath)),
			Branch: subject.WorkLine.Branch, Remote: "origin", ChangeID: plan.ChangeID,
			Title: selection.Title, Description: selection.Description,
			Paths: selection.FilePaths, HunkIDs: selection.HunkIDs, UnrelatedDeletedPaths: selection.UnrelatedDeletedPaths,
			GeneratedPathPolicyID: generatedPolicyID, RejectPath: generatedProjectionPath,
		})
	}
	journal := filepath.Join(root, ".workbench", "changes", plan.ChangeID+".jsonl")
	var saga *change.Saga
	if _, err := os.Stat(journal); err == nil {
		saga, err = change.RecoverExact(journal, requests)
	} else if errors.Is(err, os.ErrNotExist) {
		var candidates []change.Candidate
		candidates, err = change.PrepareAll(ctx, requests)
		if err == nil {
			saga, err = change.Begin(journal, candidates)
		}
	} else {
		return "", fmt.Errorf("observe change journal: %w", err)
	}
	if err != nil {
		return "", err
	}
	if err := saga.AdvanceLocal(ctx); err != nil {
		return "", err
	}
	pushErr := saga.Push(ctx)
	progress, progressErr := saga.Progress(ctx)
	if pushErr != nil {
		if progressErr != nil {
			return "", errors.Join(pushErr, fmt.Errorf("observe durable change progress: %w", progressErr))
		}
		return "", fmt.Errorf("%w; progress: %s", pushErr, renderChangeProgress(progress))
	}
	if progressErr != nil {
		return "", fmt.Errorf("observe durable change progress: %w", progressErr)
	}
	return fmt.Sprintf("Workbench change %s completed: %s", plan.ChangeID, renderChangeProgress(progress)), nil
}

func generatedProjectionPath(path string) bool {
	normalized := pathpkg.Clean(filepath.ToSlash(path))
	base := pathpkg.Base(normalized)
	switch base {
	case "package.json", "tsconfig.json", "AGENTS.md", "bun.lock":
		return true
	}
	return normalized == ".agents/skills" || strings.HasPrefix(normalized, ".agents/skills/") ||
		normalized == "node_modules" || strings.HasPrefix(normalized, "node_modules/")
}

func renderChangeProgress(progress change.Progress) string {
	parts := make([]string, 0, len(progress.Resources))
	for _, resource := range progress.Resources {
		parts = append(parts, fmt.Sprintf("%s(local=%t,pushed=%t)", resource.ResourceID, resource.Local, resource.Pushed))
	}
	return strings.Join(parts, ", ")
}

func releasedContractURI(contractVersion, filename string) string {
	return fmt.Sprintf(
		"package://github.com/phosphorco/workbench-go/releases/download/%s/workbench@%s#/%s",
		contractVersion,
		contractVersion,
		filename,
	)
}

func lifecycleContractURI(contractVersion, filename string) (string, error) {
	switch contractVersion {
	case "0.2.0", "0.3.0", currentContractVersion:
		return releasedContractURI(contractVersion, filename), nil
	case "0.1.0":
		return "", fmt.Errorf("Workbench 0.1.0 has no released %s contract", filename)
	default:
		return "", fmt.Errorf("Workbench contract release %q is unsupported for %s", contractVersion, filename)
	}
}

func amendedContractURI(source []byte) (string, error) {
	match := releasedAmendsPattern.FindSubmatch(source)
	if match == nil {
		return "", fmt.Errorf("Pkl source must begin with amends")
	}
	return string(match[1]), nil
}

func releasedContractForSubjectLine(source []byte, filename, contractVersion string, supportedVersions ...string) (evaluate.Contract, error) {
	supported := false
	for _, candidate := range supportedVersions {
		if contractVersion == candidate {
			supported = true
			break
		}
	}
	if !supported {
		return evaluate.Contract{}, fmt.Errorf("Workbench contract release %q is unsupported for %s", contractVersion, filename)
	}
	want := releasedContractURI(contractVersion, filename)
	got, err := amendedContractURI(source)
	if err != nil {
		return evaluate.Contract{}, err
	}
	if got != want {
		return evaluate.Contract{}, fmt.Errorf("Pkl source amends %q, want exact Workbench %s contract %q", got, contractVersion, want)
	}
	return evaluate.ReleasedContract(want)
}

func releasedCommitPlanContract(source []byte, contractVersion string) (evaluate.Contract, error) {
	const filename = "WorkbenchCommitPlan.pkl"
	if _, err := lifecycleContractURI(contractVersion, filename); err != nil {
		return evaluate.Contract{}, err
	}
	return releasedContractForSubjectLine(source, filename, contractVersion, "0.2.0", "0.3.0", currentContractVersion)
}

type snapshotContractKind uint8

const (
	currentSnapshotContract snapshotContractKind = iota + 1
	legacyV020V030SnapshotContract
)

func releasedSnapshotContractFromSource(source []byte) (evaluate.Contract, snapshotContractKind, error) {
	got, err := amendedContractURI(source)
	if err != nil {
		return evaluate.Contract{}, 0, err
	}
	current := releasedContractURI(currentContractVersion, "WorkbenchSnapshot.pkl")
	if got == current {
		schema, err := evaluate.ReleasedContract(current)
		return schema, currentSnapshotContract, err
	}
	for _, contractVersion := range []string{"0.2.0", "0.3.0"} {
		want, err := v020v030snapshot.ContractURI(contractVersion)
		if err != nil {
			return evaluate.Contract{}, 0, err
		}
		if got == want {
			schema, err := evaluate.ReleasedContract(want)
			return schema, legacyV020V030SnapshotContract, err
		}
	}
	if got == releasedContractURI("0.1.0", v020v030snapshot.Filename) {
		return evaluate.Contract{}, 0, errors.New("Workbench 0.1.0 has no released snapshot contract")
	}
	return evaluate.Contract{}, 0, fmt.Errorf("Pkl source amends unsupported Workbench Snapshot contract %q", got)
}

func evaluateSubject(ctx context.Context, root string, evaluator evaluate.Evaluator, contractVersion string) (contract.Subject, error) {
	source, err := os.ReadFile(filepath.Join(root, "workbench-subject.pkl"))
	if err != nil {
		return contract.Subject{}, fmt.Errorf("read workbench-subject.pkl: %w", err)
	}
	schema, err := releasedContractForSubjectLine(source, "WorkbenchSubject.pkl", contractVersion, "0.1.0", "0.2.0", "0.3.0", currentContractVersion)
	if err != nil {
		return contract.Subject{}, err
	}
	return evaluator.EvaluateSubject(ctx, source, schema)
}

func recordSnapshot(ctx context.Context, root, output string, environment commandEnvironment) (string, error) {
	result, err := environment.setup(ctx, root)
	if err != nil {
		return "", err
	}
	snapshotURI := releasedContractURI(currentContractVersion, "WorkbenchSnapshot.pkl")
	resources := make([]snapshot.Resource, 0, len(result.Resources))
	for _, resource := range result.Resources {
		checkout := filepath.Join(root, filepath.FromSlash(resource.CanonicalPath))
		head, err := gitText(ctx, checkout, "rev-parse", "HEAD")
		if err != nil {
			return "", err
		}
		if err := requirePublicCommit(ctx, resource.GitHub, head); err != nil {
			return "", fmt.Errorf("snapshot resource %q: %w", resource.Identity, err)
		}
		resources = append(resources, snapshot.Resource{Identity: resource.Identity, Shape: resource.Shape, GitHub: resource.GitHub, CanonicalPath: resource.CanonicalPath, Commit: head})
	}
	value, err := snapshot.Record(resources)
	if err != nil {
		return "", err
	}
	encoded := renderSnapshot(value, snapshotURI)
	path, err := workbenchPath(root, output)
	if err != nil {
		return "", err
	}
	if err := atomicWrite(path, encoded); err != nil {
		return "", err
	}
	return fmt.Sprintf("Recorded Workbench Snapshot at %s", output), nil
}

func requirePublicCommit(ctx context.Context, github, commit string) error {
	output, err := git(ctx, "", "ls-remote", "https://github.com/"+github+".git")
	if err != nil {
		return fmt.Errorf("read anonymous public refs: %w", err)
	}
	for _, line := range strings.Split(string(output), "\n") {
		if strings.HasPrefix(line, commit+"\t") {
			return nil
		}
	}
	return fmt.Errorf("commit %s is not reachable from an advertised public ref", commit)
}

func renderSnapshot(value contract.WorkbenchSnapshot, contractURI string) []byte {
	identities := make([]string, 0, len(value.Resources))
	for identity := range value.Resources {
		identities = append(identities, identity)
	}
	sort.Strings(identities)
	var output strings.Builder
	fmt.Fprintf(&output, "amends %s\n\nresources {\n", strconv.Quote(contractURI))
	for _, identity := range identities {
		resource := value.Resources[identity]
		fmt.Fprintf(&output, "  [%s] {\n", strconv.Quote(identity))
		if resource.Shape.Kind == contract.PackageScopeShape {
			fmt.Fprintf(&output, "    shape = new PackageScopeShape { scope = %s }\n", strconv.Quote(resource.Shape.Scope))
		} else {
			output.WriteString("    shape = new RepositoryShape {}\n")
		}
		fmt.Fprintf(&output, "    github = %s\n    canonicalPath = %s\n    commit = %s\n  }\n", strconv.Quote(resource.GitHub), strconv.Quote(resource.CanonicalPath), strconv.Quote(resource.Commit))
	}
	output.WriteString("}\n")
	return []byte(output.String())
}

func reproduceSnapshot(ctx context.Context, root, input string, evaluator evaluate.Evaluator) (string, error) {
	path, err := workbenchPath(root, input)
	if err != nil {
		return "", err
	}
	source, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read Workbench Snapshot: %w", err)
	}
	schema, kind, err := releasedSnapshotContractFromSource(source)
	if err != nil {
		return "", err
	}
	var value contract.WorkbenchSnapshot
	switch kind {
	case currentSnapshotContract:
		value, err = evaluator.EvaluateWorkbenchSnapshot(ctx, source, schema)
	case legacyV020V030SnapshotContract:
		value, err = evaluator.EvaluateLegacyV020V030Snapshot(ctx, source, schema)
	default:
		return "", errors.New("snapshot contract selection is invalid")
	}
	if err != nil {
		return "", err
	}
	observer := &snapshotGit{ctx: ctx, root: root, resources: value.Resources}
	plan, err := snapshot.Plan(value, observer)
	if err != nil {
		return "", err
	}
	if err := snapshot.Apply(plan, observer); err != nil {
		return "", err
	}
	verified, err := snapshot.Plan(value, observer)
	if err != nil {
		return "", err
	}
	acquireCount, verifiedCount := verified.Counts()
	if acquireCount != 0 || verifiedCount != len(value.Resources) {
		return "", errors.New("snapshot reproduction did not converge to the exact participating-repository revisions")
	}
	return snapshotReproductionReport(verifiedCount), nil
}

func snapshotReproductionReport(repositoryCount int) string {
	return fmt.Sprintf("Reproduced and verified %d exact %s", repositoryCount, plural(repositoryCount, "repository", "repositories"))
}

type snapshotGit struct {
	ctx       context.Context
	root      string
	resources map[string]contract.SnapshotResource
}

func (repositories *snapshotGit) Observe(canonicalPath string) (snapshot.Checkout, error) {
	path := filepath.Join(repositories.root, filepath.FromSlash(canonicalPath))
	info, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		return snapshot.Checkout{}, nil
	}
	if err != nil {
		return snapshot.Checkout{}, fmt.Errorf("observe snapshot path %q: %w", canonicalPath, err)
	}
	if !info.IsDir() {
		return snapshot.Checkout{}, fmt.Errorf("observe snapshot path %q: destination is not a directory", canonicalPath)
	}
	resource, exists := repositories.resourceAt(canonicalPath)
	if !exists {
		return snapshot.Checkout{}, fmt.Errorf("snapshot has no resource at %q", canonicalPath)
	}
	github, err := originGitHub(repositories.ctx, path)
	if err != nil {
		return snapshot.Checkout{}, err
	}
	identity, err := (contract.Declaration{Shape: resource.Shape}).Identity(github)
	if err != nil {
		return snapshot.Checkout{}, err
	}
	head, err := gitText(repositories.ctx, path, "rev-parse", "HEAD")
	if err != nil {
		return snapshot.Checkout{}, err
	}
	status, err := git(repositories.ctx, path, "status", "--porcelain=v2", "-z", "--untracked-files=all")
	if err != nil {
		return snapshot.Checkout{}, err
	}
	return snapshot.Checkout{Exists: true, GitHub: github, Identity: identity, Commit: head, Clean: len(status) == 0}, nil
}

func (repositories *snapshotGit) CreateExactIfAbsent(acquisition snapshot.Acquisition) error {
	path := filepath.Join(repositories.root, filepath.FromSlash(acquisition.CanonicalPath))
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("snapshot destination %q is no longer absent", acquisition.CanonicalPath)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	if _, err := git(repositories.ctx, "", "clone", "--no-checkout", "https://github.com/"+acquisition.GitHub+".git", path); err != nil {
		return err
	}
	if _, err := git(repositories.ctx, path, "cat-file", "-e", acquisition.Commit+"^{commit}"); err != nil {
		return fmt.Errorf("exact public commit is absent after clone: %w", err)
	}
	_, err := git(repositories.ctx, path, "checkout", "--detach", acquisition.Commit)
	return err
}

func (repositories *snapshotGit) resourceAt(path string) (contract.SnapshotResource, bool) {
	for _, resource := range repositories.resources {
		if resource.CanonicalPath == path {
			return resource, true
		}
	}
	return contract.SnapshotResource{}, false
}

func pruneCheckouts(ctx context.Context, root string, identities []string, environment commandEnvironment) (string, error) {
	result, err := environment.setup(ctx, root)
	if err != nil {
		return "", err
	}
	managed, err := setup.ReadManagedCheckouts(root)
	if err != nil {
		return "", err
	}
	managedByID := make(map[string]setup.ManagedCheckout, len(managed))
	for _, checkout := range managed {
		managedByID[checkout.Identity] = checkout
	}
	orphans := make(map[string]orphan.Candidate, len(result.Orphans))
	for _, candidate := range result.Orphans {
		orphans[candidate.Identity] = candidate
	}
	candidates := make([]orphan.Candidate, 0, len(identities))
	disposable := make(map[string]bool, len(identities))
	for _, identity := range identities {
		candidate, exists := orphans[identity]
		if !exists {
			return "", fmt.Errorf("prune identity %q is not a currently reported orphan", identity)
		}
		managedCheckout, exists := managedByID[identity]
		if !exists || !managedCheckout.CreatedByWorkbench {
			return "", fmt.Errorf("prune identity %q lacks durable Workbench-created provenance", identity)
		}
		candidates = append(candidates, candidate)
		disposable[identity] = true
	}
	participatingRepositories := make([]orphan.Resource, 0, len(result.Resources))
	for _, resource := range result.Resources {
		participatingRepositories = append(participatingRepositories, orphan.Resource{Identity: resource.Identity, GitHub: resource.GitHub, Shape: resource.Shape, CanonicalPath: resource.CanonicalPath})
	}
	observe := func(candidate orphan.Candidate) (orphan.Observation, error) {
		return observeOrphan(ctx, candidate, disposable[candidate.Identity])
	}
	plan, err := orphan.Preflight(orphan.Request{Root: root, RepositoryClosure: participatingRepositories, Candidates: candidates}, observe)
	if err != nil {
		return "", err
	}
	selectedPaths := make(map[string]struct{}, len(candidates))
	for _, candidate := range candidates {
		selectedPaths[candidate.Path] = struct{}{}
	}
	receipt, err := orphan.Apply(plan, observe, func(path string) error {
		if _, selected := selectedPaths[path]; !selected {
			return fmt.Errorf("refuse unselected prune path %q", path)
		}
		return os.RemoveAll(path)
	})
	if err != nil {
		return "", fmt.Errorf("prune partial after %d removals: %w", len(receipt.RemovedPaths), err)
	}
	return fmt.Sprintf("Pruned %d independently recoverable checkouts", len(receipt.RemovedPaths)), nil
}

func observeOrphan(ctx context.Context, candidate orphan.Candidate, disposable bool) (orphan.Observation, error) {
	info, err := os.Stat(candidate.Path)
	if errors.Is(err, os.ErrNotExist) {
		return orphan.Observation{}, nil
	}
	if err != nil || !info.IsDir() {
		return orphan.Observation{}, fmt.Errorf("observe orphan path: %w", err)
	}
	urls, err := git(ctx, candidate.Path, "remote", "get-url", "--all", "origin")
	if err != nil {
		return orphan.Observation{}, err
	}
	urlLines := nonemptyLines(string(urls))
	github := ""
	if len(urlLines) == 1 {
		github, _ = githubFromRemote(urlLines[0])
	}
	branch, _ := gitText(ctx, candidate.Path, "branch", "--show-current")
	head, _ := gitText(ctx, candidate.Path, "rev-parse", "HEAD")
	upstreamBranch, _ := gitText(ctx, candidate.Path, "rev-parse", "--abbrev-ref", "@{upstream}")
	upstreamHead, _ := gitText(ctx, candidate.Path, "rev-parse", "@{upstream}")
	status, statusErr := git(ctx, candidate.Path, "status", "--porcelain=v2", "-z", "--untracked-files=all")
	if statusErr != nil {
		return orphan.Observation{}, statusErr
	}
	remoteHead := ""
	if branch != "" {
		remoteRef := "refs/heads/" + branch
		remote, remoteErr := git(ctx, candidate.Path, "ls-remote", "--refs", "origin", remoteRef)
		if remoteErr != nil {
			return orphan.Observation{}, remoteErr
		}
		lines := nonemptyLines(string(remote))
		if len(lines) == 1 {
			fields := strings.Fields(lines[0])
			if len(fields) == 2 && fields[1] == remoteRef {
				remoteHead = fields[0]
			}
		}
	}
	nested, err := hasNestedCheckout(candidate.Path)
	if err != nil {
		return orphan.Observation{}, err
	}
	return orphan.Observation{Exists: true, OriginCount: len(urlLines), OriginGitHub: github, Branch: branch, Head: head, UpstreamBranch: upstreamBranch, UpstreamHead: upstreamHead, RemoteHead: remoteHead, Status: string(status), NestedCheckout: nested, Disposable: disposable}, nil
}

func hasNestedCheckout(root string) (bool, error) {
	found := false
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if path == filepath.Join(root, ".git") {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if path != root && entry.Name() == ".git" {
			found = true
			if entry.IsDir() {
				return filepath.SkipDir
			}
		}
		return nil
	})
	return found, err
}

func workbenchPath(root, relative string) (string, error) {
	if relative == "" || relative == "." || filepath.IsAbs(relative) || filepath.Clean(relative) != relative || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("path %q is not a normalized workbench-relative path", relative)
	}
	return filepath.Join(root, relative), nil
}

func atomicWrite(path string, contents []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), ".workbench-snapshot-*")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if _, err := temporary.Write(contents); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return err
	}
	directory, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}

func originGitHub(ctx context.Context, repository string) (string, error) {
	remote, err := gitText(ctx, repository, "remote", "get-url", "origin")
	if err != nil {
		return "", err
	}
	return githubFromRemote(remote)
}

func githubFromRemote(remote string) (string, error) {
	value := strings.TrimSuffix(remote, ".git")
	if strings.HasPrefix(value, "git@github.com:") {
		value = strings.TrimPrefix(value, "git@github.com:")
	} else {
		parsed, err := url.Parse(value)
		if err != nil || parsed.Hostname() != "github.com" {
			return "", fmt.Errorf("origin %q is not a GitHub repository", remote)
		}
		value = strings.TrimPrefix(parsed.Path, "/")
	}
	return contract.NormalizeGitHubRepository(value)
}

func nonemptyLines(value string) []string {
	var result []string
	for _, line := range strings.Split(strings.TrimSpace(value), "\n") {
		if line != "" {
			result = append(result, line)
		}
	}
	return result
}

func gitText(ctx context.Context, directory string, arguments ...string) (string, error) {
	output, err := git(ctx, directory, arguments...)
	return strings.TrimSpace(string(output)), err
}

func git(ctx context.Context, directory string, arguments ...string) ([]byte, error) {
	command := exec.CommandContext(ctx, "git", arguments...)
	if directory != "" {
		command.Dir = directory
	}
	output, err := command.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("git %s: %w: %s", strings.Join(arguments, " "), err, strings.TrimSpace(string(output)))
	}
	return output, nil
}
