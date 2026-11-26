package workspace

import (
	"fmt"
	"path"
	"sort"
	"strings"

	"github.com/phosphorco/workbench-go/internal/contract"
)

// Contract 0.14 layout projection: per-package role tsconfigs and aggregators,
// bases, derived child indexes, standalone projects, the root solution and an
// authored root package.json. Every path is slash-separated and relative to
// the repository root.

// LayoutRole is one observed role of a package or one standalone project.
type LayoutRole struct {
	Name string
	Role contract.Role
	// Config is the role's tsconfig path.
	Config string
	// Files are the source files the role compiles (selected the way
	// TypeScript selects them), including declared foreign files.
	Files []string
	// Imports are observed in Files. Source names the file.
	Imports []Import
}

// LayoutPackage is one placed package and its observed roles.
type LayoutPackage struct {
	Name      string
	Directory string
	Policy    contract.PackagePolicy
	Roles     []LayoutRole
}

// LayoutInput is everything the 0.14 projection reads.
type LayoutInput struct {
	Packages        []LayoutPackage
	Projects        []LayoutRole
	Bases           map[string]contract.TsconfigBase
	CircularEdges   []string
	CircularSource  string
	Root            *contract.RootManifest
	GeneratedMarker *string
	RuntimeBuiltins map[string]bool
	// Exists reports whether a repository-relative path exists (for
	// patchedDependencies).
	Exists func(string) bool
	// ManifestDirectories are directories holding a package.json in the
	// working tree, for workspaces glob admission.
	ManifestDirectories []string
}

// Note is an informational finding that does not refuse the projection.
type Note struct {
	Kind   string `json:"kind"`
	Path   string `json:"path,omitempty"`
	Detail string `json:"detail"`
}

// GraphSummary describes the derived sources graph.
type GraphSummary struct {
	SourcesUnits      int   `json:"sourcesUnits"`
	SourcesCycleSizes []int `json:"sourcesCycleSizes"`
	CircularEdges     int   `json:"circularEdges"`
}

// LayoutProjection is the rendered owned files plus informational notes.
type LayoutProjection struct {
	Files map[string][]byte
	Notes []Note
	Graph GraphSummary
	// IndexDirectories and BaseFiles let the caller compute the owned-file set.
	IndexDirectories []string
}

type roleKey struct{ pkg, role string }

type layoutBuild struct {
	input       LayoutInput
	byName      map[string]*LayoutPackage
	roleByKey   map[roleKey]*LayoutRole
	fileOwner   map[string]roleKey
	baseDirs    map[string]string
	emits       map[roleKey]bool
	notes       []Note
	circular    map[string]bool
	references  map[roleKey][]layoutReference
	projectRefs map[string][]layoutReference
	summary     GraphSummary
}

type layoutReference struct {
	target   roleKey
	path     string
	circular bool
	// derived is true for references derived from imports; only these may
	// carry a circular mark.
	derived bool
}

// BuildLayout renders the 0.14 projection.
func BuildLayout(input LayoutInput) (LayoutProjection, error) {
	build := &layoutBuild{
		input:       input,
		byName:      map[string]*LayoutPackage{},
		roleByKey:   map[roleKey]*LayoutRole{},
		fileOwner:   map[string]roleKey{},
		baseDirs:    map[string]string{},
		emits:       map[roleKey]bool{},
		circular:    map[string]bool{},
		references:  map[roleKey][]layoutReference{},
		projectRefs: map[string][]layoutReference{},
	}
	sort.Slice(input.Packages, func(left, right int) bool { return input.Packages[left].Directory < input.Packages[right].Directory })
	for index := range input.Packages {
		pkg := &input.Packages[index]
		if err := validateRelativeDirectory(pkg.Directory); err != nil {
			return LayoutProjection{}, fmt.Errorf("package %q: %w", pkg.Name, err)
		}
		if previous, exists := build.byName[pkg.Name]; exists {
			return LayoutProjection{}, fmt.Errorf("package name %q is claimed by %q and %q", pkg.Name, previous.Directory, pkg.Directory)
		}
		build.byName[pkg.Name] = pkg
		sort.Slice(pkg.Roles, func(left, right int) bool { return pkg.Roles[left].Name < pkg.Roles[right].Name })
		for roleIndex := range pkg.Roles {
			build.roleByKey[roleKey{pkg.Name, pkg.Roles[roleIndex].Name}] = &pkg.Roles[roleIndex]
		}
	}
	for name, base := range input.Bases {
		build.baseDirs[base.Directory] = name
		for _, pkg := range input.Packages {
			if pkg.Directory == base.Directory {
				return LayoutProjection{}, fmt.Errorf("base %q is written to package directory %q; a base directory cannot be a package directory", name, base.Directory)
			}
		}
	}
	for _, edge := range input.CircularEdges {
		build.circular[edge] = true
	}
	steps := []func() error{
		build.computeEmits,
		build.validatePolicies,
		build.assignFiles,
		build.closure,
		build.deriveReferences,
		build.validateGraph,
	}
	for _, step := range steps {
		if err := step(); err != nil {
			return LayoutProjection{}, err
		}
	}
	return build.render()
}

func (build *layoutBuild) validatePolicies() error {
	for _, pkg := range build.input.Packages {
		declaredBy := map[string]string{}
		for _, class := range dependencyClasses(pkg.Policy) {
			for _, name := range sortedMapKeys(class.values) {
				version := class.values[name]
				if previous, exists := declaredBy[name]; exists {
					peerAndDev := (previous == "devDependencies" && class.name == "peerDependencies") || (previous == "peerDependencies" && class.name == "devDependencies")
					if !peerAndDev {
						return fmt.Errorf("package %q dependency %q is declared in both %s and %s; only peerDependencies with devDependencies may name one dependency twice", pkg.Name, name, previous, class.name)
					}
				} else {
					declaredBy[name] = class.name
				}
				if pkg.Policy.Verbatim() {
					if err := validateVerbatimSpecifier(Package{Name: pkg.Name}, class.name, name, version); err != nil {
						return err
					}
				} else if strings.HasPrefix(version, "catalog:") && build.input.Root == nil {
					return fmt.Errorf("package %q dependency %q in %s declares %q, but no authored root declares a catalog; declare an exact version or author the root", pkg.Name, name, class.name, version)
				}
				if _, participating := build.byName[name]; participating && version != "workspace:*" {
					return fmt.Errorf("package %q dependency %q participates in the assembled closure but %s declares version %q; participating dependencies must use workspace:*", pkg.Name, name, class.name, version)
				}
			}
		}
		for _, role := range pkg.Roles {
			for _, consumed := range role.Role.Consumes {
				if _, exists := build.roleByKey[roleKey{pkg.Name, consumed}]; !exists {
					return fmt.Errorf("package %q role %q consumes undeclared role %q", pkg.Name, role.Name, consumed)
				}
			}
			for _, reference := range role.Role.References {
				target, err := build.authoredReference(role.Role, reference)
				if err != nil {
					return fmt.Errorf("package %q role %q: %w", pkg.Name, role.Name, err)
				}
				if target.pkg == pkg.Name {
					return fmt.Errorf("package %q role %q references its own package; use consumes", pkg.Name, role.Name)
				}
			}
			if role.Role.Base != nil {
				if _, exists := build.input.Bases[*role.Role.Base]; !exists {
					return fmt.Errorf("package %q role %q names undeclared base %q", pkg.Name, role.Name, *role.Role.Base)
				}
			}
		}
	}
	return nil
}

// assignFiles records each package file's owning role and refuses undeclared
// overlaps and undeclared files outside the package.
func (build *layoutBuild) assignFiles() error {
	for _, pkg := range build.input.Packages {
		holders := map[string][]string{}
		for _, role := range pkg.Roles {
			for _, file := range role.Files {
				if _, foreign := role.Role.ForeignFiles[relativeFrom(pkg.Directory, file)]; foreign {
					continue
				}
				if !contract.PathWithin(file, pkg.Directory) {
					return fmt.Errorf("package %q role %q compiles %q outside the package; declare it in foreignFiles", pkg.Name, role.Name, file)
				}
				holders[file] = append(holders[file], role.Name)
			}
		}
		for _, file := range sortedKeysOf(holders) {
			roles := holders[file]
			owner, err := build.owningRole(pkg, file, roles)
			if err != nil {
				return err
			}
			build.fileOwner[file] = roleKey{pkg.Name, owner}
		}
	}
	return nil
}

func (build *layoutBuild) owningRole(pkg LayoutPackage, file string, roles []string) (string, error) {
	if len(roles) == 1 {
		return roles[0], nil
	}
	alsoIncludes := func(role, other string) bool {
		for _, name := range build.roleByKey[roleKey{pkg.Name, role}].Role.AlsoIncludes {
			if name == other {
				return true
			}
		}
		return false
	}
	owners := make([]string, 0, 1)
	for _, candidate := range roles {
		owner := true
		for _, other := range roles {
			if other == candidate {
				continue
			}
			if !alsoIncludes(other, candidate) || alsoIncludes(candidate, other) {
				owner = false
			}
		}
		if owner {
			owners = append(owners, candidate)
		}
	}
	if len(owners) != 1 {
		return "", fmt.Errorf("package %q roles %q all compile %q; declare the overlap with alsoIncludes on every role but the one that owns the file", pkg.Name, roles, file)
	}
	return owners[0], nil
}

// computeEmits resolves noEmit through the role's base chain.
func (build *layoutBuild) computeEmits() error {
	for _, pkg := range build.input.Packages {
		for _, role := range pkg.Roles {
			noEmit, err := build.noEmit(role.Role, pkg.Directory)
			if err != nil {
				return fmt.Errorf("package %q role %q: %w", pkg.Name, role.Name, err)
			}
			build.emits[roleKey{pkg.Name, role.Name}] = !noEmit
		}
	}
	return nil
}

// RoleBase returns the base a role extends from directory, or "" for none.
func RoleBase(role contract.Role, directory string, bases map[string]contract.TsconfigBase) string {
	if !role.Extends() {
		return ""
	}
	if role.Base != nil {
		return *role.Base
	}
	best, bestLength := "", -1
	for name, base := range bases {
		if base.Directory == "." || contract.PathWithin(directory, base.Directory) {
			length := len(base.Directory)
			if base.Directory == "." {
				length = 0
			}
			if length > bestLength || (length == bestLength && name < best) {
				best, bestLength = name, length
			}
		}
	}
	return best
}

// BaseChain returns the base and the bases it extends, nearest first.
func BaseChain(name string, bases map[string]contract.TsconfigBase) []string {
	chain := []string{}
	for name != "" {
		chain = append(chain, name)
		next := bases[name].Extends
		if next == nil {
			break
		}
		name = *next
	}
	return chain
}

func (build *layoutBuild) noEmit(role contract.Role, directory string) (bool, error) {
	if value, exists := role.CompilerOptions.Get("noEmit"); exists {
		return value.IsTrue(), nil
	}
	for _, base := range BaseChain(RoleBase(role, directory, build.input.Bases), build.input.Bases) {
		if value, exists := build.input.Bases[base].CompilerOptions.Get("noEmit"); exists {
			return value.IsTrue(), nil
		}
	}
	return false, nil
}

// closure reuses the released closure diagnostics over every role's imports.
func (build *layoutBuild) closure() error {
	packages := make([]Package, 0, len(build.input.Packages))
	byName := make(map[string]Package, len(build.input.Packages))
	for _, pkg := range build.input.Packages {
		flat := Package{Name: pkg.Name, Directory: pkg.Directory, Policy: pkg.Policy}
		for _, role := range pkg.Roles {
			flat.Imports = append(flat.Imports, role.Imports...)
		}
		packages = append(packages, flat)
		byName[pkg.Name] = flat
	}
	diagnostics := closureDiagnostics(packages, byName, build.input.RuntimeBuiltins)
	for _, project := range build.input.Projects {
		for _, observed := range project.Imports {
			if !isBarePackageSpecifier(observed.Specifier) || build.input.RuntimeBuiltins[observed.Specifier] {
				continue
			}
			name := importedPackageName(observed.Specifier)
			target, participating := byName[name]
			if !participating {
				continue
			}
			export := importedPackageExport(name, observed.Specifier)
			if !exportAvailable(target.Policy.Exports, export) {
				diagnostics = append(diagnostics, ClosureDiagnostic{
					Kind: MissingExport, Importer: project.Config, Specifier: observed.Specifier,
					Source: observed.Source, Line: observed.Line, MissingPackage: name,
					Remedy: fmt.Sprintf("declare exports[%q] on participating package %q", export, name),
				})
			}
		}
	}
	if len(diagnostics) != 0 {
		return &ClosureError{Diagnostics: diagnostics}
	}
	return nil
}

// resolveExportFile maps an import to the dependency file its exports name.
func resolveExportFile(target *LayoutPackage, subpath string) (string, bool) {
	if target.Policy.Exports == nil {
		return "", false
	}
	if value, exists := target.Policy.Exports[subpath]; exists && !strings.Contains(subpath, "*") {
		return path.Join(target.Directory, value), true
	}
	// Node's PACKAGE_EXPORTS_RESOLVE: keys with exactly one "*" whose
	// wildcard matches at least one character, the best by
	// PATTERN_KEY_COMPARE; every "*" of the target is substituted.
	best, bestKey := "", ""
	for _, key := range sortedMapKeys(target.Policy.Exports) {
		star := strings.Index(key, "*")
		if star < 0 || strings.Count(key, "*") != 1 {
			continue
		}
		prefix, suffix := key[:star], key[star+1:]
		if !strings.HasPrefix(subpath, prefix) || subpath == prefix || len(subpath) < len(key) || !strings.HasSuffix(subpath, suffix) {
			continue
		}
		if bestKey != "" && patternKeyCompare(bestKey, key) != 1 {
			continue
		}
		replacement := subpath[len(prefix) : len(subpath)-len(suffix)]
		best, bestKey = path.Join(target.Directory, strings.ReplaceAll(target.Policy.Exports[key], "*", replacement)), key
	}
	return best, bestKey != ""
}

// patternKeyCompare is Node's PATTERN_KEY_COMPARE: -1 when left is the more
// specific pattern key, 1 when right is.
func patternKeyCompare(left, right string) int {
	leftStar, rightStar := strings.Index(left, "*"), strings.Index(right, "*")
	leftBase, rightBase := len(left), len(right)
	if leftStar >= 0 {
		leftBase = leftStar + 1
	}
	if rightStar >= 0 {
		rightBase = rightStar + 1
	}
	switch {
	case leftBase > rightBase:
		return -1
	case rightBase > leftBase:
		return 1
	case leftStar < 0:
		return 1
	case rightStar < 0:
		return -1
	case len(left) > len(right):
		return -1
	case len(right) > len(left):
		return 1
	}
	return 0
}

func (build *layoutBuild) targetRole(importer string, role contract.Role, specifier string) (roleKey, bool, error) {
	name := importedPackageName(specifier)
	target := build.byName[name]
	fallback := roleKey{name, role.TargetRole()}
	file, resolved := resolveExportFile(target, importedPackageExport(name, specifier))
	if !resolved {
		build.notes = append(build.notes, Note{Kind: "referenceFallback", Path: importer, Detail: fmt.Sprintf("%q names no exports entry of %s; referencing its %s role", specifier, name, fallback.role)})
		return fallback, false, nil
	}
	owner, owned := build.fileOwner[file]
	if !owned || owner.pkg != name {
		build.notes = append(build.notes, Note{Kind: "referenceFallback", Path: importer, Detail: fmt.Sprintf("%q resolves to %s, which no role of %s compiles; referencing its %s role", specifier, file, name, fallback.role)})
		return fallback, false, nil
	}
	if !build.emits[owner] {
		build.notes = append(build.notes, Note{Kind: "referenceFallback", Path: importer, Detail: fmt.Sprintf("%q resolves to %s in the %s role of %s, which does not emit; referencing its %s role", specifier, file, owner.role, name, fallback.role)})
		return fallback, false, nil
	}
	return owner, true, nil
}

func (build *layoutBuild) deriveReferences() error {
	for _, pkg := range build.input.Packages {
		for _, role := range pkg.Roles {
			key := roleKey{pkg.Name, role.Name}
			// targets records whether each edge was derived from imports;
			// only derived edges may carry a circular mark.
			targets := map[roleKey]bool{}
			for _, consumed := range role.Role.Consumes {
				targets[roleKey{pkg.Name, consumed}] = targets[roleKey{pkg.Name, consumed}] || false
			}
			if role.Role.Derives(role.Name) {
				for _, observed := range role.Imports {
					name, ok := build.workspaceImport(observed.Specifier)
					if !ok || name == pkg.Name {
						continue
					}
					target, _, err := build.targetRole(role.Config, role.Role, observed.Specifier)
					if err != nil {
						return err
					}
					targets[target] = true
				}
			}
			for _, reference := range role.Role.References {
				target, err := build.authoredReference(role.Role, reference)
				if err != nil {
					return err
				}
				targets[target] = targets[target] || false
			}
			references, err := build.renderReferences(path.Dir(role.Config), key, targets)
			if err != nil {
				return fmt.Errorf("package %q role %q: %w", pkg.Name, role.Name, err)
			}
			build.references[key] = references
		}
	}
	for _, project := range build.input.Projects {
		targets := map[roleKey]bool{}
		if project.Role.Derives(project.Config) {
			for _, observed := range project.Imports {
				if _, ok := build.workspaceImport(observed.Specifier); !ok {
					continue
				}
				target, _, err := build.targetRole(project.Config, project.Role, observed.Specifier)
				if err != nil {
					return err
				}
				targets[target] = true
			}
		}
		for _, reference := range project.Role.References {
			target, err := build.authoredReference(project.Role, reference)
			if err != nil {
				return fmt.Errorf("project %q: %w", project.Config, err)
			}
			targets[target] = targets[target] || false
		}
		references, err := build.renderReferences(path.Dir(project.Config), roleKey{"", project.Config}, targets)
		if err != nil {
			return fmt.Errorf("project %q: %w", project.Config, err)
		}
		build.projectRefs[project.Config] = references
	}
	return nil
}

// authoredReference resolves "<package>" to the package's referenceRole and
// "<package>#<role>" to that role, which must exist and emit.
func (build *layoutBuild) authoredReference(role contract.Role, reference string) (roleKey, error) {
	name, roleName, named := strings.Cut(reference, "#")
	if _, exists := build.byName[name]; !exists {
		return roleKey{}, fmt.Errorf("references %q, which is not a placed package", name)
	}
	if !named {
		return roleKey{name, role.TargetRole()}, nil
	}
	target := roleKey{name, roleName}
	if _, exists := build.roleByKey[target]; !exists {
		return roleKey{}, fmt.Errorf("references %q, but %s declares no %s role", reference, name, roleName)
	}
	if !build.emits[target] {
		return roleKey{}, fmt.Errorf("references %q, but the %s role of %s does not emit", reference, roleName, name)
	}
	return target, nil
}

func (build *layoutBuild) workspaceImport(specifier string) (string, bool) {
	if !isBarePackageSpecifier(specifier) || build.input.RuntimeBuiltins[specifier] {
		return "", false
	}
	name := importedPackageName(specifier)
	_, exists := build.byName[name]
	return name, exists
}

func (build *layoutBuild) renderReferences(directory string, source roleKey, targets map[roleKey]bool) ([]layoutReference, error) {
	references := make([]layoutReference, 0, len(targets))
	for target := range targets {
		role, exists := build.roleByKey[target]
		if !exists {
			return nil, fmt.Errorf("references role %q of %q, which declares no such role", target.role, target.pkg)
		}
		if !build.emits[target] {
			if target.pkg == source.pkg {
				return nil, fmt.Errorf("consumes the %s role, which does not emit; TypeScript refuses a reference to a noEmit project (TS6310)", target.role)
			}
			return nil, fmt.Errorf("references the %s role of %s, which does not emit; a project can only reference an emitting role of another package", target.role, target.pkg)
		}
		reference := layoutReference{target: target, path: relativeReference(directory, role.Config), derived: targets[target]}
		if reference.derived && source.role == contract.DefaultRole && target.role == contract.DefaultRole && target.pkg != source.pkg {
			reference.circular = build.circular[source.pkg+" -> "+target.pkg]
		}
		references = append(references, reference)
	}
	sort.Slice(references, func(left, right int) bool { return references[left].path < references[right].path })
	return references, nil
}

// validateGraph checks the circular-edge set against the derived graph.
func (build *layoutBuild) validateGraph() error {
	nodes := make([]roleKey, 0, len(build.references))
	for key := range build.references {
		nodes = append(nodes, key)
	}
	sort.Slice(nodes, func(left, right int) bool {
		if nodes[left].pkg != nodes[right].pkg {
			return nodes[left].pkg < nodes[right].pkg
		}
		return nodes[left].role < nodes[right].role
	})
	index := make(map[roleKey]int, len(nodes))
	for position, node := range nodes {
		index[node] = position
	}
	all := make([][]int, len(nodes))
	unmarked := make([][]int, len(nodes))
	derived := map[string]bool{}
	for position, node := range nodes {
		for _, reference := range build.references[node] {
			target := index[reference.target]
			all[position] = append(all[position], target)
			if !reference.circular {
				unmarked[position] = append(unmarked[position], target)
			}
			if reference.derived && node.role == contract.DefaultRole && reference.target.role == contract.DefaultRole && node.pkg != reference.target.pkg {
				derived[node.pkg+" -> "+reference.target.pkg] = true
			}
		}
	}
	remedy := "regenerate the circular-edge set with the repository's own tooling"
	if build.input.CircularSource != "" {
		remedy = fmt.Sprintf("regenerate %s with the repository's own tooling (it derives the marks; Workbench never chooses them)", build.input.CircularSource)
	}
	component := stronglyConnected(all)
	for _, edge := range sortedKeysOf(build.circular) {
		if !derived[edge] {
			return fmt.Errorf("circular edge %q is not a derived sources reference; %s", edge, remedy)
		}
		source, target, _ := strings.Cut(edge, " -> ")
		left, right := index[roleKey{source, contract.DefaultRole}], index[roleKey{target, contract.DefaultRole}]
		if component[left] != component[right] {
			return fmt.Errorf("circular edge %q closes no cycle of the derived graph; %s", edge, remedy)
		}
	}
	unmarkedComponents := stronglyConnected(unmarked)
	if cycles := componentsWithCycles(unmarkedComponents, nodes); len(cycles) != 0 {
		return fmt.Errorf("the derived reference graph has %d cycle(s) no circular edge closes: %s; %s", len(cycles), strings.Join(cycles, "; "), remedy)
	}
	for position := range nodes {
		for _, target := range unmarked[position] {
			if target == position {
				return fmt.Errorf("role %s#%s references itself", nodes[position].pkg, nodes[position].role)
			}
		}
	}
	// Circular marks close cycles between packages; a cycle that passes
	// through two roles of one package cannot be closed by any mark.
	members := map[int][]roleKey{}
	for position, componentID := range component {
		members[componentID] = append(members[componentID], nodes[position])
	}
	inPackage := []string{}
	for _, cycle := range members {
		perPackage := map[string]int{}
		for _, member := range cycle {
			perPackage[member.pkg]++
		}
		for _, member := range cycle {
			if perPackage[member.pkg] > 1 {
				inPackage = append(inPackage, describeCycle(cycle))
				break
			}
		}
	}
	if len(inPackage) != 0 {
		sort.Strings(inPackage)
		return fmt.Errorf("the reference graph cycles through two roles of one package: %s; remedy: break the cycle in the package's imports or consumes (circular marks only close cycles between packages)", strings.Join(inPackage, "; "))
	}
	sourcesNodes := []int{}
	for position, node := range nodes {
		if node.role == contract.DefaultRole {
			sourcesNodes = append(sourcesNodes, position)
		}
	}
	sourceIndex := map[int]int{}
	for position, node := range sourcesNodes {
		sourceIndex[node] = position
	}
	sourceGraph := make([][]int, len(sourcesNodes))
	for position, node := range sourcesNodes {
		for _, target := range all[node] {
			if targetPosition, exists := sourceIndex[target]; exists {
				sourceGraph[position] = append(sourceGraph[position], targetPosition)
			}
		}
	}
	sourceComponents := stronglyConnected(sourceGraph)
	counts := map[int]int{}
	for _, componentID := range sourceComponents {
		counts[componentID]++
	}
	build.graph(counts, len(build.circular))
	return nil
}

func (build *layoutBuild) graph(counts map[int]int, marks int) {
	summary := GraphSummary{SourcesUnits: len(counts), SourcesCycleSizes: []int{}, CircularEdges: marks}
	for _, count := range counts {
		if count > 1 {
			summary.SourcesCycleSizes = append(summary.SourcesCycleSizes, count)
		}
	}
	sort.Sort(sort.Reverse(sort.IntSlice(summary.SourcesCycleSizes)))
	build.summary = summary
}

// componentsWithCycles lists every component with more than one member,
// members sorted, components sorted.
func componentsWithCycles(component []int, nodes []roleKey) []string {
	members := map[int][]roleKey{}
	for position, componentID := range component {
		members[componentID] = append(members[componentID], nodes[position])
	}
	cycles := []string{}
	for _, cycle := range members {
		if len(cycle) > 1 {
			cycles = append(cycles, describeCycle(cycle))
		}
	}
	sort.Strings(cycles)
	return cycles
}

func describeCycle(cycle []roleKey) string {
	names := make([]string, 0, len(cycle))
	for _, member := range cycle {
		names = append(names, member.pkg+"#"+member.role)
	}
	sort.Strings(names)
	return strings.Join(names, ", ")
}

// stronglyConnected returns a component id per node (Tarjan, iterative).
func stronglyConnected(edges [][]int) []int {
	count := len(edges)
	index := make([]int, count)
	low := make([]int, count)
	onStack := make([]bool, count)
	component := make([]int, count)
	for node := range index {
		index[node] = -1
	}
	stack := []int{}
	next, components := 0, 0
	type frame struct{ node, edge int }
	for root := 0; root < count; root++ {
		if index[root] >= 0 {
			continue
		}
		frames := []frame{{root, 0}}
		index[root], low[root] = next, next
		next++
		stack = append(stack, root)
		onStack[root] = true
		for len(frames) > 0 {
			top := &frames[len(frames)-1]
			if top.edge < len(edges[top.node]) {
				target := edges[top.node][top.edge]
				top.edge++
				if index[target] < 0 {
					index[target], low[target] = next, next
					next++
					stack = append(stack, target)
					onStack[target] = true
					frames = append(frames, frame{target, 0})
				} else if onStack[target] && index[target] < low[top.node] {
					low[top.node] = index[target]
				}
				continue
			}
			node := top.node
			frames = frames[:len(frames)-1]
			if len(frames) > 0 && low[node] < low[frames[len(frames)-1].node] {
				low[frames[len(frames)-1].node] = low[node]
			}
			if low[node] == index[node] {
				for {
					member := stack[len(stack)-1]
					stack = stack[:len(stack)-1]
					onStack[member] = false
					component[member] = components
					if member == node {
						break
					}
				}
				components++
			}
		}
	}
	return component
}

func relativeReference(fromDirectory, target string) string {
	relative := relativeFrom(fromDirectory, target)
	if !strings.HasPrefix(relative, "../") {
		relative = "./" + relative
	}
	return relative
}

// relativeFrom returns target relative to directory ("." or "" is the root).
func relativeFrom(directory, target string) string {
	if directory == "" {
		directory = "."
	}
	fromParts := strings.Split(path.Clean(directory), "/")
	if path.Clean(directory) == "." {
		fromParts = nil
	}
	toParts := strings.Split(path.Clean(target), "/")
	shared := 0
	for shared < len(fromParts) && shared < len(toParts) && fromParts[shared] == toParts[shared] {
		shared++
	}
	parts := make([]string, 0, len(fromParts)-shared+len(toParts)-shared)
	for range fromParts[shared:] {
		parts = append(parts, "..")
	}
	parts = append(parts, toParts[shared:]...)
	if len(parts) == 0 {
		return "."
	}
	return strings.Join(parts, "/")
}

func sortedKeysOf[Value any](values map[string]Value) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
