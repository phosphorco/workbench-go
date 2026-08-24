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
	"github.com/phosphorco/workbench-go/internal/skills"
	"github.com/phosphorco/workbench-go/internal/workspace"
	"github.com/phosphorco/workbench-go/internal/world"
)

const (
	localSubjectURI    = "workbench-contract:/WorkbenchSubject.pkl"
	localRepositoryURI = "workbench-contract:/PackageScopeRepository.pkl"
)

var (
	amendsPattern = regexp.MustCompile(`^\s*amends\s+"([^"\r\n]+)"`)
	importPattern = regexp.MustCompile(`(?m)(?:from\s+|import\s*)["']([^"']+)["']`)
)

type Result struct {
	World        world.World
	ChangedPaths []string
}

func Run(ctx context.Context, workbenchRoot string) (Result, error) {
	root, err := filepath.Abs(workbenchRoot)
	if err != nil {
		return Result{}, fmt.Errorf("resolve workbench root: %w", err)
	}
	subjectSource, err := os.ReadFile(filepath.Join(root, "workbench-subject.pkl"))
	if err != nil {
		return Result{}, fmt.Errorf("read workbench-subject.pkl: %w", err)
	}
	subjectSchema, err := schemaForSource(subjectSource, "WorkbenchSubject.pkl")
	if err != nil {
		return Result{}, err
	}
	subject, err := evaluate.EvaluateSubject(ctx, subjectSource, subjectSchema)
	if err != nil {
		return Result{}, err
	}

	source := &discoverySource{
		ctx:          ctx,
		root:         root,
		workLine:     subject.WorkLine,
		declarations: make(map[string]contract.PackageScopeRepository),
	}
	discovered, err := world.Discover(subject, source)
	if err != nil {
		return Result{}, err
	}

	desired := make([]gitreconcile.Checkout, 0, len(discovered.Resources))
	for _, resource := range discovered.Resources {
		canonicalPath := filepath.Join(root, filepath.FromSlash(resource.CanonicalPath))
		if err := os.MkdirAll(filepath.Dir(canonicalPath), 0o755); err != nil {
			return Result{}, fmt.Errorf("create canonical checkout parent: %w", err)
		}
		desired = append(desired, gitreconcile.Checkout{
			Path:       canonicalPath,
			RemoteURL:  "https://github.com/" + resource.Identity,
			Branch:     subject.WorkLine.Branch,
			BaseBranch: subject.WorkLine.BaseBranch,
		})
	}
	if err := gitreconcile.Reconcile(ctx, desired); err != nil {
		return Result{}, fmt.Errorf("reconcile canonical checkouts: %w", err)
	}

	packages, err := observePackages(root, discovered)
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

	skillChanges, err := projectSkills(root, discovered)
	if err != nil {
		return Result{}, err
	}
	changed = append(changed, skillChanges...)
	if err := reconcileDependencies(ctx, root); err != nil {
		return Result{}, err
	}
	workspaceRemainder, err := workspace.Apply(root, projection)
	if err != nil {
		return Result{}, fmt.Errorf("confirm workspace projection convergence: %w", err)
	}
	skillRemainder, err := projectSkills(root, discovered)
	if err != nil {
		return Result{}, fmt.Errorf("confirm skill projection convergence: %w", err)
	}
	if len(workspaceRemainder) != 0 || len(skillRemainder) != 0 {
		return Result{}, fmt.Errorf("Workbench-owned projections did not converge: workspace=%v skills=%v", workspaceRemainder, skillRemainder)
	}
	sort.Strings(changed)
	return Result{World: discovered, ChangedPaths: changed}, nil
}

func reconcileDependencies(ctx context.Context, root string) error {
	command := exec.CommandContext(ctx, "bun", "install", "--ignore-scripts")
	command.Dir = root
	command.Env = os.Environ()
	output, err := command.CombinedOutput()
	if err != nil {
		return fmt.Errorf("reconcile dependency links: %w: %s", err, strings.TrimSpace(string(output)))
	}
	return nil
}

type discoverySource struct {
	ctx          context.Context
	root         string
	workLine     contract.WorkLine
	declarations map[string]contract.PackageScopeRepository
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
	schema, err := schemaForSource(encoded, "PackageScopeRepository.pkl")
	if err != nil {
		return contract.PackageScopeRepository{}, fmt.Errorf("select %q repository contract: %w", identity, err)
	}
	declaration, err := evaluate.EvaluatePackageScopeRepository(source.ctx, encoded, schema)
	if err != nil {
		return contract.PackageScopeRepository{}, fmt.Errorf("evaluate %q workbench.pkl: %w", identity, err)
	}
	source.declarations[identity] = declaration
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
	return identity, true, nil
}

func schemaForSource(source []byte, filename string) (evaluate.Contract, error) {
	match := amendsPattern.FindSubmatch(source)
	if match == nil {
		return evaluate.Contract{}, fmt.Errorf("Pkl source must begin with amends")
	}
	uri := string(match[1])
	if !strings.HasSuffix(uri, "/"+filename) && !strings.HasSuffix(uri, "#/"+filename) {
		return evaluate.Contract{}, fmt.Errorf("Pkl source amends %q, want %s contract", uri, filename)
	}
	switch uri {
	case localSubjectURI:
		if filename != "WorkbenchSubject.pkl" {
			return evaluate.Contract{}, fmt.Errorf("local Subject contract used for %s", filename)
		}
		return evaluate.LocalContract(uri, localSubjectContract)
	case localRepositoryURI:
		if filename != "PackageScopeRepository.pkl" {
			return evaluate.Contract{}, fmt.Errorf("local repository contract used for %s", filename)
		}
		return evaluate.LocalContract(uri, localRepositoryContract)
	default:
		return evaluate.ReleasedContract(uri)
	}
}

func observePackages(root string, discovered world.World) ([]workspace.Package, error) {
	result := make([]workspace.Package, 0)
	for _, resource := range discovered.Resources {
		resourceRoot := filepath.Join(root, filepath.FromSlash(resource.CanonicalPath))
		names := make([]string, 0, len(resource.Declaration.Packages))
		for name := range resource.Declaration.Packages {
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
				Policy:    resource.Declaration.Packages[name],
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

func projectSkills(root string, discovered world.World) ([]string, error) {
	sources := make([]skills.Source, 0, len(discovered.Resources))
	for _, resource := range discovered.Resources {
		sources = append(sources, skills.Source{Root: filepath.Join(root, filepath.FromSlash(resource.CanonicalPath))})
	}
	inventory, err := skills.Discover(sources)
	if err != nil {
		return nil, fmt.Errorf("discover assembled skills: %w", err)
	}
	workbenchSelection := contract.SkillSelection{}
	changed := make([]string, 0)
	for _, consumer := range discovered.Resources {
		editing := contract.SkillSelection{}
		for _, include := range consumer.Declaration.Includes {
			if include.Skills == nil {
				continue
			}
			if include.Skills.Editing != nil {
				editing = mergeSelection(editing, *include.Skills.Editing)
			}
			if include.Skills.Workbench != nil {
				workbenchSelection = mergeSelection(workbenchSelection, *include.Skills.Workbench)
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
