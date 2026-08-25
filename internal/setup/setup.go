package setup

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

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
)

var (
	amendsPattern = regexp.MustCompile(`^\s*amends\s+"([^"\r\n]+)"`)
	importPattern = regexp.MustCompile(`(?m)(?:from\s+|import\s*)["']([^"']+)["']`)
)

type Result struct {
	Resources       []Resource
	Orphans         []orphan.Candidate
	ChangedPaths    []string
	ContractVersion string
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
	if version == "0.2.0" || version == "0.3.0" || version == "0.4.0" {
		var ok bool
		evaluatorVersioned, ok = toolchain.Evaluator.(versionedEvaluator)
		if !ok {
			return Result{}, fmt.Errorf("Workbench %s setup requires an explicitly configured Pkl evaluator", version)
		}
	}
	if err := migration.Apply(); err != nil {
		return Result{}, err
	}
	source := &discoverySource{
		ctx:                ctx,
		root:               root,
		workLine:           subject.WorkLine,
		evaluator:          toolchain.Evaluator,
		evaluatorVersioned: evaluatorVersioned,
		version:            version,
		declarations:       make(map[string]contract.PackageScopeRepository),
		v020Declarations:   make(map[string]contract.Declaration),
	}
	var closure repositoryclosure.Closure
	var resources []Resource
	if version == "0.1.0" {
		closure, err = repositoryclosure.Discover(subject, source)
		if err == nil {
			resources = legacyResources(closure)
		}
	} else {
		var declared repositoryclosure.DeclaredClosure
		declared, err = repositoryclosure.DiscoverDeclarations(subject, source)
		if err == nil {
			resources = declaredResources(declared)
		}
	}
	if err != nil {
		return Result{}, err
	}
	previousReceipt := migration.receipt

	desired := make([]gitreconcile.Checkout, 0, len(resources))
	created := make(map[string]bool, len(resources))
	for _, resource := range resources {
		canonicalPath := filepath.Join(root, filepath.FromSlash(resource.CanonicalPath))
		_, statErr := os.Stat(canonicalPath)
		created[resource.Identity] = errors.Is(statErr, os.ErrNotExist)
		if err := os.MkdirAll(filepath.Dir(canonicalPath), 0o755); err != nil {
			return Result{}, fmt.Errorf("create canonical checkout parent: %w", err)
		}
		desired = append(desired, gitreconcile.Checkout{
			Path:       canonicalPath,
			RemoteURL:  "https://github.com/" + resource.GitHub,
			Branch:     subject.WorkLine.Branch,
			BaseBranch: subject.WorkLine.BaseBranch,
		})
	}
	if err := gitreconcile.Reconcile(ctx, desired); err != nil {
		return Result{}, fmt.Errorf("reconcile canonical checkouts: %w", err)
	}

	packages, err := observePackages(root, resources, version)
	if err != nil {
		return Result{}, err
	}
	skillPlan, err := planSkills(root, resources, version, previousReceipt)
	if err != nil {
		return Result{}, err
	}
	projection, err := workspace.Build(packages)
	if err != nil {
		return Result{}, fmt.Errorf("build workspace projection: %w", err)
	}
	changed, err := workspace.Apply(root, projection)
	if err != nil {
		return Result{}, fmt.Errorf("apply workspace projection: %w", err)
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
	convergencePlan, err := planSkills(root, resources, version, previousReceipt)
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
	if version == "0.2.0" || version == "0.3.0" || version == "0.4.0" {
		orientationChanged, err := projectOrientation(ctx, root, subject, resources, evaluatorVersioned, version)
		if err != nil {
			return Result{}, err
		}
		if orientationChanged {
			changed = append(changed, "AGENTS.md")
		}
	}
	sort.Strings(changed)
	return Result{Resources: resources, Orphans: orphans, ChangedPaths: changed, ContractVersion: version}, nil
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
	root               string
	workLine           contract.WorkLine
	evaluator          contractEvaluator
	evaluatorVersioned versionedEvaluator
	version            string
	declarations       map[string]contract.PackageScopeRepository
	v020Declarations   map[string]contract.Declaration
}

func (source *discoverySource) LoadRepository(identity string) (contract.PackageScopeRepository, error) {
	if declaration, exists := source.declarations[identity]; exists {
		return declaration, nil
	}
	discoveryPath := filepath.Join(source.root, ".workbench", "discovery", strings.ReplaceAll(identity, "/", "--"))
	if err := os.MkdirAll(filepath.Dir(discoveryPath), 0o755); err != nil {
		return contract.PackageScopeRepository{}, fmt.Errorf("create discovery checkout parent: %w", err)
	}
	if err := gitreconcile.Reconcile(source.ctx, []gitreconcile.Checkout{{
		Path:       discoveryPath,
		RemoteURL:  "https://github.com/" + identity,
		Branch:     source.workLine.Branch,
		BaseBranch: source.workLine.BaseBranch,
	}}); err != nil {
		return contract.PackageScopeRepository{}, fmt.Errorf("acquire declaration for %q: %w", identity, err)
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
	discoveryPath := filepath.Join(source.root, ".workbench", "discovery", strings.ReplaceAll(github, "/", "--"))
	if err := os.MkdirAll(filepath.Dir(discoveryPath), 0o755); err != nil {
		return contract.Declaration{}, fmt.Errorf("create discovery checkout parent: %w", err)
	}
	if err := gitreconcile.Reconcile(source.ctx, []gitreconcile.Checkout{{
		Path: discoveryPath, RemoteURL: "https://github.com/" + github,
		Branch: source.workLine.Branch, BaseBranch: source.workLine.BaseBranch,
	}}); err != nil {
		return contract.Declaration{}, fmt.Errorf("acquire declaration for %q: %w", github, err)
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
	if source.version != version || (version != "0.2.0" && version != "0.3.0" && version != "0.4.0") {
		return contract.Declaration{}, fmt.Errorf("repository %q declaration is %s, want exact %s", github, version, source.version)
	}
	var declaration contract.Declaration
	if filename == "PackageScopeRepository.pkl" {
		if version == "0.3.0" || version == "0.4.0" {
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
	path := filepath.Join(source.root, filepath.FromSlash(canonicalPath))
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
	if (source.version == "0.2.0" || source.version == "0.3.0" || source.version == "0.4.0") && strings.HasPrefix(filepath.ToSlash(canonicalPath), "pkg/") {
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
		value, err := evaluate.LocalContract(uri, localRepositoryDeclarationContract)
		return value, "0.2.0", err
	case localV020AgentInstructionsURI:
		value, err := evaluate.LocalContract(uri, localAgentInstructionsContract)
		return value, "0.2.0", err
	case localV030SubjectURI:
		value, err := evaluate.LocalContract(uri, localSubjectContract)
		return value, "0.3.0", err
	case localV030PackageScopeURI:
		value, err := evaluate.LocalContract(uri, localRepositoryContract)
		return value, "0.3.0", err
	case localV030RepositoryURI:
		value, err := evaluate.LocalContract(uri, localRepositoryDeclarationContract)
		return value, "0.3.0", err
	case localV030AgentInstructionsURI:
		value, err := evaluate.LocalContract(uri, localAgentInstructionsContract)
		return value, "0.3.0", err
	case localV040SubjectURI:
		value, err := evaluate.LocalContract(uri, localSubjectContract)
		return value, "0.4.0", err
	case localV040PackageScopeURI:
		value, err := evaluate.LocalContract(uri, localRepositoryContract)
		return value, "0.4.0", err
	case localV040RepositoryURI:
		value, err := evaluate.LocalContract(uri, localRepositoryDeclarationContract)
		return value, "0.4.0", err
	case localV040AgentInstructionsURI:
		value, err := evaluate.LocalContract(uri, localAgentInstructionsContract)
		return value, "0.4.0", err
	default:
		version := ""
		for _, candidate := range []string{"0.1.0", "0.2.0", "0.3.0", "0.4.0"} {
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

func observePackages(root string, resources []Resource, contractVersion string) ([]workspace.Package, error) {
	result := make([]workspace.Package, 0)
	for _, resource := range resources {
		resourceRoot := filepath.Join(root, filepath.FromSlash(resource.CanonicalPath))
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
			relative, err := filepath.Rel(root, packageRoot)
			if err != nil {
				return nil, err
			}
			imports, err := sourceImports(filepath.Join(packageRoot, "src"))
			if err != nil {
				return nil, fmt.Errorf("observe package %q imports: %w", name, err)
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
	if (contractVersion == "0.3.0" || contractVersion == "0.4.0") && resource.Shape.Kind == contract.PackageScopeShape {
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

func sourceImports(root string) ([]string, error) {
	set := make(map[string]struct{})
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
		for _, match := range importPattern.FindAllSubmatch(contents, -1) {
			set[string(match[1])] = struct{}{}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	result := make([]string, 0, len(set))
	for value := range set {
		result = append(result, value)
	}
	sort.Strings(result)
	return result, nil
}

type skillProjectionEntry struct {
	destination string
	prefix      string
	selected    []skills.Skill
	projection  skills.Projection
}

type skillProjectionPlan struct {
	legacy  bool
	entries []skillProjectionEntry
}

func planSkills(root string, resources []Resource, contractVersion string, previous managedCheckoutReceipt) (skillProjectionPlan, error) {
	sources := make([]skills.Source, 0, len(resources))
	for _, resource := range resources {
		sources = append(sources, skills.Source{Root: filepath.Join(root, filepath.FromSlash(resource.CanonicalPath))})
	}
	var inventory skills.Inventory
	var err error
	if contractVersion == "0.1.0" {
		inventory, err = skills.DiscoverLegacy(sources)
	} else {
		inventory, err = skills.Discover(sources)
	}
	if err != nil {
		return skillProjectionPlan{}, fmt.Errorf("discover assembled skills: %w", err)
	}
	workbenchSelection := contract.SkillSelection{}
	plan := skillProjectionPlan{legacy: contractVersion == "0.1.0", entries: make([]skillProjectionEntry, 0, len(resources)+1)}
	for _, consumer := range resources {
		editing := contract.SkillSelection{}
		for _, policy := range consumer.Includes {
			if policy.Editing != nil {
				editing = mergeSelection(editing, *policy.Editing)
			}
			if policy.Workbench != nil {
				workbenchSelection = mergeSelection(workbenchSelection, *policy.Workbench)
			}
		}
		selected, err := skills.Select(inventory, editing)
		if err != nil {
			return skillProjectionPlan{}, fmt.Errorf("select editing skills for %q: %w", consumer.Identity, err)
		}
		entry := skillProjectionEntry{
			destination: filepath.Join(root, filepath.FromSlash(consumer.CanonicalPath)),
			prefix:      consumer.CanonicalPath,
			selected:    selected,
		}
		if !plan.legacy {
			tracks, observerErr := trackedSkillPathObserver(entry.destination)
			if observerErr != nil {
				return skillProjectionPlan{}, fmt.Errorf("observe tracked editing skill paths for %q: %w", consumer.Identity, observerErr)
			}
			entry.projection, err = skills.PlanWithTracking(entry.destination, selected, tracks)
			if err != nil {
				return skillProjectionPlan{}, fmt.Errorf("plan editing skills for %q: %w", consumer.Identity, err)
			}
		}
		plan.entries = append(plan.entries, entry)
	}
	if !plan.legacy {
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
			tracks, observerErr := trackedSkillPathObserver(destination)
			if observerErr != nil {
				return skillProjectionPlan{}, fmt.Errorf("observe tracked retired skill paths for %q: %w", retired.Identity, observerErr)
			}
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
	}
	selected, err := skills.Select(inventory, workbenchSelection)
	if err != nil {
		return skillProjectionPlan{}, fmt.Errorf("select workbench skills: %w", err)
	}
	entry := skillProjectionEntry{destination: root, selected: selected}
	if !plan.legacy {
		tracks, observerErr := trackedSkillPathObserver(root)
		if observerErr != nil {
			return skillProjectionPlan{}, fmt.Errorf("observe tracked workbench skill paths: %w", observerErr)
		}
		entry.projection, err = skills.PlanWithTracking(root, selected, tracks)
		if err != nil {
			return skillProjectionPlan{}, fmt.Errorf("plan workbench skills: %w", err)
		}
	}
	plan.entries = append(plan.entries, entry)
	return plan, nil
}

func (plan skillProjectionPlan) Apply() ([]string, error) {
	changedByEntry := make([][]string, len(plan.entries))
	if plan.legacy {
		for index, entry := range plan.entries {
			paths, err := skills.ApplyLegacy(entry.destination, entry.selected)
			if err != nil {
				return nil, fmt.Errorf("apply legacy skill projection at %q: %w", entry.destination, err)
			}
			changedByEntry[index] = paths
		}
	} else {
		projections := make([]skills.Projection, 0, len(plan.entries))
		for _, entry := range plan.entries {
			projections = append(projections, entry.projection)
		}
		var err error
		changedByEntry, err = skills.ApplyPlans(projections)
		if err != nil {
			return nil, fmt.Errorf("apply skill projections: %w", err)
		}
	}
	changed := make([]string, 0)
	for index, paths := range changedByEntry {
		for _, path := range paths {
			changed = append(changed, filepath.ToSlash(filepath.Join(plan.entries[index].prefix, path)))
		}
	}
	return changed, nil
}

func trackedSkillPathObserver(root string) (skills.TrackedPathObserver, error) {
	_, err := os.Stat(filepath.Join(root, ".git"))
	if errors.Is(err, os.ErrNotExist) {
		return func(string) (bool, error) { return false, nil }, nil
	}
	if err != nil {
		return nil, fmt.Errorf("observe Git boundary at %q: %w", root, err)
	}
	return func(relativePath string) (bool, error) {
		command := exec.Command("git", "ls-files", "-z", "--", filepath.ToSlash(relativePath))
		command.Dir = root
		output, err := command.CombinedOutput()
		if err != nil {
			return false, fmt.Errorf("observe tracked path %q: %w: %s", relativePath, err, strings.TrimSpace(string(output)))
		}
		return len(output) != 0, nil
	}, nil
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
