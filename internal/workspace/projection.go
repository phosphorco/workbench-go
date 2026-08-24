package workspace

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/phosphorco/workbench-go/internal/contract"
)

type Package struct {
	Name      string
	Directory string
	Imports   []string
	Policy    contract.PackagePolicy
}

type Projection struct {
	Files map[string][]byte
}

type packageJSON struct {
	Name                 string            `json:"name"`
	Private              bool              `json:"private"`
	Type                 string            `json:"type"`
	Dependencies         map[string]string `json:"dependencies,omitempty"`
	PeerDependencies     map[string]string `json:"peerDependencies,omitempty"`
	OptionalDependencies map[string]string `json:"optionalDependencies,omitempty"`
}

type rootPackageJSON struct {
	Name       string   `json:"name"`
	Private    bool     `json:"private"`
	Workspaces []string `json:"workspaces"`
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

	files := make(map[string][]byte, 2+len(ordered)*2)
	workspaces := make([]string, 0, len(ordered))
	rootReferences := make([]tsReference, 0, len(ordered))
	for _, pkg := range ordered {
		workspaces = append(workspaces, filepath.ToSlash(pkg.Directory))
		rootReferences = append(rootReferences, tsReference{Path: "./" + filepath.ToSlash(pkg.Directory)})
		manifest, tsconfig, err := renderPackage(pkg, byName)
		if err != nil {
			return Projection{}, err
		}
		files[filepath.Join(pkg.Directory, "package.json")] = manifest
		files[filepath.Join(pkg.Directory, "tsconfig.json")] = tsconfig
	}
	var err error
	files["package.json"], err = encode(rootPackageJSON{Name: "workbench", Private: true, Workspaces: workspaces})
	if err != nil {
		return Projection{}, err
	}
	files["tsconfig.json"], err = encode(rootTSConfig{Files: []string{}, References: rootReferences})
	if err != nil {
		return Projection{}, err
	}
	return Projection{Files: files}, nil
}

func renderPackage(pkg Package, byName map[string]Package) ([]byte, []byte, error) {
	dependencies := copyMap(pkg.Policy.RequiredButNotReferenced)
	internalDependencies := make(map[string]struct{})
	for _, specifier := range pkg.Imports {
		name := importedPackageName(specifier)
		if _, exists := byName[name]; !exists || name == pkg.Name {
			continue
		}
		dependencies[name] = "workspace:*"
		internalDependencies[name] = struct{}{}
	}
	for name := range pkg.Policy.PeerDependencies {
		delete(dependencies, name)
	}
	for name := range pkg.Policy.OptionalDependencies {
		delete(dependencies, name)
	}
	manifest, err := encode(packageJSON{
		Name:                 pkg.Name,
		Private:              true,
		Type:                 "module",
		Dependencies:         nilIfEmpty(dependencies),
		PeerDependencies:     nilIfEmpty(copyMap(pkg.Policy.PeerDependencies)),
		OptionalDependencies: nilIfEmpty(copyMap(pkg.Policy.OptionalDependencies)),
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
	tsconfig, err := encode(packageTSConfig{
		CompilerOptions: map[string]any{
			"composite":        true,
			"declaration":      true,
			"module":           "NodeNext",
			"moduleResolution": "NodeNext",
			"outDir":           "dist",
			"rootDir":          "src",
			"strict":           true,
			"target":           "ES2022",
		},
		Include:    []string{"src/**/*.ts", "src/**/*.tsx"},
		References: references,
	})
	if err != nil {
		return nil, nil, fmt.Errorf("render %s tsconfig.json: %w", pkg.Name, err)
	}
	return manifest, tsconfig, nil
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
