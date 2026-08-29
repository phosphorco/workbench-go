package workspace

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/phosphorco/workbench-go/internal/contract"
)

type Package struct {
	Name      string
	Directory string
	Imports   []Import
	Policy    contract.PackagePolicy
}

type Import struct {
	Specifier   string
	Source      string
	Line        int
	Development bool
}

type ClosureDiagnosticKind string

const (
	MissingPackage             ClosureDiagnosticKind = "missingPackage"
	MissingWorkspaceDependency ClosureDiagnosticKind = "missingWorkspaceDependency"
	MissingImport              ClosureDiagnosticKind = "missingImport"
	MissingExport              ClosureDiagnosticKind = "missingExport"
)

type ClosureDiagnostic struct {
	Kind            ClosureDiagnosticKind
	Importer        string
	Specifier       string
	Source          string
	Line            int
	MissingPackage  string
	DependencyClass string
	Remedy          string
}

type ClosureError struct {
	Diagnostics []ClosureDiagnostic
}

func (failure *ClosureError) Error() string {
	lines := make([]string, 0, len(failure.Diagnostics))
	for _, diagnostic := range failure.Diagnostics {
		location := diagnostic.Source
		if diagnostic.Line > 0 {
			location = fmt.Sprintf("%s:%d", location, diagnostic.Line)
		}
		if diagnostic.Kind == MissingWorkspaceDependency {
			lines = append(lines, fmt.Sprintf("%s: package %q declares %q in %s: %s. Remedy: %s", location, diagnostic.Importer, diagnostic.Specifier, diagnostic.DependencyClass, closureProblem(diagnostic), diagnostic.Remedy))
			continue
		}
		lines = append(lines, fmt.Sprintf("%s: package %q imports %q: %s. Remedy: %s", location, diagnostic.Importer, diagnostic.Specifier, closureProblem(diagnostic), diagnostic.Remedy))
	}
	return fmt.Sprintf("workspace closure contains %d gap(s):\n%s", len(lines), strings.Join(lines, "\n"))
}

func closureProblem(diagnostic ClosureDiagnostic) string {
	switch diagnostic.Kind {
	case MissingPackage, MissingWorkspaceDependency:
		return fmt.Sprintf("package %q is absent from the assembled repository closure", diagnostic.MissingPackage)
	case MissingImport:
		return "the importing package has no matching imports declaration"
	case MissingExport:
		return fmt.Sprintf("participating package %q has no matching exports declaration", diagnostic.MissingPackage)
	default:
		return "the import is not expressible by the assembled package contract"
	}
}

type Projection struct {
	Files map[string][]byte
}

type BuildOptions struct {
	ReassembleRootDependencies bool
	ProductionTypeScript       bool
}

type TypeScriptAuthority struct {
	Package string
	Version string
}

type TypeScriptAuthorityError struct {
	Reason      string
	Authorities []TypeScriptAuthority
}

type RootDependencyAuthority struct {
	Package string
	Class   string
	Name    string
	Version string
}

type RootDependencyAuthorityError struct {
	Dependency  string
	Reason      string
	Authorities []RootDependencyAuthority
}

func (failure *RootDependencyAuthorityError) Error() string {
	details := make([]string, 0, len(failure.Authorities))
	for _, authority := range failure.Authorities {
		details = append(details, fmt.Sprintf("%s %s declares %q", authority.Package, authority.Class, authority.Version))
	}
	return fmt.Sprintf("root external dependency %q has %s authority (%s); remedy: declare the same exact external version across participating package dependency classes", failure.Dependency, failure.Reason, strings.Join(details, ", "))
}

func (failure *TypeScriptAuthorityError) Error() string {
	if len(failure.Authorities) == 0 {
		return "root TypeScript tool has no version authority; remedy: declare one exact external typescript version in a participating package's devDependencies"
	}
	details := make([]string, 0, len(failure.Authorities))
	for _, authority := range failure.Authorities {
		details = append(details, fmt.Sprintf("%s declares %q", authority.Package, authority.Version))
	}
	return fmt.Sprintf("root TypeScript tool has %s authority (%s); remedy: declare the same exact external typescript version in participating package devDependencies", failure.Reason, strings.Join(details, ", "))
}

type packageJSON struct {
	Name                 string            `json:"name"`
	Private              bool              `json:"private"`
	Type                 string            `json:"type"`
	Exports              any               `json:"exports"`
	Imports              map[string]string `json:"imports,omitempty"`
	Dependencies         map[string]string `json:"dependencies,omitempty"`
	DevDependencies      map[string]string `json:"devDependencies,omitempty"`
	PeerDependencies     map[string]string `json:"peerDependencies,omitempty"`
	OptionalDependencies map[string]string `json:"optionalDependencies,omitempty"`
}

type packageExports struct {
	Root packageRootExport `json:"."`
}

type packageRootExport struct {
	Types   string `json:"types"`
	Default string `json:"default"`
}

type rootPackageJSON struct {
	Name            string            `json:"name"`
	Private         bool              `json:"private"`
	Workspaces      []string          `json:"workspaces"`
	Scripts         map[string]string `json:"scripts"`
	DevDependencies map[string]string `json:"devDependencies,omitempty"`
}

type tsReference struct {
	Path string `json:"path"`
}

type rootTSConfig struct {
	Files      []string      `json:"files"`
	References []tsReference `json:"references"`
}

type packageTSConfig struct {
	CompilerOptions map[string]any `json:"compilerOptions"`
	Include         []string       `json:"include"`
	References      []tsReference  `json:"references,omitempty"`
}

func Build(packages []Package) (Projection, error) {
	return BuildWithOptions(packages, BuildOptions{ReassembleRootDependencies: true, ProductionTypeScript: true})
}

func BuildWithOptions(packages []Package, options BuildOptions) (Projection, error) {
	ordered := append([]Package(nil), packages...)
	sort.Slice(ordered, func(left, right int) bool { return ordered[left].Directory < ordered[right].Directory })
	byName := make(map[string]Package, len(ordered))
	for _, pkg := range ordered {
		if err := validateRelativeDirectory(pkg.Directory); err != nil {
			return Projection{}, fmt.Errorf("package %q: %w", pkg.Name, err)
		}
		if pkg.Name == "" {
			return Projection{}, fmt.Errorf("package in %q has an empty name", pkg.Directory)
		}
		if previous, exists := byName[pkg.Name]; exists {
			return Projection{}, fmt.Errorf("package name %q is claimed by %q and %q", pkg.Name, previous.Directory, pkg.Directory)
		}
		byName[pkg.Name] = pkg
	}
	if err := validatePackagePolicies(ordered, byName); err != nil {
		return Projection{}, err
	}
	if diagnostics := closureDiagnostics(ordered, byName); len(diagnostics) != 0 {
		return Projection{}, &ClosureError{Diagnostics: diagnostics}
	}
	rootDevDependencies := map[string]string(nil)
	if options.ReassembleRootDependencies {
		var err error
		rootDevDependencies, err = rootExternalDependencies(ordered)
		if err != nil {
			return Projection{}, err
		}
	}

	files := make(map[string][]byte, 2+len(ordered)*2)
	workspaces := make([]string, 0, len(ordered))
	rootReferences := make([]tsReference, 0, len(ordered))
	for _, pkg := range ordered {
		workspaces = append(workspaces, filepath.ToSlash(pkg.Directory))
		rootReferences = append(rootReferences, tsReference{Path: "./" + filepath.ToSlash(pkg.Directory)})
		manifest, tsconfig, err := renderPackage(pkg, byName, options.ProductionTypeScript)
		if err != nil {
			return Projection{}, err
		}
		files[filepath.Join(pkg.Directory, "package.json")] = manifest
		files[filepath.Join(pkg.Directory, "tsconfig.json")] = tsconfig
	}
	var err error
	files["package.json"], err = encode(rootPackageJSON{
		Name: "workbench", Private: true, Workspaces: workspaces,
		Scripts: map[string]string{
			"test":      "bun test --path-ignore-patterns='**/dist/**'",
			"typecheck": "tsc --build tsconfig.json --pretty false",
		},
		DevDependencies: rootDevDependencies,
	})
	if err != nil {
		return Projection{}, err
	}
	files["tsconfig.json"], err = encode(rootTSConfig{Files: []string{}, References: rootReferences})
	if err != nil {
		return Projection{}, err
	}
	return Projection{Files: files}, nil
}

var exactPackageVersion = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+(?:-[0-9A-Za-z.-]+)?(?:\+[0-9A-Za-z.-]+)?$`)

func rootTypeScriptVersion(packages []Package) (string, error) {
	authorities := make([]TypeScriptAuthority, 0)
	for _, pkg := range packages {
		if version, exists := pkg.Policy.DevDependencies["typescript"]; exists {
			authorities = append(authorities, TypeScriptAuthority{Package: pkg.Name, Version: version})
		}
	}
	if len(authorities) == 0 {
		return "", &TypeScriptAuthorityError{Reason: "missing"}
	}
	version := authorities[0].Version
	for _, authority := range authorities {
		if !exactPackageVersion.MatchString(authority.Version) {
			return "", &TypeScriptAuthorityError{Reason: "non-exact or non-external", Authorities: authorities}
		}
		if authority.Version != version {
			return "", &TypeScriptAuthorityError{Reason: "conflicting", Authorities: authorities}
		}
	}
	return version, nil
}

func rootExternalDependencies(packages []Package) (map[string]string, error) {
	if _, err := rootTypeScriptVersion(packages); err != nil {
		return nil, err
	}
	authorities := make(map[string][]RootDependencyAuthority)
	for _, pkg := range packages {
		for _, class := range dependencyClasses(pkg.Policy) {
			for _, name := range sortedMapKeys(class.values) {
				version := class.values[name]
				if isWorkspaceProtocol(version) {
					continue
				}
				authorities[name] = append(authorities[name], RootDependencyAuthority{
					Package: pkg.Name, Class: class.name, Name: name, Version: version,
				})
			}
		}
	}
	root := make(map[string]string, len(authorities))
	for _, name := range sortedMapKeysOfAuthorities(authorities) {
		declarations := authorities[name]
		version := declarations[0].Version
		for _, declaration := range declarations {
			if !exactPackageVersion.MatchString(declaration.Version) {
				return nil, &RootDependencyAuthorityError{Dependency: name, Reason: "non-exact", Authorities: declarations}
			}
			if declaration.Version != version {
				return nil, &RootDependencyAuthorityError{Dependency: name, Reason: "conflicting", Authorities: declarations}
			}
		}
		root[name] = version
	}
	return root, nil
}

func sortedMapKeysOfAuthorities(values map[string][]RootDependencyAuthority) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func renderPackage(pkg Package, byName map[string]Package, productionTypeScript bool) ([]byte, []byte, error) {
	dependencies := copyMap(pkg.Policy.Dependencies)
	devDependencies := copyMap(pkg.Policy.DevDependencies)
	peerDependencies := copyMap(pkg.Policy.PeerDependencies)
	optionalDependencies := copyMap(pkg.Policy.OptionalDependencies)
	for name, version := range pkg.Policy.RequiredButNotReferenced {
		dependencies[name] = version
	}
	internalDependencies := make(map[string]struct{})
	derivedDevelopment := make(map[string]bool)
	for _, observed := range pkg.Imports {
		name := importedPackageName(observed.Specifier)
		if _, exists := byName[name]; !exists || name == pkg.Name {
			continue
		}
		internalDependencies[name] = struct{}{}
		switch dependencyClass(pkg.Policy, name) {
		case "dependencies", "requiredButNotReferenced":
			dependencies[name] = "workspace:*"
		case "devDependencies":
			devDependencies[name] = "workspace:*"
		case "peerDependencies":
			peerDependencies[name] = "workspace:*"
		case "optionalDependencies":
			optionalDependencies[name] = "workspace:*"
		default:
			if observed.Development {
				if _, runtime := dependencies[name]; !runtime {
					devDependencies[name] = "workspace:*"
					derivedDevelopment[name] = true
				}
			} else {
				dependencies[name] = "workspace:*"
				if derivedDevelopment[name] {
					delete(devDependencies, name)
					delete(derivedDevelopment, name)
				}
			}
		}
	}
	exports := any(packageExports{Root: packageRootExport{Types: "./dist/index.d.ts", Default: "./dist/index.js"}})
	if len(pkg.Policy.Exports) != 0 {
		exports = copyMap(pkg.Policy.Exports)
	}
	manifest, err := encode(packageJSON{
		Name:                 pkg.Name,
		Private:              true,
		Type:                 "module",
		Exports:              exports,
		Imports:              nilIfEmpty(copyMap(pkg.Policy.Imports)),
		Dependencies:         nilIfEmpty(dependencies),
		DevDependencies:      nilIfEmpty(devDependencies),
		PeerDependencies:     nilIfEmpty(peerDependencies),
		OptionalDependencies: nilIfEmpty(optionalDependencies),
	})
	if err != nil {
		return nil, nil, fmt.Errorf("render %s package.json: %w", pkg.Name, err)
	}

	referenceNames := make([]string, 0, len(internalDependencies))
	for name := range internalDependencies {
		referenceNames = append(referenceNames, name)
	}
	sort.Strings(referenceNames)
	references := make([]tsReference, 0, len(referenceNames))
	for _, name := range referenceNames {
		relative, err := filepath.Rel(pkg.Directory, byName[name].Directory)
		if err != nil {
			return nil, nil, fmt.Errorf("reference %s -> %s: %w", pkg.Name, name, err)
		}
		references = append(references, tsReference{Path: filepath.ToSlash(relative)})
	}
	compilerOptions := map[string]any{
		"composite":        true,
		"declaration":      true,
		"module":           "NodeNext",
		"moduleResolution": "NodeNext",
		"outDir":           "dist",
		"rootDir":          "src",
		"strict":           true,
		"target":           "ES2022",
		"tsBuildInfoFile":  "dist/tsconfig.tsbuildinfo",
	}
	if productionTypeScript {
		compilerOptions["allowImportingTsExtensions"] = true
		compilerOptions["emitDeclarationOnly"] = true
		compilerOptions["module"] = "Preserve"
		compilerOptions["moduleResolution"] = "Bundler"
		compilerOptions["skipLibCheck"] = true
	}
	tsconfig, err := encode(packageTSConfig{
		CompilerOptions: compilerOptions,
		Include:         []string{"src/**/*.ts", "src/**/*.tsx"},
		References:      references,
	})
	if err != nil {
		return nil, nil, fmt.Errorf("render %s tsconfig.json: %w", pkg.Name, err)
	}
	return manifest, tsconfig, nil
}

func validatePackagePolicies(packages []Package, byName map[string]Package) error {
	for _, pkg := range packages {
		declaredBy := make(map[string]string)
		for _, class := range dependencyClasses(pkg.Policy) {
			names := sortedMapKeys(class.values)
			for _, name := range names {
				version := class.values[name]
				if previous, exists := declaredBy[name]; exists {
					return fmt.Errorf("package %q dependency %q is declared in both %s and %s; declare each external dependency in exactly one class", pkg.Name, name, previous, class.name)
				}
				declaredBy[name] = class.name
				if strings.HasPrefix(version, "catalog:") {
					return fmt.Errorf("package %q dependency %q in %s declares %q, but the generated root has no package catalog; declare an exact resolved version in %s", pkg.Name, name, class.name, version, class.name)
				}
				if _, participating := byName[name]; participating && version != "workspace:*" {
					return fmt.Errorf("package %q dependency %q participates in the assembled closure but %s declares version %q; participating dependencies must use workspace:*", pkg.Name, name, class.name, version)
				}
			}
		}
	}
	return nil
}

func closureDiagnostics(packages []Package, byName map[string]Package) []ClosureDiagnostic {
	diagnostics := make([]ClosureDiagnostic, 0)
	for _, pkg := range packages {
		observedPackages := make(map[string]struct{})
		for _, observed := range pkg.Imports {
			if isBarePackageSpecifier(observed.Specifier) {
				observedPackages[importedPackageName(observed.Specifier)] = struct{}{}
			}
		}
		for _, class := range dependencyClasses(pkg.Policy) {
			for _, name := range sortedMapKeys(class.values) {
				version := class.values[name]
				if !isWorkspaceProtocol(version) {
					continue
				}
				if _, participating := byName[name]; participating {
					continue
				}
				if _, observed := observedPackages[name]; observed {
					continue
				}
				diagnostics = append(diagnostics, ClosureDiagnostic{
					Kind: MissingWorkspaceDependency, Importer: pkg.Name, Specifier: name,
					Source: pkg.Directory, MissingPackage: name, DependencyClass: class.name,
					Remedy: workspaceClosureRemedy(name, class.name, version),
				})
			}
		}
		for _, observed := range pkg.Imports {
			specifier := observed.Specifier
			switch {
			case strings.HasPrefix(specifier, "."), strings.HasPrefix(specifier, "/"), strings.Contains(specifier, ":"):
				continue
			case strings.HasPrefix(specifier, "#"):
				if !mappingContains(pkg.Policy.Imports, specifier) {
					diagnostics = append(diagnostics, ClosureDiagnostic{
						Kind: MissingImport, Importer: pkg.Name, Specifier: specifier,
						Source: observed.Source, Line: observed.Line,
						Remedy: fmt.Sprintf("declare a matching imports entry for %q in package %q", specifier, pkg.Name),
					})
				}
				continue
			}

			name := importedPackageName(specifier)
			target, participating := byName[name]
			if !participating {
				if externalDependencyDeclared(pkg.Policy, name) {
					continue
				}
				remedy := fmt.Sprintf("add the Repository that declares %q to includes, or declare %q in exactly one external dependency class", name, name)
				if class, version, workspace := workspaceDependencyDeclaration(pkg.Policy, name); workspace {
					remedy = workspaceClosureRemedy(name, class, version)
				}
				diagnostics = append(diagnostics, ClosureDiagnostic{
					Kind: MissingPackage, Importer: pkg.Name, Specifier: specifier,
					Source: observed.Source, Line: observed.Line, MissingPackage: name,
					Remedy: remedy,
				})
				continue
			}

			export := importedPackageExport(name, specifier)
			if !exportAvailable(target.Policy.Exports, export) {
				diagnostics = append(diagnostics, ClosureDiagnostic{
					Kind: MissingExport, Importer: pkg.Name, Specifier: specifier,
					Source: observed.Source, Line: observed.Line, MissingPackage: name,
					Remedy: fmt.Sprintf("declare exports[%q] on participating package %q", export, name),
				})
			}
		}
	}
	return diagnostics
}

func workspaceDependencyDeclaration(policy contract.PackagePolicy, name string) (string, string, bool) {
	for _, class := range dependencyClasses(policy) {
		if version, exists := class.values[name]; exists && isWorkspaceProtocol(version) {
			return class.name, version, true
		}
	}
	return "", "", false
}

func externalDependencyDeclared(policy contract.PackagePolicy, name string) bool {
	for _, values := range []map[string]string{policy.Dependencies, policy.DevDependencies, policy.RequiredButNotReferenced, policy.PeerDependencies, policy.OptionalDependencies} {
		if version, exists := values[name]; exists {
			return !isWorkspaceProtocol(version)
		}
	}
	return false
}

func dependencyClass(policy contract.PackagePolicy, name string) string {
	for _, candidate := range dependencyClasses(policy) {
		if _, exists := candidate.values[name]; exists {
			return candidate.name
		}
	}
	return ""
}

type dependencyClassValues struct {
	name   string
	values map[string]string
}

func dependencyClasses(policy contract.PackagePolicy) []dependencyClassValues {
	return []dependencyClassValues{
		{name: "dependencies", values: policy.Dependencies},
		{name: "devDependencies", values: policy.DevDependencies},
		{name: "requiredButNotReferenced", values: policy.RequiredButNotReferenced},
		{name: "peerDependencies", values: policy.PeerDependencies},
		{name: "optionalDependencies", values: policy.OptionalDependencies},
	}
}

func isWorkspaceProtocol(version string) bool {
	return strings.HasPrefix(version, "workspace:")
}

func workspaceClosureRemedy(name, class, version string) string {
	if version == "workspace:*" {
		return fmt.Sprintf("add the Repository that declares %q to includes; %s version %q requires that package to participate in the assembled closure", name, class, version)
	}
	return fmt.Sprintf("replace %s version %q for %q with exact workspace:* and add the Repository that declares %q to includes", class, version, name, name)
}

func isBarePackageSpecifier(specifier string) bool {
	return !strings.HasPrefix(specifier, ".") && !strings.HasPrefix(specifier, "/") && !strings.HasPrefix(specifier, "#") && !strings.Contains(specifier, ":")
}

func exportAvailable(exports map[string]string, requested string) bool {
	if len(exports) == 0 {
		return requested == "."
	}
	return mappingContains(exports, requested)
}

func importedPackageExport(name, specifier string) string {
	if specifier == name {
		return "."
	}
	return "." + strings.TrimPrefix(specifier, name)
}

func mappingContains(values map[string]string, requested string) bool {
	if _, exists := values[requested]; exists {
		return true
	}
	for pattern := range values {
		if strings.HasSuffix(pattern, "*") && strings.HasPrefix(requested, strings.TrimSuffix(pattern, "*")) {
			return true
		}
	}
	return false
}

func sortedMapKeys(values map[string]string) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func Apply(root string, projection Projection) ([]string, error) {
	paths := make([]string, 0, len(projection.Files))
	for path := range projection.Files {
		if err := validateRelativeFile(path); err != nil {
			return nil, err
		}
		paths = append(paths, path)
	}
	sort.Strings(paths)
	changed := make([]string, 0, len(paths))
	for _, relative := range paths {
		target := filepath.Join(root, relative)
		contents := projection.Files[relative]
		before, err := os.ReadFile(target)
		if err == nil && bytes.Equal(before, contents) {
			continue
		}
		if err != nil && !os.IsNotExist(err) {
			return nil, fmt.Errorf("read generated path %q: %w", relative, err)
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return nil, fmt.Errorf("create parent for %q: %w", relative, err)
		}
		if err := os.WriteFile(target, contents, 0o644); err != nil {
			return nil, fmt.Errorf("write generated path %q: %w", relative, err)
		}
		changed = append(changed, filepath.ToSlash(relative))
	}
	return changed, nil
}

func importedPackageName(specifier string) string {
	parts := strings.Split(specifier, "/")
	if strings.HasPrefix(specifier, "@") && len(parts) >= 2 {
		return parts[0] + "/" + parts[1]
	}
	return parts[0]
}

func encode(value any) ([]byte, error) {
	encoded, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(encoded, '\n'), nil
}

func copyMap(source map[string]string) map[string]string {
	result := make(map[string]string, len(source))
	for name, version := range source {
		result[name] = version
	}
	return result
}

func nilIfEmpty(values map[string]string) map[string]string {
	if len(values) == 0 {
		return nil
	}
	return values
}

func validateRelativeDirectory(path string) error {
	if path == "" || filepath.IsAbs(path) || filepath.Clean(path) != path || path == "." || path == ".." || strings.HasPrefix(path, ".."+string(filepath.Separator)) {
		return fmt.Errorf("directory %q is not a confined relative path", path)
	}
	return nil
}

func validateRelativeFile(path string) error {
	if filepath.Base(path) != "package.json" && filepath.Base(path) != "tsconfig.json" {
		return fmt.Errorf("path %q is not a Workbench-owned workspace projection", path)
	}
	if filepath.IsAbs(path) || filepath.Clean(path) != path || path == ".." || strings.HasPrefix(path, ".."+string(filepath.Separator)) {
		return fmt.Errorf("path %q escapes the workbench root", path)
	}
	return nil
}
