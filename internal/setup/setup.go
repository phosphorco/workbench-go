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
	"github.com/phosphorco/workbench-go/internal/skills"
	"github.com/phosphorco/workbench-go/internal/workspace"
	"github.com/phosphorco/workbench-go/internal/world"
)

const (
	localSubjectURI               = "workbench-contract:/WorkbenchSubject.pkl"
	localRepositoryURI            = "workbench-contract:/PackageScopeRepository.pkl"
	localV020SubjectURI           = "workbench-contract:/0.2.0/WorkbenchSubject.pkl"
	localV020PackageScopeURI      = "workbench-contract:/0.2.0/PackageScopeRepository.pkl"
	localV020RepositoryURI        = "workbench-contract:/0.2.0/Repository.pkl"
	localV020AgentInstructionsURI = "workbench-contract:/0.2.0/AgentInstructions.pkl"
)

var (
	amendsPattern = regexp.MustCompile(`^\s*amends\s+"([^"\r\n]+)"`)
	importPattern = regexp.MustCompile(`(?m)(?:from\s+|import\s*)["']([^"']+)["']`)
)

type Result struct {
	World        world.World
	Resources    []Resource
	Orphans      []orphan.Candidate
	ChangedPaths []string
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

type v020Evaluator interface {
	contractEvaluator
	EvaluatePackageScopeDeclaration(context.Context, []byte, evaluate.Contract) (contract.Declaration, error)
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

// RunWith reconciles a World using only the explicitly supplied evaluator and
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

	var evaluator020 v020Evaluator
	if version == "0.2.0" {
		var ok bool
		evaluator020, ok = toolchain.Evaluator.(v020Evaluator)
		if !ok {
			return Result{}, fmt.Errorf("Workbench 0.2.0 setup requires an explicitly configured Pkl evaluator")
		}
	}
	source := &discoverySource{
		ctx:              ctx,
		root:             root,
		workLine:         subject.WorkLine,
		evaluator:        toolchain.Evaluator,
		evaluator020:     evaluator020,
		version:          version,
		declarations:     make(map[string]contract.PackageScopeRepository),
		v020Declarations: make(map[string]contract.Declaration),
	}
	var legacy world.World
	var resources []Resource
	if version == "0.1.0" {
		legacy, err = world.Discover(subject, source)
		if err == nil {
			resources = legacyResources(legacy)
		}
	} else {
		var declared world.DeclaredWorld
		declared, err = world.DiscoverDeclarations(subject, source)
		if err == nil {
			resources = declaredResources(declared)
		}
	}
	if err != nil {
		return Result{}, err
	}
	previousReceipt, err := readWorldReceipt(root)
	if err != nil {
		return Result{}, err
	}

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

	packages, err := observePackages(root, resources)
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

	skillChanges, err := projectSkills(root, resources)
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
	skillRemainder, err := projectSkills(root, resources)
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
	receiptChanged, err := writeWorldReceipt(root, resources, previousReceipt, created)
	if err != nil {
		return Result{}, err
	}
	if receiptChanged {
		changed = append(changed, ".workbench/world.json")
	}
	if version == "0.2.0" {
		orientationChanged, err := projectOrientation(ctx, root, subject, resources, evaluator020)
		if err != nil {
			return Result{}, err
		}
		if orientationChanged {
			changed = append(changed, "AGENTS.md")
		}
	}
	sort.Strings(changed)
	return Result{World: legacy, Resources: resources, Orphans: orphans, ChangedPaths: changed}, nil
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
	ctx              context.Context
	root             string
	workLine         contract.WorkLine
	evaluator        contractEvaluator
	evaluator020     v020Evaluator
	version          string
	declarations     map[string]contract.PackageScopeRepository
	v020Declarations map[string]contract.Declaration
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
	if source.version != "0.2.0" || version != "0.2.0" {
		return contract.Declaration{}, fmt.Errorf("repository %q declaration is %s, want exact 0.2.0", github, version)
	}
	var declaration contract.Declaration
	if filename == "PackageScopeRepository.pkl" {
		declaration, err = source.evaluator020.EvaluatePackageScopeDeclaration(source.ctx, encoded, schema)
	} else {
		declaration, err = source.evaluator020.EvaluateRepositoryDeclaration(source.ctx, encoded, schema)
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
	if source.version == "0.2.0" && strings.HasPrefix(filepath.ToSlash(canonicalPath), "pkg/") {
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
		value, err := evaluate.LocalContract(uri, localRepositoryContract)
		return value, "0.2.0", err
	case localV020RepositoryURI:
		value, err := evaluate.LocalContract(uri, localRepositoryDeclarationContract)
		return value, "0.2.0", err
	case localV020AgentInstructionsURI:
		value, err := evaluate.LocalContract(uri, localAgentInstructionsContract)
		return value, "0.2.0", err
	default:
		version := ""
		for _, candidate := range []string{"0.1.0", "0.2.0"} {
			needle := "/releases/download/" + candidate + "/workbench@" + candidate + "#/"
			if strings.Contains(uri, needle) {
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

func observePackages(root string, resources []Resource) ([]workspace.Package, error) {
	result := make([]workspace.Package, 0)
	for _, resource := range resources {
		resourceRoot := filepath.Join(root, filepath.FromSlash(resource.CanonicalPath))
		names := make([]string, 0, len(resource.Packages))
		for name := range resource.Packages {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			packageRoot, err := locatePackage(resourceRoot, name, len(names) == 1)
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

func locatePackage(resourceRoot string, name string, allowRoot bool) (string, error) {
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

func projectSkills(root string, resources []Resource) ([]string, error) {
	sources := make([]skills.Source, 0, len(resources))
	for _, resource := range resources {
		sources = append(sources, skills.Source{Root: filepath.Join(root, filepath.FromSlash(resource.CanonicalPath))})
	}
	inventory, err := skills.Discover(sources)
	if err != nil {
		return nil, fmt.Errorf("discover assembled skills: %w", err)
	}
	workbenchSelection := contract.SkillSelection{}
	changed := make([]string, 0)
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
			return nil, fmt.Errorf("select editing skills for %q: %w", consumer.Identity, err)
		}
		paths, err := skills.Apply(filepath.Join(root, filepath.FromSlash(consumer.CanonicalPath)), selected)
		if err != nil {
			return nil, fmt.Errorf("project editing skills for %q: %w", consumer.Identity, err)
		}
		for _, path := range paths {
			changed = append(changed, filepath.ToSlash(filepath.Join(consumer.CanonicalPath, path)))
		}
	}
	selected, err := skills.Select(inventory, workbenchSelection)
	if err != nil {
		return nil, fmt.Errorf("select workbench skills: %w", err)
	}
	paths, err := skills.Apply(root, selected)
	if err != nil {
		return nil, fmt.Errorf("project workbench skills: %w", err)
	}
	changed = append(changed, paths...)
	return changed, nil
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
