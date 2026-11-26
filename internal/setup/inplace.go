package setup

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/phosphorco/workbench-go/internal/contract"
	"github.com/phosphorco/workbench-go/internal/evaluate"
	workbenchversion "github.com/phosphorco/workbench-go/internal/version"
	"github.com/phosphorco/workbench-go/internal/workspace"
)

const (
	localV0140InPlaceSubjectURI = "workbench-contract:/0.14.0/InPlaceSubject.pkl"
	inPlaceSubjectFile          = "InPlaceSubject.pkl"
)

type inPlaceEvaluator interface {
	EvaluateInPlaceSubject(context.Context, []byte, evaluate.Contract) (contract.InPlaceSubject, error)
	EvaluateRepositoryDeclaration(context.Context, []byte, evaluate.Contract) (contract.Declaration, error)
	EvaluatePackageScopeDeclarationV030(context.Context, []byte, evaluate.Contract) (contract.Declaration, error)
}

// IsInPlace reports whether root's workbench-subject.pkl amends the
// InPlaceSubject contract.
func IsInPlace(root string) (bool, error) {
	source, err := os.ReadFile(filepath.Join(root, "workbench-subject.pkl"))
	if err != nil {
		return false, fmt.Errorf("read workbench-subject.pkl: %w", err)
	}
	filename, err := amendedFilename(source)
	if err != nil {
		return false, err
	}
	return filename == inPlaceSubjectFile, nil
}

func inPlaceSubjectSchema(source []byte) (evaluate.Contract, error) {
	match := amendsPattern.FindSubmatch(source)
	if match == nil {
		return evaluate.Contract{}, fmt.Errorf("Pkl source must begin with amends")
	}
	uri := string(match[1])
	if uri == localV0140InPlaceSubjectURI {
		return evaluate.LocalContract(uri, localInPlaceSubjectContract)
	}
	released := "package://github.com/phosphorco/workbench-go/releases/download/" + workbenchversion.ReleaseCoordinate + "/workbench@0.14.0#/" + inPlaceSubjectFile
	if uri == released && workbenchversion.CurrentContractVersion == "0.14.0" {
		return evaluate.ReleasedContract(uri)
	}
	return evaluate.Contract{}, fmt.Errorf("InPlaceSubject %q is not a 0.14.0 contract; an in-place Workbench requires contract 0.14.0", uri)
}

// CheckReport is the read-only comparison of every owned file.
type CheckReport struct {
	Repository string                 `json:"repository"`
	Owned      int                    `json:"ownedFiles"`
	Differing  []FileDifference       `json:"differing"`
	Missing    []string               `json:"missing"`
	Extra      []string               `json:"extra"`
	Notes      []workspace.Note       `json:"notes"`
	Graph      workspace.GraphSummary `json:"graph"`
}

// FileDifference is one owned file whose bytes differ from the projection.
type FileDifference struct {
	Path string `json:"path"`
	Diff string `json:"diff"`
}

// Equal reports whether every owned file equals the projection.
func (report CheckReport) Equal() bool {
	return len(report.Differing) == 0 && len(report.Missing) == 0 && len(report.Extra) == 0
}

// inPlaceState is a computed in-place projection and its owned-file set.
type inPlaceState struct {
	root       string
	github     string
	subject    contract.InPlaceSubject
	projection workspace.LayoutProjection
	owned      []string
}

// CheckInPlace computes the in-place projection and compares it with the
// working tree without writing anything or running any reconciler.
func CheckInPlace(ctx context.Context, root string, toolchain Toolchain) (CheckReport, error) {
	state, err := computeInPlace(ctx, root, toolchain)
	if err != nil {
		return CheckReport{}, err
	}
	return state.compare()
}

// runInPlace writes the owned files that differ, deletes extra owned files and
// installs only when the Subject declares it, with a frozen lockfile.
func runInPlace(ctx context.Context, root string, toolchain Toolchain) (Result, error) {
	state, err := computeInPlace(ctx, root, toolchain)
	if err != nil {
		return Result{}, err
	}
	report, err := state.compare()
	if err != nil {
		return Result{}, err
	}
	changed := []string{}
	for _, difference := range report.Differing {
		changed = append(changed, difference.Path)
	}
	changed = append(changed, report.Missing...)
	for _, relative := range changed {
		target := filepath.Join(root, filepath.FromSlash(relative))
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return Result{}, fmt.Errorf("create parent for %q: %w", relative, err)
		}
		if err := os.WriteFile(target, state.projection.Files[relative], 0o644); err != nil {
			return Result{}, fmt.Errorf("write owned file %q: %w", relative, err)
		}
	}
	for _, relative := range report.Extra {
		if err := os.Remove(filepath.Join(root, filepath.FromSlash(relative))); err != nil {
			return Result{}, fmt.Errorf("delete extra owned file %q: %w", relative, err)
		}
		changed = append(changed, relative)
	}
	if state.subject.Install {
		command := exec.CommandContext(ctx, toolchain.Bun, "install", "--frozen-lockfile", "--ignore-scripts")
		command.Dir = root
		command.Env = os.Environ()
		if output, err := command.CombinedOutput(); err != nil {
			return Result{}, fmt.Errorf("install with a frozen lockfile: %w: %s", err, strings.TrimSpace(string(output)))
		}
	}
	sort.Strings(changed)
	return Result{
		Resources:       []Resource{{Identity: state.github, GitHub: state.github, CanonicalPath: ".", Shape: contract.ResourceShape{Kind: contract.RepositoryShape}}},
		ChangedPaths:    changed,
		ContractVersion: "0.14.0",
	}, nil
}

func computeInPlace(ctx context.Context, root string, toolchain Toolchain) (*inPlaceState, error) {
	if toolchain.Evaluator == nil {
		return nil, fmt.Errorf("setup evaluator is absent")
	}
	if toolchain.Bun == "" {
		return nil, fmt.Errorf("setup Bun executable is absent")
	}
	evaluator, ok := toolchain.Evaluator.(inPlaceEvaluator)
	if !ok {
		return nil, fmt.Errorf("an in-place Workbench requires an explicitly configured Pkl evaluator")
	}
	root, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("resolve workbench root: %w", err)
	}
	subjectSource, err := os.ReadFile(filepath.Join(root, "workbench-subject.pkl"))
	if err != nil {
		return nil, fmt.Errorf("read workbench-subject.pkl: %w", err)
	}
	schema, err := inPlaceSubjectSchema(subjectSource)
	if err != nil {
		return nil, err
	}
	subject, err := evaluator.EvaluateInPlaceSubject(ctx, subjectSource, schema)
	if err != nil {
		return nil, err
	}
	github, err := contract.GitHubIdentity(subject.Entrypoint)
	if err != nil {
		return nil, err
	}
	if err := requireInPlaceCheckout(root, github); err != nil {
		return nil, err
	}
	declaration, err := loadInPlaceDeclaration(ctx, root, github, toolchain)
	if err != nil {
		return nil, err
	}
	files, err := repositoryFiles(ctx, root)
	if err != nil {
		return nil, err
	}
	tracked, err := trackedFiles(ctx, root)
	if err != nil {
		return nil, err
	}
	tree := newLayoutTree(root, files)
	input, notes, err := layoutInput(ctx, toolchain.Bun, tree, root, declaration)
	if err != nil {
		return nil, err
	}
	projection, err := workspace.BuildLayout(input)
	if err != nil {
		return nil, fmt.Errorf("build workspace projection: %w", err)
	}
	projection.Notes = append(notes, projection.Notes...)
	return &inPlaceState{
		root: root, github: github, subject: subject, projection: projection,
		owned: ownedFiles(tracked, input, projection),
	}, nil
}

// requireInPlaceCheckout proves root is the top of a checkout whose origin
// names the entrypoint. Git is only read.
func requireInPlaceCheckout(root, github string) error {
	top, err := runSetupGit(root, "rev-parse", "--show-toplevel")
	if err != nil {
		return fmt.Errorf("an in-place Workbench must be the top of a Git checkout: %w", err)
	}
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return err
	}
	realTop, err := filepath.EvalSymlinks(strings.TrimSpace(top))
	if err != nil {
		return err
	}
	if filepath.Clean(realRoot) != filepath.Clean(realTop) {
		return fmt.Errorf("an in-place Workbench must be the top of its checkout %s, not %s", realTop, realRoot)
	}
	origin, err := runSetupGit(root, "remote", "get-url", "origin")
	if err != nil {
		return fmt.Errorf("read the checkout's origin: %w", err)
	}
	identity, err := contract.GitHubRemoteIdentity(origin)
	if err != nil {
		return fmt.Errorf("origin %q: %w", strings.TrimSpace(origin), err)
	}
	if identity != github {
		return fmt.Errorf("origin names %s but the InPlaceSubject entrypoint names %s; remedy: run setup in a checkout of the entrypoint or correct the entrypoint", identity, github)
	}
	return nil
}

func loadInPlaceDeclaration(ctx context.Context, root, github string, toolchain Toolchain) (contract.Declaration, error) {
	encoded, err := readRealFileWithin(root, "workbench.pkl")
	if err != nil {
		return contract.Declaration{}, fmt.Errorf("read workbench.pkl: %w", err)
	}
	filename, err := amendedFilename(encoded)
	if err != nil {
		return contract.Declaration{}, err
	}
	if filename != "Repository.pkl" {
		return contract.Declaration{}, fmt.Errorf("an in-place workbench.pkl must amend the Repository.pkl contract, not %q", filename)
	}
	schema, version, err := schemaForSource(encoded, filename)
	if err != nil {
		return contract.Declaration{}, err
	}
	if !supportsRoles(version) {
		return contract.Declaration{}, fmt.Errorf("an in-place Workbench requires a 0.14.0 Repository declaration, got %s", version)
	}
	evaluator, ok := toolchain.Evaluator.(versionedEvaluator)
	if !ok {
		return contract.Declaration{}, fmt.Errorf("an in-place Workbench requires an explicitly configured Pkl evaluator")
	}
	declaration, err := evaluator.EvaluateRepositoryDeclaration(ctx, encoded, schema)
	if err != nil {
		return contract.Declaration{}, fmt.Errorf("evaluate workbench.pkl: %w", err)
	}
	source := &discoverySource{ctx: ctx, evaluatorVersioned: evaluator, version: version}
	if err := source.mergeInRepositoryScopes(github, root, &declaration); err != nil {
		return contract.Declaration{}, err
	}
	for designation := range declaration.Includes {
		normalized, err := contract.NormalizeGitHubRepository(designation)
		if err != nil || normalized != github {
			return contract.Declaration{}, fmt.Errorf("an in-place Workbench refuses cross-repository includes; %q includes %q", github, designation)
		}
	}
	if len(declaration.Buildables) != 0 {
		return contract.Declaration{}, fmt.Errorf("an in-place Workbench projects no buildables; declare them in a context Workbench")
	}
	if declaration.Root == nil {
		return contract.Declaration{}, fmt.Errorf("an in-place Workbench owns the root package.json, so workbench.pkl must declare root { packageJson { fields { … } } }")
	}
	return declaration, nil
}

// layoutInput places every package, observes roles and projects, and gathers
// what the 0.14 projection reads.
func layoutInput(ctx context.Context, bun string, tree *layoutTree, root string, declaration contract.Declaration) (workspace.LayoutInput, []workspace.Note, error) {
	bases := declaration.TypeScript.Bases
	for _, name := range sortedKeysOf(bases) {
		if directory := bases[name].Directory; directory != "." {
			if _, err := realPathWithin(root, directory, directoryKind); err != nil {
				return workspace.LayoutInput{}, nil, fmt.Errorf("base %q directory %q: %w", name, directory, err)
			}
		}
	}
	packages := make([]workspace.LayoutPackage, 0, len(declaration.Packages))
	placed := map[string]string{}
	for _, name := range sortedKeysOf(declaration.Packages) {
		policy := declaration.Packages[name]
		if policy.Directory == "" {
			return workspace.LayoutInput{}, nil, fmt.Errorf("package %q has no directory; an in-place Repository places every package with directory or an in-repository scope", name)
		}
		if _, err := realPathWithin(root, policy.Directory, directoryKind); err != nil {
			return workspace.LayoutInput{}, nil, fmt.Errorf("locate package %q: %w", name, err)
		}
		if previous, exists := placed[policy.Directory]; exists {
			return workspace.LayoutInput{}, nil, fmt.Errorf("packages %q and %q both resolve to directory %q", previous, name, policy.Directory)
		}
		placed[policy.Directory] = name
		pkg := workspace.LayoutPackage{Name: name, Directory: policy.Directory, Policy: policy}
		roles := policy.EffectiveRoles()
		for _, roleName := range contract.SortedRoleNames(roles) {
			pkg.Roles = append(pkg.Roles, workspace.LayoutRole{Name: roleName, Role: roles[roleName], Config: path.Join(policy.Directory, "tsconfig."+roleName+".json")})
		}
		packages = append(packages, pkg)
	}
	for directory, name := range placed {
		for other, otherName := range placed {
			if other != directory && contract.PathWithin(other, directory) {
				return workspace.LayoutInput{}, nil, fmt.Errorf("package %q lies at %q, inside package %q at %q", otherName, other, name, directory)
			}
		}
	}
	projects := make([]workspace.LayoutRole, 0, len(declaration.TypeScript.Projects))
	for _, config := range sortedKeysOf(declaration.TypeScript.Projects) {
		if directory := path.Dir(config); directory != "." {
			if _, err := realPathWithin(root, directory, directoryKind); err != nil {
				return workspace.LayoutInput{}, nil, fmt.Errorf("project %q directory: %w", config, err)
			}
		}
		if _, owner := placed[path.Dir(config)]; owner && regexpRoleFile(path.Base(config)) {
			return workspace.LayoutInput{}, nil, fmt.Errorf("project %q is a role file of the package in %s; declare it as a role", config, path.Dir(config))
		}
		projects = append(projects, workspace.LayoutRole{Name: config, Role: declaration.TypeScript.Projects[config], Config: config})
	}
	notes, err := observeLayoutRoles(ctx, bun, tree, bases, packages, projects)
	if err != nil {
		return workspace.LayoutInput{}, nil, err
	}
	var circular []string
	circularSource := ""
	if declaration.CircularEdges != nil {
		circularSource = declaration.CircularEdges.Path
		circular, err = readCircularEdges(root, circularSource)
		if err != nil {
			return workspace.LayoutInput{}, nil, err
		}
	}
	manifests := []string{}
	for _, file := range tree.files {
		if path.Base(file) == "package.json" && path.Dir(file) != "." {
			manifests = append(manifests, path.Dir(file))
		}
	}
	return workspace.LayoutInput{
		Packages: packages, Projects: projects, Bases: bases,
		CircularEdges: circular, CircularSource: circularSource,
		Root: declaration.Root, GeneratedMarker: declaration.GeneratedMarker,
		RuntimeBuiltins:     workspace.RuntimeBuiltinModulesV0130(),
		Exists:              func(relative string) bool { _, err := realPathWithin(root, relative, regularFile); return err == nil },
		ManifestDirectories: manifests,
	}, notes, nil
}

func regexpRoleFile(name string) bool {
	return name == "tsconfig.json" || (strings.HasPrefix(name, "tsconfig.") && strings.HasSuffix(name, ".json") && name != "tsconfig.base.json")
}

// ownedFiles is the owned-file rule applied to tracked files: package.json,
// tsconfig.json and tsconfig.*.json in placed package directories; base
// files; .tsconfigs/** under index and package directories; standalone
// project files; the root package.json and tsconfig.json.
func ownedFiles(tracked []string, input workspace.LayoutInput, projection workspace.LayoutProjection) []string {
	packageDirectories := map[string]bool{}
	for _, pkg := range input.Packages {
		packageDirectories[pkg.Directory] = true
	}
	indexRoots := []string{}
	for _, directory := range projection.IndexDirectories {
		indexRoots = append(indexRoots, joinRelative(cleanRelative(directory), ".tsconfigs/"))
	}
	for directory := range packageDirectories {
		indexRoots = append(indexRoots, directory+"/.tsconfigs/")
	}
	exact := map[string]bool{"package.json": true, "tsconfig.json": true}
	for _, base := range input.Bases {
		exact[workspace.BaseFile(base.Directory)] = true
	}
	for _, project := range input.Projects {
		exact[project.Config] = true
	}
	owned := []string{}
	for _, file := range tracked {
		directory, name := path.Dir(file), path.Base(file)
		switch {
		case exact[file]:
		case packageDirectories[directory] && (name == "package.json" || regexpRoleFile(name)):
		case hasAnyPrefix(file, indexRoots):
		default:
			continue
		}
		owned = append(owned, file)
	}
	for file := range projection.Files {
		owned = append(owned, file)
	}
	sort.Strings(owned)
	return dedupeSorted(owned)
}

func hasAnyPrefix(value string, prefixes []string) bool {
	for _, prefix := range prefixes {
		if strings.HasPrefix(value, prefix) {
			return true
		}
	}
	return false
}

func dedupeSorted(values []string) []string {
	result := values[:0]
	for index, value := range values {
		if index > 0 && values[index-1] == value {
			continue
		}
		result = append(result, value)
	}
	return result
}

func (state *inPlaceState) compare() (CheckReport, error) {
	report := CheckReport{Repository: state.github, Owned: len(state.owned), Differing: []FileDifference{}, Missing: []string{}, Extra: []string{}, Notes: state.projection.Notes, Graph: state.projection.Graph}
	if report.Notes == nil {
		report.Notes = []workspace.Note{}
	}
	for _, relative := range state.owned {
		want, projected := state.projection.Files[relative]
		current, err := os.ReadFile(filepath.Join(state.root, filepath.FromSlash(relative)))
		missing := errors.Is(err, os.ErrNotExist)
		if err != nil && !missing {
			return CheckReport{}, fmt.Errorf("read owned file %q: %w", relative, err)
		}
		switch {
		case !projected && !missing:
			report.Extra = append(report.Extra, relative)
		case projected && missing:
			report.Missing = append(report.Missing, relative)
		case projected && !bytes.Equal(current, want):
			report.Differing = append(report.Differing, FileDifference{Path: relative, Diff: unifiedDiff(relative, current, want)})
		}
	}
	return report, nil
}

// Human renders the report, showing at most limit diffs and saying how many
// it omitted. JSON output is never capped.
func (report CheckReport) Human(limit int) string {
	var output strings.Builder
	if report.Equal() {
		fmt.Fprintf(&output, "Workbench check: all %d owned files equal the projection.", report.Owned)
	} else {
		fmt.Fprintf(&output, "Workbench check: %d differing, %d missing, %d extra of %d owned files.", len(report.Differing), len(report.Missing), len(report.Extra), report.Owned)
	}
	for index, difference := range report.Differing {
		if index >= limit {
			fmt.Fprintf(&output, "\n… %d more differing files omitted (use --json for every diff)", len(report.Differing)-limit)
			break
		}
		output.WriteString("\n")
		output.WriteString(strings.TrimSuffix(difference.Diff, "\n"))
	}
	for _, missing := range report.Missing {
		fmt.Fprintf(&output, "\nmissing owned file: %s", missing)
	}
	for _, extra := range report.Extra {
		fmt.Fprintf(&output, "\nextra owned file: %s", extra)
	}
	if len(report.Notes) != 0 {
		counts := map[string]int{}
		for _, note := range report.Notes {
			counts[note.Kind]++
		}
		parts := []string{}
		for _, kind := range sortedKeysOf(counts) {
			parts = append(parts, fmt.Sprintf("%s %d", kind, counts[kind]))
		}
		fmt.Fprintf(&output, "\nInformational: %s (details in --json).", strings.Join(parts, ", "))
	}
	return output.String()
}

func sortedKeysOf[Value any](values map[string]Value) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

// refuseInPlaceLayout refuses 0.14 layout fields in a context Workbench. A
// context Workbench projects a 0.14 declaration with the 0.13 workspace
// projection, so these fields would otherwise be silently ignored.
func refuseInPlaceLayout(github string, declaration contract.Declaration) error {
	fields := []string{}
	if !declaration.TypeScript.Empty() {
		fields = append(fields, "typescript")
	}
	if declaration.CircularEdges != nil {
		fields = append(fields, "circularEdges")
	}
	if declaration.Root != nil {
		fields = append(fields, "root")
	}
	if declaration.GeneratedMarker != nil {
		fields = append(fields, "generatedMarker")
	}
	for _, name := range sortedKeysOf(declaration.Packages) {
		policy := declaration.Packages[name]
		if len(policy.Roles) != 0 {
			fields = append(fields, "packages["+name+"].roles")
		}
		if len(policy.Fields.Members) != 0 {
			fields = append(fields, "packages["+name+"].fields")
		}
		if policy.Private != nil && !*policy.Private {
			fields = append(fields, "packages["+name+"].private")
		}
	}
	if len(fields) != 0 {
		return fmt.Errorf("repository %q declares %s, which only an in-place Workbench projects; remedy: declare an InPlaceSubject in the repository, or remove them", github, strings.Join(fields, ", "))
	}
	return nil
}
