package setup

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/phosphorco/workbench-go/internal/buildable"
	"github.com/phosphorco/workbench-go/internal/contract"
	"github.com/phosphorco/workbench-go/internal/evaluate"
	"github.com/phosphorco/workbench-go/internal/gitreconcile"
	"github.com/phosphorco/workbench-go/internal/orphan"
	"github.com/phosphorco/workbench-go/internal/repositoryclosure"
	"github.com/phosphorco/workbench-go/internal/skills"
	"github.com/phosphorco/workbench-go/internal/workspace"
)

const (
	localSubjectURI               = "workbench-contract:/WorkbenchSubject.pkl"
	localRepositoryURI            = "workbench-contract:/PackageScopeRepository.pkl"
	localV020SubjectURI           = "workbench-contract:/0.2.0/WorkbenchSubject.pkl"
	localV020PackageScopeURI      = "workbench-contract:/0.2.0/PackageScopeRepository.pkl"
	localV020RepositoryURI        = "workbench-contract:/0.2.0/Repository.pkl"
	localV020AgentInstructionsURI = "workbench-contract:/0.2.0/AgentInstructions.pkl"
	localV030SubjectURI           = "workbench-contract:/0.3.0/WorkbenchSubject.pkl"
	localV030PackageScopeURI      = "workbench-contract:/0.3.0/PackageScopeRepository.pkl"
	localV030RepositoryURI        = "workbench-contract:/0.3.0/Repository.pkl"
	localV030AgentInstructionsURI = "workbench-contract:/0.3.0/AgentInstructions.pkl"
	localV040SubjectURI           = "workbench-contract:/0.4.0/WorkbenchSubject.pkl"
	localV040PackageScopeURI      = "workbench-contract:/0.4.0/PackageScopeRepository.pkl"
	localV040RepositoryURI        = "workbench-contract:/0.4.0/Repository.pkl"
	localV040AgentInstructionsURI = "workbench-contract:/0.4.0/AgentInstructions.pkl"
	localV050SubjectURI           = "workbench-contract:/0.5.0/WorkbenchSubject.pkl"
	localV050PackageScopeURI      = "workbench-contract:/0.5.0/PackageScopeRepository.pkl"
	localV050RepositoryURI        = "workbench-contract:/0.5.0/Repository.pkl"
	localV050AgentInstructionsURI = "workbench-contract:/0.5.0/AgentInstructions.pkl"
	localV060SubjectURI           = "workbench-contract:/0.6.0/WorkbenchSubject.pkl"
	localV060PackageScopeURI      = "workbench-contract:/0.6.0/PackageScopeRepository.pkl"
	localV060RepositoryURI        = "workbench-contract:/0.6.0/Repository.pkl"
	localV060AgentInstructionsURI = "workbench-contract:/0.6.0/AgentInstructions.pkl"
	localV061SubjectURI           = "workbench-contract:/0.6.1/WorkbenchSubject.pkl"
	localV061PackageScopeURI      = "workbench-contract:/0.6.1/PackageScopeRepository.pkl"
	localV061RepositoryURI        = "workbench-contract:/0.6.1/Repository.pkl"
	localV061AgentInstructionsURI = "workbench-contract:/0.6.1/AgentInstructions.pkl"
)

var (
	amendsPattern = regexp.MustCompile(`^\s*amends\s+"([^"\r\n]+)"`)
)

type Result struct {
	Resources       []Resource
	Orphans         []orphan.Candidate
	ChangedPaths    []string
	ContractVersion string
	SkillWarnings   []skills.Diagnostic
}

// Resource is setup's version-neutral view. Identity is derived by the
// selected contract while GitHub remains the independent checkout authority.
type Resource struct {
	Identity      string
	GitHub        string
	CanonicalPath string
	Shape         contract.ResourceShape
	Packages      map[string]contract.PackagePolicy
	Includes      []contract.SkillPolicy
}

type contractEvaluator interface {
	EvaluateSubject(context.Context, []byte, evaluate.Contract) (contract.Subject, error)
	EvaluatePackageScopeRepository(context.Context, []byte, evaluate.Contract) (contract.PackageScopeRepository, error)
}

type versionedEvaluator interface {
	contractEvaluator
	EvaluatePackageScopeDeclaration(context.Context, []byte, evaluate.Contract) (contract.Declaration, error)
	EvaluatePackageScopeDeclarationV030(context.Context, []byte, evaluate.Contract) (contract.Declaration, error)
	EvaluateRepositoryDeclaration(context.Context, []byte, evaluate.Contract) (contract.Declaration, error)
	EvaluateAgentInstructions(context.Context, []byte, evaluate.Contract) (contract.AgentInstructions, error)
}

// Toolchain is the complete executable authority used by released setup.
// Neither field is resolved from PATH by RunWith.
type Toolchain struct {
	Evaluator contractEvaluator
	Bun       string
}

func NewToolchain(evaluator evaluate.Evaluator, bun string) Toolchain {
	return Toolchain{Evaluator: evaluator, Bun: bun}
}

type ambientEvaluator struct{}

func (ambientEvaluator) EvaluateSubject(ctx context.Context, source []byte, schema evaluate.Contract) (contract.Subject, error) {
	return evaluate.EvaluateSubject(ctx, source, schema)
}
func (ambientEvaluator) EvaluatePackageScopeRepository(ctx context.Context, source []byte, schema evaluate.Contract) (contract.PackageScopeRepository, error) {
	return evaluate.EvaluatePackageScopeRepository(ctx, source, schema)
}

func Run(ctx context.Context, workbenchRoot string) (Result, error) {
	return run(ctx, workbenchRoot, Toolchain{Evaluator: ambientEvaluator{}, Bun: "bun"}, true)
}

// RunWith reconciles a Workbench using only the explicitly supplied evaluator and
// Bun executable. Released composition must use this entrypoint.
func RunWith(ctx context.Context, workbenchRoot string, toolchain Toolchain) (Result, error) {
	return run(ctx, workbenchRoot, toolchain, false)
}

func run(ctx context.Context, workbenchRoot string, toolchain Toolchain, ambientDev bool) (Result, error) {
	if toolchain.Evaluator == nil {
		return Result{}, fmt.Errorf("setup evaluator is absent")
	}
	if toolchain.Bun == "" {
		return Result{}, fmt.Errorf("setup Bun executable is absent")
	}
	if !ambientDev && (!filepath.IsAbs(toolchain.Bun) || filepath.Clean(toolchain.Bun) != toolchain.Bun) {
		return Result{}, fmt.Errorf("setup Bun executable %q is not an exact absolute path", toolchain.Bun)
	}
	root, err := filepath.Abs(workbenchRoot)
	if err != nil {
		return Result{}, fmt.Errorf("resolve workbench root: %w", err)
	}
	subjectSource, err := os.ReadFile(filepath.Join(root, "workbench-subject.pkl"))
	if err != nil {
		return Result{}, fmt.Errorf("read workbench-subject.pkl: %w", err)
	}
	subjectSchema, version, err := schemaForSource(subjectSource, "WorkbenchSubject.pkl")
	if err != nil {
		return Result{}, err
	}
	subject, err := toolchain.Evaluator.EvaluateSubject(ctx, subjectSource, subjectSchema)
	if err != nil {
		return Result{}, err
	}
	migration, err := preflightManagedCheckoutMigration(root)
	if err != nil {
		return Result{}, err
	}
	var evaluatorVersioned versionedEvaluator
	if isVersionedContract(version) {
		var ok bool
		evaluatorVersioned, ok = toolchain.Evaluator.(versionedEvaluator)
		if !ok {
			return Result{}, fmt.Errorf("Workbench %s setup requires an explicitly configured Pkl evaluator", version)
		}
	}
	scratchRoot, err := os.MkdirTemp("", "workbench-setup-preflight-*")
	if err != nil {
		return Result{}, fmt.Errorf("create setup preflight scratch: %w", err)
	}
	defer os.RemoveAll(scratchRoot)
	previousReceipt := migration.receipt
	overrides, err := existingSubjectSources(root, previousReceipt, subject.WorkLine)
	if err != nil {
		return Result{}, err
	}
	source := &discoverySource{
		ctx:                ctx,
		targetRoot:         root,
		scratchRoot:        scratchRoot,
		workLine:           subject.WorkLine,
		evaluator:          toolchain.Evaluator,
		evaluatorVersioned: evaluatorVersioned,
		version:            version,
		declarations:       make(map[string]contract.PackageScopeRepository),
		v020Declarations:   make(map[string]contract.Declaration),
		checkouts:          make(map[string]string),
		commits:            make(map[string]string),
		overrides:          overrides,
	}
	var resources []Resource
	for {
		resources, err = discoverResources(subject, source, version)
		if err != nil {
			return Result{}, err
		}
		added, err := source.preferCanonicalSubjectSources(resources)
		if err != nil {
			return Result{}, err
		}
		if !added {
			break
		}
	}
	sourceRoots := make(map[string]string, len(resources))
	for _, resource := range resources {
		checkout, exists := source.checkouts[resource.GitHub]
		if !exists {
			return Result{}, fmt.Errorf("preflight source for %q was not acquired", resource.GitHub)
		}
		sourceRoots[resource.Identity] = checkout
	}
	if version == "0.1.0" {
		if err := rejectRetiredV010SkillSources(resources, sourceRoots); err != nil {
			return Result{}, err
		}
	}
	catalog, catalogObservations, err := loadSkillCatalog(resources, sourceRoots)
	if err != nil {
		return Result{}, err
	}
	report := catalog.Report()
	if len(report.Issues) != 0 {
		return Result{}, skillCatalogError(report.Issues)
	}
	skillPlan, err := planSkillsWithCatalog(root, resources, previousReceipt, catalog, sourceRoots)
	if err != nil {
		return Result{}, err
	}
	packages, err := observePackagesAt(ctx, resources, version, sourceRoots, toolchain.Bun)
	if err != nil {
		return Result{}, err
	}
	projection, err := workspace.BuildWithOptions(packages, workspace.BuildOptions{
		ReassembleRootDependencies: version == "0.6.0" || version == "0.6.1",
		ProductionTypeScript:       version == "0.6.0" || version == "0.6.1",
	})
	if err != nil {
		return Result{}, fmt.Errorf("build workspace projection: %w", err)
	}
	buildableProjection, err := encodeBuildableProjection(resources, source.v020Declarations, sourceRoots)
	if err != nil {
		return Result{}, err
	}

	desired := make([]gitreconcile.Checkout, 0, len(resources))
	created := make(map[string]bool, len(resources))
	for _, resource := range resources {
		canonicalPath := filepath.Join(root, filepath.FromSlash(resource.CanonicalPath))
		_, statErr := os.Stat(canonicalPath)
		created[resource.Identity] = errors.Is(statErr, os.ErrNotExist)
		commit := source.commits[resource.GitHub]
		if commit == "" {
			return Result{}, fmt.Errorf("preflight source for %q has no exact commit", resource.GitHub)
		}
		desired = append(desired, gitreconcile.Checkout{
			Path:           canonicalPath,
			RemoteURL:      "https://github.com/" + resource.GitHub,
			Branch:         subject.WorkLine.Branch,
			BaseBranch:     subject.WorkLine.BaseBranch,
			ExpectedCommit: commit,
		})
	}
	canonicalChanges, err := gitreconcile.Prepare(ctx, desired)
	if err != nil {
		return Result{}, fmt.Errorf("reconcile canonical checkouts: %w", err)
	}
	if err := reobserveCatalogs(catalogObservations); err != nil {
		return Result{}, err
	}
	if err := gitreconcile.Apply(ctx, canonicalChanges); err != nil {
		return Result{}, fmt.Errorf("reconcile canonical checkouts: %w", err)
	}
	if err := verifyCanonicalCatalogs(resources, root, catalogObservations); err != nil {
		return Result{}, err
	}
	if err := migration.Apply(); err != nil {
		return Result{}, err
	}
	changed, err := workspace.Apply(root, projection)
	if err != nil {
		return Result{}, fmt.Errorf("apply workspace projection: %w", err)
	}
	buildablesChanged, err := writeWholeOutput(filepath.Join(root, filepath.FromSlash(buildable.ProjectionPath)), buildableProjection)
	if err != nil {
		return Result{}, fmt.Errorf("apply buildable projection: %w", err)
	}
	if buildablesChanged {
		changed = append(changed, buildable.ProjectionPath)
	}

	skillChanges, err := skillPlan.Apply()
	if err != nil {
		return Result{}, err
	}
	changed = append(changed, skillChanges...)
	if err := reconcileDependencies(ctx, root, toolchain.Bun); err != nil {
		return Result{}, err
	}
	workspaceRemainder, err := workspace.Apply(root, projection)
	if err != nil {
		return Result{}, fmt.Errorf("confirm workspace projection convergence: %w", err)
	}
	convergencePlan, err := planSkillsWithCatalog(root, resources, previousReceipt, catalog, nil)
	if err != nil {
		return Result{}, fmt.Errorf("plan skill projection convergence: %w", err)
	}
	skillRemainder, err := convergencePlan.Apply()
	if err != nil {
		return Result{}, fmt.Errorf("confirm skill projection convergence: %w", err)
	}
	if len(workspaceRemainder) != 0 || len(skillRemainder) != 0 {
		return Result{}, fmt.Errorf("Workbench-owned projections did not converge: workspace=%v skills=%v", workspaceRemainder, skillRemainder)
	}
	orphans, err := reportOrphans(root, resources, previousReceipt)
	if err != nil {
		return Result{}, err
	}
	receiptChanged, err := writeManagedCheckoutReceipt(root, resources, previousReceipt, created)
	if err != nil {
		return Result{}, err
	}
	if receiptChanged {
		changed = append(changed, ".workbench/managed-checkouts.json")
	}
	if isVersionedContract(version) {
		orientationChanged, err := projectOrientation(ctx, root, subject, resources, evaluatorVersioned, version)
		if err != nil {
			return Result{}, err
		}
		if orientationChanged {
			changed = append(changed, "AGENTS.md")
		}
	}
	sort.Strings(changed)
	return Result{Resources: resources, Orphans: orphans, ChangedPaths: changed, ContractVersion: version, SkillWarnings: report.Warnings}, nil
}

func discoverResources(subject contract.Subject, source *discoverySource, version string) ([]Resource, error) {
	if version == "0.1.0" {
		closure, err := repositoryclosure.Discover(subject, source)
		if err != nil {
			return nil, err
		}
		return legacyResources(closure), nil
	}
	declared, err := repositoryclosure.DiscoverDeclarations(subject, source)
	if err != nil {
		return nil, err
	}
	return declaredResources(declared), nil
}

func isVersionedContract(version string) bool {
	return version == "0.2.0" || version == "0.3.0" || version == "0.4.0" || version == "0.5.0" || version == "0.6.0" || version == "0.6.1"
}

func reconcileDependencies(ctx context.Context, root, bun string) error {
	command := exec.CommandContext(ctx, bun, "install", "--ignore-scripts")
	command.Dir = root
	command.Env = os.Environ()
	output, err := command.CombinedOutput()
	if err != nil {
		return fmt.Errorf("reconcile dependency links: %w: %s", err, strings.TrimSpace(string(output)))
	}
	return nil
}

type discoverySource struct {
	ctx                context.Context
	targetRoot         string
	scratchRoot        string
	workLine           contract.WorkLine
	evaluator          contractEvaluator
	evaluatorVersioned versionedEvaluator
	version            string
	declarations       map[string]contract.PackageScopeRepository
	v020Declarations   map[string]contract.Declaration
	checkouts          map[string]string
	commits            map[string]string
	overrides          map[string]string
}

func (source *discoverySource) acquire(github string) (string, error) {
	if checkout, exists := source.checkouts[github]; exists {
		return checkout, nil
	}
	if checkout, exists := source.overrides[github]; exists {
		commit, err := runSetupGit(checkout, "rev-parse", "HEAD")
		if err != nil {
			return "", fmt.Errorf("observe existing Subject source for %q: %w", github, err)
		}
		source.checkouts[github] = checkout
		source.commits[github] = strings.TrimSpace(commit)
		return checkout, nil
	}
	checkout := filepath.Join(source.scratchRoot, "repositories", strings.ReplaceAll(github, "/", "--"))
	if err := os.MkdirAll(filepath.Dir(checkout), 0o755); err != nil {
		return "", fmt.Errorf("create scratch repository parent: %w", err)
	}
	changes, err := gitreconcile.Prepare(source.ctx, []gitreconcile.Checkout{{
		Path:       checkout,
		RemoteURL:  "https://github.com/" + github,
		Branch:     source.workLine.Branch,
		BaseBranch: source.workLine.BaseBranch,
	}})
	if err != nil {
		return "", fmt.Errorf("acquire scratch repository for %q: %w", github, err)
	}
	prepared := changes.Prepared()
	if len(prepared) != 1 || prepared[0].Commit == "" {
		return "", fmt.Errorf("acquire scratch repository for %q: prepared revision is absent", github)
	}
	if err := gitreconcile.Apply(source.ctx, changes); err != nil {
		return "", fmt.Errorf("acquire scratch repository for %q: %w", github, err)
	}
	source.checkouts[github] = checkout
	source.commits[github] = prepared[0].Commit
	return checkout, nil
}

func existingSubjectSources(root string, receipt managedCheckoutReceipt, workLine contract.WorkLine) (map[string]string, error) {
	result := make(map[string]string)
	for _, resource := range receipt.Resources {
		path := filepath.Join(root, filepath.FromSlash(resource.CanonicalPath))
		matches, err := isCanonicalSubjectSource(path, resource.GitHub, workLine.Branch)
		if err != nil {
			return nil, fmt.Errorf("observe existing source %q: %w", resource.Identity, err)
		}
		if matches {
			result[resource.GitHub] = path
		}
	}
	return result, nil
}

func (source *discoverySource) preferCanonicalSubjectSources(resources []Resource) (bool, error) {
	added := false
	for _, resource := range resources {
		if _, exists := source.overrides[resource.GitHub]; exists {
			continue
		}
		path := filepath.Join(source.targetRoot, filepath.FromSlash(resource.CanonicalPath))
		matches, err := isCanonicalSubjectSource(path, resource.GitHub, source.workLine.Branch)
		if err != nil {
			return false, fmt.Errorf("observe canonical Subject source %q: %w", resource.Identity, err)
		}
		if !matches {
			localScratch, ok, err := source.scratchLocalSubject(resource.GitHub, path)
			if err != nil {
				return false, fmt.Errorf("observe local Subject source %q: %w", resource.Identity, err)
			}
			if !ok {
				continue
			}
			path = localScratch
		}
		source.overrides[resource.GitHub] = path
		delete(source.checkouts, resource.GitHub)
		delete(source.commits, resource.GitHub)
		added = true
	}
	if added {
		source.declarations = make(map[string]contract.PackageScopeRepository)
		source.v020Declarations = make(map[string]contract.Declaration)
	}
	return added, nil
}

func (source *discoverySource) scratchLocalSubject(github, canonicalPath string) (string, bool, error) {
	info, err := os.Lstat(canonicalPath)
	if errors.Is(err, os.ErrNotExist) {
		return "", false, nil
	}
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return "", false, err
	}
	top, err := runSetupGit(canonicalPath, "rev-parse", "--show-toplevel")
	if err != nil {
		return "", false, nil
	}
	absoluteTop, err := filepath.Abs(strings.TrimSpace(top))
	if err != nil || filepath.Clean(absoluteTop) != filepath.Clean(canonicalPath) {
		return "", false, nil
	}
	origin, err := runSetupGit(canonicalPath, "config", "--get", "remote.origin.url")
	if err != nil || strings.TrimSpace(origin) != "https://github.com/"+github {
		return "", false, nil
	}
	localCommit, err := runSetupGit(canonicalPath, "for-each-ref", "--format=%(objectname)", "refs/heads/"+source.workLine.Branch)
	if err != nil || strings.TrimSpace(localCommit) == "" {
		return "", false, nil
	}
	checkout := filepath.Join(source.scratchRoot, "local-subjects", strings.ReplaceAll(github, "/", "--"))
	changes, err := gitreconcile.Prepare(source.ctx, []gitreconcile.Checkout{{
		Path: checkout, RemoteURL: canonicalPath,
		Branch: source.workLine.Branch, BaseBranch: source.workLine.BaseBranch,
		ExpectedCommit: strings.TrimSpace(localCommit),
	}})
	if err != nil {
		return "", false, err
	}
	if err := gitreconcile.Apply(source.ctx, changes); err != nil {
		return "", false, err
	}
	return checkout, true, nil
}

func isCanonicalSubjectSource(path, github, branchName string) (bool, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return false, nil
	}
	top, err := runSetupGit(path, "rev-parse", "--show-toplevel")
	if err != nil {
		return false, nil
	}
	absoluteTop, err := filepath.Abs(strings.TrimSpace(top))
	if err != nil || filepath.Clean(absoluteTop) != filepath.Clean(path) {
		return false, nil
	}
	origin, err := runSetupGit(path, "config", "--get", "remote.origin.url")
	if err != nil || strings.TrimSpace(origin) != "https://github.com/"+github {
		return false, nil
	}
	branch, err := runSetupGit(path, "symbolic-ref", "--quiet", "--short", "HEAD")
	if err != nil || strings.TrimSpace(branch) != branchName {
		return false, nil
	}
	return true, nil
}

func (source *discoverySource) LoadRepository(identity string) (contract.PackageScopeRepository, error) {
	if declaration, exists := source.declarations[identity]; exists {
		return declaration, nil
	}
	discoveryPath, err := source.acquire(identity)
	if err != nil {
		return contract.PackageScopeRepository{}, err
	}
	encoded, err := os.ReadFile(filepath.Join(discoveryPath, "workbench.pkl"))
	if err != nil {
		return contract.PackageScopeRepository{}, fmt.Errorf("read %q workbench.pkl: %w", identity, err)
	}
	schema, version, err := schemaForSource(encoded, "PackageScopeRepository.pkl")
	if err != nil {
		return contract.PackageScopeRepository{}, fmt.Errorf("select %q repository contract: %w", identity, err)
	}
	if version != "0.1.0" || source.version != "0.1.0" {
		return contract.PackageScopeRepository{}, fmt.Errorf("repository %q declaration is %s, want exact 0.1.0", identity, version)
	}
	declaration, err := source.evaluator.EvaluatePackageScopeRepository(source.ctx, encoded, schema)
	if err != nil {
		return contract.PackageScopeRepository{}, fmt.Errorf("evaluate %q workbench.pkl: %w", identity, err)
	}
	source.declarations[identity] = declaration
	return declaration, nil
}

func (source *discoverySource) LoadDeclaration(github string) (contract.Declaration, error) {
	if declaration, exists := source.v020Declarations[github]; exists {
		return declaration, nil
	}
	discoveryPath, err := source.acquire(github)
	if err != nil {
		return contract.Declaration{}, err
	}
	encoded, err := os.ReadFile(filepath.Join(discoveryPath, "workbench.pkl"))
	if err != nil {
		return contract.Declaration{}, fmt.Errorf("read %q workbench.pkl: %w", github, err)
	}
	filename, err := amendedFilename(encoded)
	if err != nil {
		return contract.Declaration{}, fmt.Errorf("select %q repository contract: %w", github, err)
	}
	if filename != "PackageScopeRepository.pkl" && filename != "Repository.pkl" {
		return contract.Declaration{}, fmt.Errorf("select %q repository contract: unsupported declaration module %q", github, filename)
	}
	schema, version, err := schemaForSource(encoded, filename)
	if err != nil {
		return contract.Declaration{}, fmt.Errorf("select %q repository contract: %w", github, err)
	}
	if source.version != version || !isVersionedContract(version) {
		return contract.Declaration{}, fmt.Errorf("repository %q declaration is %s, want exact %s", github, version, source.version)
	}
	var declaration contract.Declaration
	if filename == "PackageScopeRepository.pkl" {
		if version == "0.3.0" || version == "0.4.0" || version == "0.5.0" || version == "0.6.0" || version == "0.6.1" {
			declaration, err = source.evaluatorVersioned.EvaluatePackageScopeDeclarationV030(source.ctx, encoded, schema)
		} else {
			declaration, err = source.evaluatorVersioned.EvaluatePackageScopeDeclaration(source.ctx, encoded, schema)
		}
	} else {
		declaration, err = source.evaluatorVersioned.EvaluateRepositoryDeclaration(source.ctx, encoded, schema)
	}
	if err != nil {
		return contract.Declaration{}, fmt.Errorf("evaluate %q workbench.pkl: %w", github, err)
	}
	source.v020Declarations[github] = declaration
	return declaration, nil
}

func (source *discoverySource) IdentityAt(canonicalPath string) (string, bool, error) {
	path := filepath.Join(source.targetRoot, filepath.FromSlash(canonicalPath))
	info, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	if !info.IsDir() {
		return "", true, fmt.Errorf("canonical path is not a directory")
	}
	command := exec.CommandContext(source.ctx, "git", "config", "--get", "remote.origin.url")
	command.Dir = path
	command.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	output, err := command.CombinedOutput()
	if err != nil {
		return "", true, fmt.Errorf("read origin: %w: %s", err, strings.TrimSpace(string(output)))
	}
	identity, err := contract.GitHubIdentity(strings.TrimSpace(string(output)))
	if err != nil {
		return "", true, err
	}
	if isVersionedContract(source.version) && strings.HasPrefix(filepath.ToSlash(canonicalPath), "pkg/") {
		// PackageScope identity is derived from the closed placement arm, while
		// origin remains independent and must still name the declaration source.
		for github, declaration := range source.v020Declarations {
			path, pathErr := declaration.CanonicalPath(github)
			if pathErr == nil && path == filepath.ToSlash(canonicalPath) && identity != github {
				return identity, true, nil
			}
		}
		return strings.TrimPrefix(filepath.ToSlash(canonicalPath), "pkg/"), true, nil
	}
	return identity, true, nil
}

func amendedFilename(source []byte) (string, error) {
	match := amendsPattern.FindSubmatch(source)
	if match == nil {
		return "", fmt.Errorf("Pkl source must begin with amends")
	}
	uri := string(match[1])
	fragment := uri
	if hash := strings.LastIndex(fragment, "#/"); hash >= 0 {
		fragment = fragment[hash+2:]
	}
	return filepath.Base(fragment), nil
}

func schemaForSource(source []byte, filename string) (evaluate.Contract, string, error) {
	match := amendsPattern.FindSubmatch(source)
	if match == nil {
		return evaluate.Contract{}, "", fmt.Errorf("Pkl source must begin with amends")
	}
	uri := string(match[1])
	if !strings.HasSuffix(uri, "/"+filename) && !strings.HasSuffix(uri, "#/"+filename) {
		return evaluate.Contract{}, "", fmt.Errorf("Pkl source amends %q, want %s contract", uri, filename)
	}
	switch uri {
	case localSubjectURI:
		if filename != "WorkbenchSubject.pkl" {
			return evaluate.Contract{}, "", fmt.Errorf("local Subject contract used for %s", filename)
		}
		value, err := evaluate.LocalContract(uri, localSubjectContract)
		return value, "0.1.0", err
	case localRepositoryURI:
		if filename != "PackageScopeRepository.pkl" {
			return evaluate.Contract{}, "", fmt.Errorf("local repository contract used for %s", filename)
		}
		value, err := evaluate.LocalContract(uri, legacyLocalRepositoryContract)
		return value, "0.1.0", err
	case localV020SubjectURI:
		value, err := evaluate.LocalContract(uri, localSubjectContract)
		return value, "0.2.0", err
	case localV020PackageScopeURI:
		value, err := evaluate.LocalContract(uri, localV020RepositoryContract)
		return value, "0.2.0", err
	case localV020RepositoryURI:
		value, err := evaluate.LocalContract(uri, localV050RepositoryDeclarationContract)
		return value, "0.2.0", err
	case localV020AgentInstructionsURI:
		value, err := evaluate.LocalContract(uri, localAgentInstructionsContract)
		return value, "0.2.0", err
	case localV030SubjectURI:
		value, err := evaluate.LocalContract(uri, localSubjectContract)
		return value, "0.3.0", err
	case localV030PackageScopeURI:
		value, err := evaluate.LocalContract(uri, localV050RepositoryContract)
		return value, "0.3.0", err
	case localV030RepositoryURI:
		value, err := evaluate.LocalContract(uri, localV050RepositoryDeclarationContract)
		return value, "0.3.0", err
	case localV030AgentInstructionsURI:
		value, err := evaluate.LocalContract(uri, localAgentInstructionsContract)
		return value, "0.3.0", err
	case localV040SubjectURI:
		value, err := evaluate.LocalContract(uri, localSubjectContract)
		return value, "0.4.0", err
	case localV040PackageScopeURI:
		value, err := evaluate.LocalContract(uri, localV050RepositoryContract)
		return value, "0.4.0", err
	case localV040RepositoryURI:
		value, err := evaluate.LocalContract(uri, localV050RepositoryDeclarationContract)
		return value, "0.4.0", err
	case localV040AgentInstructionsURI:
		value, err := evaluate.LocalContract(uri, localAgentInstructionsContract)
		return value, "0.4.0", err
	case localV050SubjectURI:
		value, err := evaluate.LocalContract(uri, localSubjectContract)
		return value, "0.5.0", err
	case localV050PackageScopeURI:
		value, err := evaluate.LocalContract(uri, localV050RepositoryContract)
		return value, "0.5.0", err
	case localV050RepositoryURI:
		value, err := evaluate.LocalContract(uri, localV050RepositoryDeclarationContract)
		return value, "0.5.0", err
	case localV050AgentInstructionsURI:
		value, err := evaluate.LocalContract(uri, localAgentInstructionsContract)
		return value, "0.5.0", err
	case localV060SubjectURI:
		value, err := evaluate.LocalContract(uri, localSubjectContract)
		return value, "0.6.0", err
	case localV060PackageScopeURI:
		value, err := evaluate.LocalContract(uri, localRepositoryContract)
		return value, "0.6.0", err
	case localV060RepositoryURI:
		value, err := evaluate.LocalContract(uri, localRepositoryDeclarationContract)
		return value, "0.6.0", err
	case localV060AgentInstructionsURI:
		value, err := evaluate.LocalContract(uri, localAgentInstructionsContract)
		return value, "0.6.0", err
	case localV061SubjectURI:
		value, err := evaluate.LocalContract(uri, localSubjectContract)
		return value, "0.6.1", err
	case localV061PackageScopeURI:
		value, err := evaluate.LocalContract(uri, localRepositoryContract)
		return value, "0.6.1", err
	case localV061RepositoryURI:
		value, err := evaluate.LocalContract(uri, localRepositoryDeclarationContract)
		return value, "0.6.1", err
	case localV061AgentInstructionsURI:
		value, err := evaluate.LocalContract(uri, localAgentInstructionsContract)
		return value, "0.6.1", err
	default:
		version := ""
		for _, candidate := range []string{"0.1.0", "0.2.0", "0.3.0", "0.4.0", "0.5.0", "0.6.0", "0.6.1"} {
			exact := "package://github.com/phosphorco/workbench-go/releases/download/" + candidate + "/workbench@" + candidate + "#/" + filename
			if uri == exact {
				version = candidate
			}
		}
		if version == "" {
			return evaluate.Contract{}, "", fmt.Errorf("unsupported Workbench contract release in %q", uri)
		}
		if version == "0.1.0" && filename != "WorkbenchSubject.pkl" && filename != "PackageScopeRepository.pkl" {
			return evaluate.Contract{}, "", fmt.Errorf("Workbench 0.1.0 has no %s contract", filename)
		}
		value, err := evaluate.ReleasedContract(uri)
		return value, version, err
	}
}

// EvaluateCurrentDeclaration evaluates one caller-local repository declaration
// against the bundled candidate schema. It is intentionally narrower than
// setup: cold lifecycle commands need the current owner declaration before a
// generated projection can exist, but must not acquire or assemble a closure.
func EvaluateCurrentDeclaration(ctx context.Context, evaluator evaluate.Evaluator, source []byte) (contract.Declaration, error) {
	filename, err := amendedFilename(source)
	if err != nil {
		return contract.Declaration{}, err
	}
	if filename != "PackageScopeRepository.pkl" && filename != "Repository.pkl" {
		return contract.Declaration{}, fmt.Errorf("unsupported declaration module %q", filename)
	}
	schema, version, err := schemaForSource(source, filename)
	if err != nil {
		return contract.Declaration{}, err
	}
	if version != "0.6.0" && version != "0.6.1" {
		return contract.Declaration{}, fmt.Errorf("buildable lifecycle requires exact 0.6.1 declaration, got %s", version)
	}
	if filename == "PackageScopeRepository.pkl" {
		return evaluator.EvaluatePackageScopeDeclarationV030(ctx, source, schema)
	}
	return evaluator.EvaluateRepositoryDeclaration(ctx, source, schema)
}

func observePackages(root string, resources []Resource, contractVersion string) ([]workspace.Package, error) {
	resourceRoots := make(map[string]string, len(resources))
	for _, resource := range resources {
		resourceRoots[resource.Identity] = filepath.Join(root, filepath.FromSlash(resource.CanonicalPath))
	}
	bun, err := exec.LookPath("bun")
	if err != nil {
		return nil, fmt.Errorf("locate Bun for package observation: %w", err)
	}
	return observePackagesAt(context.Background(), resources, contractVersion, resourceRoots, bun)
}

func observePackagesAt(ctx context.Context, resources []Resource, contractVersion string, resourceRoots map[string]string, bun string) ([]workspace.Package, error) {
	result := make([]workspace.Package, 0)
	for _, resource := range resources {
		resourceRoot, exists := resourceRoots[resource.Identity]
		if !exists {
			return nil, fmt.Errorf("source root for %q is absent", resource.Identity)
		}
		names := make([]string, 0, len(resource.Packages))
		for name := range resource.Packages {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			packageRoot, err := locatePackage(resourceRoot, resource, name, contractVersion, len(names) == 1)
			if err != nil {
				return nil, fmt.Errorf("locate package %q in %q: %w", name, resource.Identity, err)
			}
			relativeInResource, err := filepath.Rel(resourceRoot, packageRoot)
			if err != nil {
				return nil, err
			}
			relative := filepath.Join(filepath.FromSlash(resource.CanonicalPath), relativeInResource)
			imports, err := sourceImports(ctx, filepath.Join(packageRoot, "src"), bun)
			if err != nil {
				return nil, fmt.Errorf("observe package %q imports: %w", name, err)
			}
			for index := range imports {
				imports[index].Source = filepath.ToSlash(filepath.Join(relative, "src", imports[index].Source))
			}
			result = append(result, workspace.Package{
				Name:      name,
				Directory: relative,
				Imports:   imports,
				Policy:    resource.Packages[name],
			})
		}
	}
	return result, nil
}

func locatePackage(resourceRoot string, resource Resource, name, contractVersion string, allowRoot bool) (string, error) {
	if (contractVersion == "0.3.0" || contractVersion == "0.4.0" || contractVersion == "0.5.0" || contractVersion == "0.6.0" || contractVersion == "0.6.1") && resource.Shape.Kind == contract.PackageScopeShape {
		return locatePackageScopePackage(resourceRoot, resource.Shape.Scope, name)
	}
	return locateLegacyPackage(resourceRoot, name, allowRoot)
}

func locatePackageScopePackage(resourceRoot, scope, name string) (string, error) {
	declaration := contract.Declaration{
		Shape:    contract.ResourceShape{Kind: contract.PackageScopeShape, Scope: scope},
		Packages: map[string]contract.PackagePolicy{name: {}},
	}
	leaf, err := declaration.PackageDirectory(name)
	if err != nil {
		return "", err
	}

	canonical, err := canonicalPackageScopeSourceExists(resourceRoot, leaf)
	if err != nil {
		return "", fmt.Errorf("observe canonical source directory %q: %w", filepath.ToSlash(filepath.Join(leaf, "src")), err)
	}
	nonCanonical := make([]string, 0, 2)
	for _, relative := range []string{"src", filepath.Join("packages", leaf, "src")} {
		exists, err := sourceLayoutExists(filepath.Join(resourceRoot, relative))
		if err != nil {
			return "", fmt.Errorf("observe non-canonical source directory %q: %w", filepath.ToSlash(relative), err)
		}
		if exists {
			nonCanonical = append(nonCanonical, filepath.ToSlash(relative))
		}
	}
	want := filepath.ToSlash(filepath.Join(leaf, "src"))
	if len(nonCanonical) != 0 {
		return "", fmt.Errorf("PackageScope package %q has non-canonical source layout(s) %q; requires %q", name, nonCanonical, want)
	}
	if !canonical {
		return "", fmt.Errorf("PackageScope package %q requires canonical source directory %q", name, want)
	}
	return filepath.Join(resourceRoot, leaf), nil
}

func canonicalPackageScopeSourceExists(resourceRoot, leaf string) (bool, error) {
	rootInfo, err := os.Lstat(resourceRoot)
	if err != nil {
		return false, err
	}
	if rootInfo.Mode()&os.ModeSymlink != 0 || !rootInfo.IsDir() {
		return false, fmt.Errorf("PackageScope checkout root must be a real directory, not a symlink")
	}
	realRoot, err := filepath.EvalSymlinks(resourceRoot)
	if err != nil {
		return false, fmt.Errorf("resolve PackageScope checkout root: %w", err)
	}

	packageRoot := filepath.Join(resourceRoot, leaf)
	packageInfo, err := os.Lstat(packageRoot)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if packageInfo.Mode()&os.ModeSymlink != 0 || !packageInfo.IsDir() {
		return false, fmt.Errorf("canonical package directory %q must be a real directory, not a symlink", filepath.ToSlash(leaf))
	}
	if err := requireRealDirectoryWithin(realRoot, packageRoot); err != nil {
		return false, fmt.Errorf("canonical package directory %q: %w", filepath.ToSlash(leaf), err)
	}

	sourceRoot := filepath.Join(packageRoot, "src")
	sourceInfo, err := os.Lstat(sourceRoot)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if sourceInfo.Mode()&os.ModeSymlink != 0 || !sourceInfo.IsDir() {
		return false, fmt.Errorf("canonical source directory %q must be a real directory, not a symlink", filepath.ToSlash(filepath.Join(leaf, "src")))
	}
	if err := requireRealDirectoryWithin(realRoot, sourceRoot); err != nil {
		return false, fmt.Errorf("canonical source directory %q: %w", filepath.ToSlash(filepath.Join(leaf, "src")), err)
	}
	return true, nil
}

func requireRealDirectoryWithin(realRoot, path string) error {
	realPath, err := filepath.EvalSymlinks(path)
	if err != nil {
		return fmt.Errorf("resolve real directory: %w", err)
	}
	relative, err := filepath.Rel(realRoot, realPath)
	if err != nil {
		return fmt.Errorf("compare real directory with PackageScope checkout: %w", err)
	}
	if relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return fmt.Errorf("real directory %q escapes the PackageScope checkout", realPath)
	}
	return nil
}

func sourceLayoutExists(path string) (bool, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return info.IsDir() || info.Mode()&os.ModeSymlink != 0, nil
}

func locateLegacyPackage(resourceRoot string, name string, allowRoot bool) (string, error) {
	leaf := name
	if slash := strings.LastIndex(name, "/"); slash >= 0 {
		leaf = name[slash+1:]
	}
	candidates := []string{filepath.Join(resourceRoot, "packages", leaf), filepath.Join(resourceRoot, leaf)}
	if allowRoot {
		candidates = append([]string{resourceRoot}, candidates...)
	}
	for _, candidate := range candidates {
		if info, err := os.Stat(filepath.Join(candidate, "src")); err == nil && info.IsDir() {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("no source directory matches package leaf %q", leaf)
}

func sourceImports(ctx context.Context, root, bun string) ([]workspace.Import, error) {
	files := make([]typeScriptSourceFile, 0)
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() || (!strings.HasSuffix(path, ".ts") && !strings.HasSuffix(path, ".tsx")) {
			return nil
		}
		contents, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		loader := "ts"
		if strings.HasSuffix(path, ".tsx") {
			loader = "tsx"
		}
		files = append(files, typeScriptSourceFile{source: contents, path: filepath.ToSlash(relative), loader: loader, development: isDevelopmentSource(relative)})
		return nil
	})
	if err != nil {
		return nil, err
	}
	observed, err := parserBackedTypeScriptImports(ctx, bun, files)
	if err != nil {
		return nil, err
	}
	sort.Slice(observed, func(left, right int) bool {
		if observed[left].Source != observed[right].Source {
			return observed[left].Source < observed[right].Source
		}
		if observed[left].Line != observed[right].Line {
			return observed[left].Line < observed[right].Line
		}
		return observed[left].Specifier < observed[right].Specifier
	})
	return observed, nil
}

func isDevelopmentSource(path string) bool {
	base := filepath.Base(path)
	return strings.Contains(base, ".test.") || strings.Contains(base, ".spec.") || strings.Contains(filepath.ToSlash(path), "/__tests__/")
}

type catalogObservation struct {
	identity string
	root     string
	digest   string
}

func loadSkillCatalog(resources []Resource, resourceRoots map[string]string) (skills.Catalog, []catalogObservation, error) {
	sources := make([]skills.Source, 0, len(resources))
	observations := make([]catalogObservation, 0, len(resources))
	for _, resource := range resources {
		resourceRoot, exists := resourceRoots[resource.Identity]
		if !exists {
			return skills.Catalog{}, nil, fmt.Errorf("skill source root for %q is absent", resource.Identity)
		}
		catalogRoot := filepath.Join(resourceRoot, "skills")
		digest, exists, err := observeCatalogTree(catalogRoot)
		if err != nil {
			return skills.Catalog{}, nil, fmt.Errorf("observe %q skill catalog: %w", resource.GitHub, err)
		}
		observations = append(observations, catalogObservation{identity: resource.Identity, root: catalogRoot, digest: digest})
		if exists {
			sources = append(sources, skills.Source{Name: resource.GitHub, Root: catalogRoot})
		}
	}
	catalog, err := skills.Load(sources)
	if err != nil {
		return skills.Catalog{}, nil, fmt.Errorf("load participating skill catalogs: %w", err)
	}
	return catalog, observations, nil
}

func observeCatalogTree(root string) (string, bool, error) {
	info, err := os.Lstat(root)
	if errors.Is(err, os.ErrNotExist) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return "", false, fmt.Errorf("catalog root is not a real directory")
	}
	hash := sha256.New()
	err = filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("catalog contains symlink %q", path)
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		if entry.IsDir() {
			fmt.Fprintf(hash, "d\x00%s\x00", filepath.ToSlash(relative))
			return nil
		}
		if !entry.Type().IsRegular() {
			return fmt.Errorf("catalog contains non-regular file %q", path)
		}
		contents, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		fmt.Fprintf(hash, "f\x00%s\x00%d\x00", filepath.ToSlash(relative), len(contents))
		_, _ = hash.Write(contents)
		return nil
	})
	if err != nil {
		return "", false, err
	}
	return fmt.Sprintf("sha256:%x", hash.Sum(nil)), true, nil
}

func reobserveCatalogs(observations []catalogObservation) error {
	for _, observation := range observations {
		digest, _, err := observeCatalogTree(observation.root)
		if err != nil {
			return fmt.Errorf("reobserve %q skill catalog: %w", observation.identity, err)
		}
		if digest != observation.digest {
			return fmt.Errorf("skill catalog for %q changed after preflight", observation.identity)
		}
	}
	return nil
}

func verifyCanonicalCatalogs(resources []Resource, root string, observations []catalogObservation) error {
	byIdentity := make(map[string]catalogObservation, len(observations))
	for _, observation := range observations {
		byIdentity[observation.identity] = observation
	}
	for _, resource := range resources {
		expected := byIdentity[resource.Identity]
		canonicalRoot := filepath.Join(root, filepath.FromSlash(resource.CanonicalPath), "skills")
		digest, _, err := observeCatalogTree(canonicalRoot)
		if err != nil {
			return fmt.Errorf("observe reconciled %q skill catalog: %w", resource.Identity, err)
		}
		if digest != expected.digest {
			return fmt.Errorf("reconciled skill catalog for %q differs from its validated source revision", resource.Identity)
		}
	}
	return nil
}

func skillCatalogError(issues []skills.Diagnostic) error {
	lines := make([]string, 0, len(issues))
	for _, issue := range issues {
		lines = append(lines, issue.Location()+": "+issue.Message)
	}
	return fmt.Errorf("skill catalog contains %d blocking issue(s):\n%s", len(lines), strings.Join(lines, "\n"))
}

func rejectRetiredV010SkillSources(resources []Resource, roots map[string]string) error {
	for _, resource := range resources {
		legacyRoot := filepath.Join(roots[resource.Identity], ".agents", "skills")
		entries, err := os.ReadDir(legacyRoot)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return fmt.Errorf("inspect retired 0.1 skill source at %q: %w", resource.GitHub, err)
		}
		for _, entry := range entries {
			if !entry.IsDir() {
				continue
			}
			relative := filepath.ToSlash(filepath.Join(".agents", "skills", entry.Name(), "SKILL.md"))
			if info, err := os.Lstat(filepath.Join(legacyRoot, entry.Name(), "SKILL.md")); err == nil && info.Mode().IsRegular() {
				return fmt.Errorf("%s:%s: Workbench 0.1 skill-source layout is retired; recreate this Git-owned skill under skills/%s/SKILL.md", resource.GitHub, relative, entry.Name())
			} else if err != nil && !errors.Is(err, os.ErrNotExist) {
				return fmt.Errorf("inspect retired 0.1 skill source %s:%s: %w", resource.GitHub, relative, err)
			}
		}
	}
	return nil
}

type skillProjectionEntry struct {
	destination string
	prefix      string
	selected    []skills.Skill
	projection  skills.Projection
}

type skillProjectionPlan struct {
	entries []skillProjectionEntry
}

func planSkills(root string, resources []Resource, previous managedCheckoutReceipt) (skillProjectionPlan, error) {
	resourceRoots := make(map[string]string, len(resources))
	for _, resource := range resources {
		resourceRoots[resource.Identity] = filepath.Join(root, filepath.FromSlash(resource.CanonicalPath))
	}
	catalog, _, err := loadSkillCatalog(resources, resourceRoots)
	if err != nil {
		return skillProjectionPlan{}, err
	}
	if report := catalog.Report(); len(report.Issues) != 0 {
		return skillProjectionPlan{}, skillCatalogError(report.Issues)
	}
	return planSkillsWithCatalog(root, resources, previous, catalog, nil)
}

func planSkillsWithCatalog(root string, resources []Resource, previous managedCheckoutReceipt, catalog skills.Catalog, sourceRoots map[string]string) (skillProjectionPlan, error) {
	plan := skillProjectionPlan{entries: make([]skillProjectionEntry, 0, len(resources)+1)}
	for _, consumer := range resources {
		editing := contract.SkillSelection{}
		for _, policy := range consumer.Includes {
			if policy.Editing != nil {
				editing = mergeSelection(editing, *policy.Editing)
			}
		}
		selected, err := skills.Select(catalog, editing)
		if err != nil {
			return skillProjectionPlan{}, fmt.Errorf("select editing skills for %q: %w", consumer.Identity, err)
		}
		if sourceRoot := sourceRoots[consumer.Identity]; sourceRoot != "" {
			if _, err := skills.PlanWithTracking(sourceRoot, selected, trackedSkillPathObserver(sourceRoot)); err != nil {
				return skillProjectionPlan{}, fmt.Errorf("preflight editing skill portability for %q: %w", consumer.Identity, err)
			}
		}
		entry := skillProjectionEntry{
			destination: filepath.Join(root, filepath.FromSlash(consumer.CanonicalPath)),
			prefix:      consumer.CanonicalPath,
			selected:    selected,
		}
		tracks := trackedSkillPathObserver(entry.destination)
		entry.projection, err = skills.PlanWithTracking(entry.destination, selected, tracks)
		if err != nil {
			return skillProjectionPlan{}, fmt.Errorf("plan editing skills for %q: %w", consumer.Identity, err)
		}
		plan.entries = append(plan.entries, entry)
	}
	current := make(map[string]struct{}, len(resources))
	for _, resource := range resources {
		current[resource.Identity] = struct{}{}
	}
	for _, retired := range previous.Resources {
		if _, exists := current[retired.Identity]; exists {
			continue
		}
		destination := filepath.Join(root, filepath.FromSlash(retired.CanonicalPath))
		info, statErr := os.Lstat(destination)
		if errors.Is(statErr, os.ErrNotExist) {
			continue
		}
		if statErr != nil {
			return skillProjectionPlan{}, fmt.Errorf("observe retired skill projection destination %q: %w", retired.Identity, statErr)
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return skillProjectionPlan{}, fmt.Errorf("retired skill projection destination %q is not a real directory", retired.Identity)
		}
		if err := validateRetiredSkillDestination(destination, retired); err != nil {
			return skillProjectionPlan{}, err
		}
		tracks := trackedSkillPathObserver(destination)
		projection, planErr := skills.PlanWithTracking(destination, nil, tracks)
		if planErr != nil {
			return skillProjectionPlan{}, fmt.Errorf("plan retired editing skill cleanup for %q: %w", retired.Identity, planErr)
		}
		plan.entries = append(plan.entries, skillProjectionEntry{
			destination: destination,
			prefix:      retired.CanonicalPath,
			projection:  projection,
		})
	}
	// The Workbench root is the closure-wide flat registry. Per-repository
	// editing projections remain attenuated by each consumer's include policy,
	// while the root reassembles every participating Git-owned skill source.
	selected, err := skills.Select(catalog, contract.SkillSelection{All: true})
	if err != nil {
		return skillProjectionPlan{}, fmt.Errorf("select workbench skills: %w", err)
	}
	entry := skillProjectionEntry{destination: root, selected: selected}
	tracks := trackedSkillPathObserver(root)
	entry.projection, err = skills.PlanWithTracking(root, selected, tracks)
	if err != nil {
		return skillProjectionPlan{}, fmt.Errorf("plan workbench skills: %w", err)
	}
	plan.entries = append(plan.entries, entry)
	return plan, nil
}

func (plan skillProjectionPlan) Apply() ([]string, error) {
	projections := make([]skills.Projection, 0, len(plan.entries))
	for _, entry := range plan.entries {
		projections = append(projections, entry.projection)
	}
	changedByEntry, err := skills.ApplyPlans(projections)
	if err != nil {
		return nil, fmt.Errorf("apply skill projections: %w", err)
	}
	changed := make([]string, 0)
	for index, paths := range changedByEntry {
		for _, path := range paths {
			changed = append(changed, filepath.ToSlash(filepath.Join(plan.entries[index].prefix, path)))
		}
	}
	return changed, nil
}

func trackedSkillPathObserver(root string) skills.TrackedPathObserver {
	return func(relativePath string) (bool, error) {
		_, err := os.Stat(filepath.Join(root, ".git"))
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		if err != nil {
			return false, fmt.Errorf("observe Git boundary at %q: %w", root, err)
		}
		command := exec.Command("git", "ls-files", "-z", "--", filepath.ToSlash(relativePath))
		command.Dir = root
		output, err := command.CombinedOutput()
		if err != nil {
			return false, fmt.Errorf("observe tracked path %q: %w: %s", relativePath, err, strings.TrimSpace(string(output)))
		}
		return len(output) != 0, nil
	}
}

func validateRetiredSkillDestination(destination string, retired receiptResource) error {
	resolved, err := filepath.EvalSymlinks(destination)
	if err != nil {
		return fmt.Errorf("resolve retired skill projection destination %q: %w", retired.Identity, err)
	}
	resolved, err = filepath.Abs(resolved)
	if err != nil {
		return fmt.Errorf("normalize retired skill projection destination %q: %w", retired.Identity, err)
	}
	destination, err = filepath.Abs(destination)
	if err != nil {
		return fmt.Errorf("normalize expected retired skill projection destination %q: %w", retired.Identity, err)
	}
	if filepath.Clean(resolved) != filepath.Clean(destination) {
		return fmt.Errorf("retired skill projection destination %q resolves outside its canonical path", retired.Identity)
	}
	declaration := contract.Declaration{Shape: retired.Shape}
	identity, identityErr := declaration.Identity(retired.GitHub)
	canonicalPath, pathErr := declaration.CanonicalPath(retired.GitHub)
	if identityErr != nil || pathErr != nil || identity != retired.Identity || canonicalPath != retired.CanonicalPath {
		return fmt.Errorf("retired skill projection destination %q disagrees with its closed identity or placement", retired.Identity)
	}
	topLevel, err := runSetupGit(destination, "rev-parse", "--show-toplevel")
	if err != nil {
		return fmt.Errorf("identify retired Git checkout %q: %w", retired.Identity, err)
	}
	actualRoot, err := filepath.Abs(strings.TrimSpace(topLevel))
	if err != nil || filepath.Clean(actualRoot) != filepath.Clean(destination) {
		return fmt.Errorf("retired skill projection destination %q is not its Git worktree root", retired.Identity)
	}
	origin, err := runSetupGit(destination, "config", "--get", "remote.origin.url")
	if err != nil {
		return fmt.Errorf("read retired Git origin %q: %w", retired.Identity, err)
	}
	wantOrigin := "https://github.com/" + retired.GitHub
	if strings.TrimSpace(origin) != wantOrigin {
		return fmt.Errorf("retired skill projection destination %q has origin %q, want %q", retired.Identity, strings.TrimSpace(origin), wantOrigin)
	}
	github, err := contract.GitHubIdentity(strings.TrimSpace(origin))
	if err != nil || github != retired.GitHub {
		return fmt.Errorf("retired skill projection destination %q has invalid GitHub identity", retired.Identity)
	}
	return nil
}

func runSetupGit(root string, arguments ...string) (string, error) {
	command := exec.Command("git", arguments...)
	command.Dir = root
	command.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	output, err := command.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git %s: %w: %s", strings.Join(arguments, " "), err, strings.TrimSpace(string(output)))
	}
	return string(output), nil
}

func mergeSelection(left contract.SkillSelection, right contract.SkillSelection) contract.SkillSelection {
	left.All = left.All || right.All
	left.Domains = appendUnique(left.Domains, right.Domains...)
	left.Names = appendUnique(left.Names, right.Names...)
	return left
}

func appendUnique(values []string, additions ...string) []string {
	set := make(map[string]struct{}, len(values)+len(additions))
	for _, value := range values {
		set[value] = struct{}{}
	}
	for _, value := range additions {
		set[value] = struct{}{}
	}
	result := make([]string, 0, len(set))
	for value := range set {
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}
