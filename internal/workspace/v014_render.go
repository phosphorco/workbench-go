package workspace

import (
	"fmt"
	"path"
	"sort"
	"strings"

	"github.com/phosphorco/workbench-go/internal/contract"
	"github.com/phosphorco/workbench-go/internal/orderedjson"
)

func (build *layoutBuild) render() (LayoutProjection, error) {
	files := map[string][]byte{}
	put := func(name string, value orderedjson.Value) error {
		if _, exists := files[name]; exists {
			return fmt.Errorf("two owned files render %q", name)
		}
		encoded, err := orderedjson.Render(value)
		if err != nil {
			return fmt.Errorf("render %s: %w", name, err)
		}
		files[name] = encoded
		return nil
	}
	for _, pkg := range build.input.Packages {
		manifest, err := build.packageManifest(pkg)
		if err != nil {
			return LayoutProjection{}, err
		}
		if err := put(path.Join(pkg.Directory, "package.json"), manifest); err != nil {
			return LayoutProjection{}, err
		}
		aggregate := build.document()
		aggregate.Set("files", orderedjson.ArrayValue())
		roleReferences := make([]string, 0, len(pkg.Roles))
		for _, role := range pkg.Roles {
			roleReferences = append(roleReferences, relativeReference(pkg.Directory, role.Config))
			document, err := build.roleDocument(role, pkg.Directory, build.references[roleKey{pkg.Name, role.Name}], true, build.emits[roleKey{pkg.Name, role.Name}])
			if err != nil {
				return LayoutProjection{}, fmt.Errorf("package %q role %q: %w", pkg.Name, role.Name, err)
			}
			if err := put(role.Config, document); err != nil {
				return LayoutProjection{}, err
			}
		}
		sort.Strings(roleReferences)
		aggregate.Set("references", referenceArray(roleReferences, nil))
		if err := put(path.Join(pkg.Directory, "tsconfig.json"), aggregate); err != nil {
			return LayoutProjection{}, err
		}
	}
	for _, name := range sortedKeysOf(build.input.Bases) {
		document, err := build.baseDocument(name)
		if err != nil {
			return LayoutProjection{}, err
		}
		if err := put(baseFile(build.input.Bases[name].Directory), document); err != nil {
			return LayoutProjection{}, err
		}
	}
	for _, project := range build.input.Projects {
		emits := true
		if noEmit, err := build.noEmit(project.Role, path.Dir(project.Config)); err != nil {
			return LayoutProjection{}, err
		} else if noEmit {
			emits = false
		}
		document, err := build.roleDocument(project, path.Dir(project.Config), build.projectRefs[project.Config], false, emits)
		if err != nil {
			return LayoutProjection{}, fmt.Errorf("project %q: %w", project.Config, err)
		}
		if err := put(project.Config, document); err != nil {
			return LayoutProjection{}, err
		}
	}
	indexDirectories := build.indexDirectories()
	for _, directory := range indexDirectories {
		roles := map[string][]string{}
		for _, pkg := range build.input.Packages {
			if directory != "." && !strings.HasPrefix(pkg.Directory, directory+"/") {
				continue
			}
			for _, role := range pkg.Roles {
				roles[role.Name] = append(roles[role.Name], role.Config)
			}
		}
		indexRoot := joinDirectory(directory, ".tsconfigs")
		references := []string{}
		for _, roleName := range sortedKeysOf(roles) {
			configs := make([]string, 0, len(roles[roleName]))
			for _, config := range roles[roleName] {
				configs = append(configs, relativeReference(indexRoot, config))
			}
			sort.Strings(configs)
			index := build.document()
			index.Set("files", orderedjson.ArrayValue())
			index.Set("references", referenceArray(configs, nil))
			name := path.Join(indexRoot, "tsconfig."+roleName+".child-index.json")
			if err := put(name, index); err != nil {
				return LayoutProjection{}, err
			}
			references = append(references, relativeReference(directory, name))
		}
		for _, project := range build.input.Projects {
			if path.Dir(project.Config) == directory {
				references = append(references, relativeReference(directory, project.Config))
			}
		}
		sort.Strings(references)
		solution := build.document()
		solution.Set("files", orderedjson.ArrayValue())
		solution.Set("references", referenceArray(references, nil))
		if err := put(joinDirectory(directory, "tsconfig.json"), solution); err != nil {
			return LayoutProjection{}, err
		}
	}
	if build.input.Root != nil {
		manifest, err := build.rootManifest()
		if err != nil {
			return LayoutProjection{}, err
		}
		if err := put("package.json", manifest); err != nil {
			return LayoutProjection{}, err
		}
	}
	sort.SliceStable(build.notes, func(left, right int) bool {
		if build.notes[left].Kind != build.notes[right].Kind {
			return build.notes[left].Kind < build.notes[right].Kind
		}
		if build.notes[left].Path != build.notes[right].Path {
			return build.notes[left].Path < build.notes[right].Path
		}
		return build.notes[left].Detail < build.notes[right].Detail
	})
	return LayoutProjection{Files: files, Notes: dedupeNotes(build.notes), Graph: build.summary, IndexDirectories: indexDirectories}, nil
}

func dedupeNotes(notes []Note) []Note {
	result := make([]Note, 0, len(notes))
	for index, note := range notes {
		if index > 0 && notes[index-1] == note {
			continue
		}
		result = append(result, note)
	}
	return result
}

// indexDirectories are the repository root and every base directory with a
// package beneath it.
func (build *layoutBuild) indexDirectories() []string {
	directories := []string{"."}
	for _, name := range sortedKeysOf(build.input.Bases) {
		directory := build.input.Bases[name].Directory
		if directory == "." {
			continue
		}
		for _, pkg := range build.input.Packages {
			if strings.HasPrefix(pkg.Directory, directory+"/") {
				directories = append(directories, directory)
				break
			}
		}
	}
	sort.Strings(directories)
	return directories
}

func joinDirectory(directory, name string) string {
	if directory == "." || directory == "" {
		return name
	}
	return path.Join(directory, name)
}

func baseFile(directory string) string { return joinDirectory(directory, "tsconfig.base.json") }

// BaseFile returns the path of a base written to directory.
func BaseFile(directory string) string { return baseFile(directory) }

func (build *layoutBuild) document() orderedjson.Value {
	document := orderedjson.ObjectValue()
	if build.input.GeneratedMarker != nil {
		document.Set("//", orderedjson.StringValue(*build.input.GeneratedMarker))
	}
	return document
}

func referenceArray(paths []string, circular map[string]bool) orderedjson.Value {
	items := make([]orderedjson.Value, 0, len(paths))
	for _, value := range paths {
		item := orderedjson.ObjectValue()
		item.Set("path", orderedjson.StringValue(value))
		if circular[value] {
			item.Set("circular", orderedjson.BoolValue(true))
		}
		items = append(items, item)
	}
	return orderedjson.ArrayValue(items...)
}

func roleOutputName(name string) string {
	if strings.HasSuffix(name, ".json") {
		base := strings.TrimSuffix(path.Base(name), ".json")
		return strings.TrimPrefix(strings.TrimPrefix(base, "tsconfig"), ".")
	}
	return name
}

func (build *layoutBuild) roleDocument(role LayoutRole, directory string, references []layoutReference, packageRole, emits bool) (orderedjson.Value, error) {
	document := build.document()
	base := RoleBase(role.Role, directory, build.input.Bases)
	if base != "" {
		document.Set("extends", orderedjson.StringValue(relativeReference(path.Dir(role.Config), baseFile(build.input.Bases[base].Directory))))
	}
	options := copyObject(role.Role.CompilerOptions)
	if emits {
		output := roleOutputName(role.Name)
		outDir := "${configDir}/dist-types"
		if output != "" && output != contract.DefaultRole {
			outDir += "/" + output
		}
		if base == "" {
			options.Set("composite", orderedjson.BoolValue(true))
			options.Set("outDir", orderedjson.StringValue(outDir))
		} else if output != contract.DefaultRole {
			options.Set("outDir", orderedjson.StringValue(outDir))
		}
	}
	if len(options.Members) != 0 {
		document.Set("compilerOptions", options)
	}
	for _, list := range []struct {
		key    string
		values *[]string
	}{{"files", role.Role.Files}, {"include", role.Role.Include}, {"exclude", role.Role.Exclude}} {
		if list.values != nil {
			document.Set(list.key, orderedjson.StringArray(*list.values))
		}
	}
	if packageRole || len(references) != 0 {
		paths := make([]string, 0, len(references))
		circular := map[string]bool{}
		for _, reference := range references {
			paths = append(paths, reference.path)
			circular[reference.path] = reference.circular
		}
		document.Set("references", referenceArray(paths, circular))
	}
	return document, nil
}

func copyObject(value orderedjson.Value) orderedjson.Value {
	result := orderedjson.ObjectValue()
	if value.Kind == orderedjson.Object {
		result.Members = append(result.Members, value.Members...)
	}
	return result
}

func (build *layoutBuild) baseDocument(name string) (orderedjson.Value, error) {
	base := build.input.Bases[name]
	document := build.document()
	if base.Schema != nil {
		document.Set("$schema", orderedjson.StringValue(*base.Schema))
	}
	if base.Extends != nil {
		parent := build.input.Bases[*base.Extends]
		document.Set("extends", orderedjson.StringValue(relativeReference(base.Directory, baseFile(parent.Directory))))
	}
	if base.TypeAcquisition != nil {
		document.Set("typeAcquisition", *base.TypeAcquisition)
	}
	options := copyObject(base.CompilerOptions)
	if base.Extends == nil {
		options.Set("composite", orderedjson.BoolValue(true))
		options.Set("outDir", orderedjson.StringValue("${configDir}/dist-types"))
	}
	document.Set("compilerOptions", options)
	if base.Include != nil {
		document.Set("include", orderedjson.StringArray(*base.Include))
	}
	if base.Exclude != nil {
		document.Set("exclude", orderedjson.StringArray(*base.Exclude))
	}
	return document, nil
}

func stringMapValue(values map[string]string) orderedjson.Value {
	object := orderedjson.ObjectValue()
	for _, key := range sortedMapKeys(values) {
		object.Set(key, orderedjson.StringValue(values[key]))
	}
	return object
}

func (build *layoutBuild) packageDependencies(pkg LayoutPackage) (map[string]string, map[string]string, map[string]string, map[string]string) {
	dependencies := copyMap(pkg.Policy.Dependencies)
	devDependencies := copyMap(pkg.Policy.DevDependencies)
	peerDependencies := copyMap(pkg.Policy.PeerDependencies)
	optionalDependencies := copyMap(pkg.Policy.OptionalDependencies)
	for name, version := range pkg.Policy.RequiredButNotReferenced {
		dependencies[name] = version
	}
	production := map[string]bool{}
	development := map[string]bool{}
	for _, role := range pkg.Roles {
		for _, observed := range role.Imports {
			name, ok := build.workspaceImport(observed.Specifier)
			if !ok || name == pkg.Name {
				continue
			}
			if role.Role.Development {
				development[name] = true
			} else {
				production[name] = true
			}
		}
	}
	for _, name := range sortedKeysOf(production) {
		if dependencyClass(pkg.Policy, name) == "" {
			dependencies[name] = "workspace:*"
		}
	}
	for _, name := range sortedKeysOf(development) {
		if dependencyClass(pkg.Policy, name) == "" && !production[name] {
			devDependencies[name] = "workspace:*"
		}
	}
	return dependencies, devDependencies, peerDependencies, optionalDependencies
}

func (build *layoutBuild) packageManifest(pkg LayoutPackage) (orderedjson.Value, error) {
	manifest := build.document()
	manifest.Set("name", orderedjson.StringValue(pkg.Name))
	fields := pkg.Policy.Fields
	if version, exists := fields.Get("version"); exists {
		manifest.Set("version", version)
	}
	if pkg.Policy.Private != nil {
		manifest.Set("private", orderedjson.BoolValue(*pkg.Policy.Private))
	}
	manifest.Set("type", orderedjson.StringValue("module"))
	for _, member := range fields.Members {
		if member.Key == "version" {
			continue
		}
		manifest.Set(member.Key, member.Value)
	}
	if len(pkg.Policy.Imports) != 0 {
		manifest.Set("imports", stringMapValue(pkg.Policy.Imports))
	}
	if len(pkg.Policy.Exports) != 0 {
		manifest.Set("exports", stringMapValue(pkg.Policy.Exports))
	}
	dependencies, devDependencies, peerDependencies, optionalDependencies := build.packageDependencies(pkg)
	for _, class := range []struct {
		key    string
		values map[string]string
	}{{"dependencies", dependencies}, {"devDependencies", devDependencies}, {"peerDependencies", peerDependencies}, {"optionalDependencies", optionalDependencies}} {
		if len(class.values) != 0 {
			manifest.Set(class.key, stringMapValue(class.values))
		}
	}
	return manifest, nil
}

func (build *layoutBuild) insidePackage(file string) bool {
	for _, pkg := range build.input.Packages {
		if contract.PathWithin(file, pkg.Directory) {
			return true
		}
	}
	return false
}

func (build *layoutBuild) rootManifest() (orderedjson.Value, error) {
	fields := build.input.Root.PackageJSON.Fields
	if fields.Kind != orderedjson.Object {
		fields = orderedjson.ObjectValue()
	}
	for _, key := range fields.Keys() {
		if key == "//" {
			return orderedjson.Value{}, fmt.Errorf("root packageJson field %q is owned by Workbench (generatedMarker)", key)
		}
	}
	authored := map[string]map[string]string{}
	for _, class := range []string{"dependencies", "devDependencies"} {
		authored[class] = map[string]string{}
		value, exists := fields.Get(class)
		if !exists {
			continue
		}
		if value.Kind != orderedjson.Object {
			return orderedjson.Value{}, fmt.Errorf("root packageJson %s must be an object", class)
		}
		for _, member := range value.Members {
			if member.Value.Kind != orderedjson.String {
				return orderedjson.Value{}, fmt.Errorf("root packageJson %s %q must be a string", class, member.Key)
			}
			version := member.Value.Text()
			if isWorkspaceProtocol(version) {
				if _, placed := build.byName[member.Key]; !placed {
					return orderedjson.Value{}, fmt.Errorf("root packageJson %s %q declares %q, but %q is not a placed package", class, member.Key, version, member.Key)
				}
			}
			authored[class][member.Key] = version
		}
	}
	if err := build.admitCatalogs(fields, authored); err != nil {
		return orderedjson.Value{}, err
	}
	derived := map[string]map[string]string{"dependencies": {}, "devDependencies": {}}
	for _, project := range build.input.Projects {
		if build.insidePackage(project.Config) {
			continue
		}
		class := "dependencies"
		if project.Role.Development {
			class = "devDependencies"
		}
		for _, observed := range project.Imports {
			name, ok := build.workspaceImport(observed.Specifier)
			if !ok {
				continue
			}
			if _, declared := authored["dependencies"][name]; declared {
				continue
			}
			if _, declared := authored["devDependencies"][name]; declared {
				continue
			}
			derived[class][name] = "workspace:*"
		}
	}
	for name := range derived["devDependencies"] {
		if _, production := derived["dependencies"][name]; production {
			delete(derived["devDependencies"], name)
		}
	}
	if _, hasCompiler := authored["dependencies"]["typescript"]; !hasCompiler {
		if _, hasCompiler := authored["devDependencies"]["typescript"]; !hasCompiler {
			return orderedjson.Value{}, fmt.Errorf("the authored root declares no typescript dependency; remedy: declare the compiler package in root dependencies or devDependencies")
		}
	}
	workspaces, err := build.admitWorkspaces(fields)
	if err != nil {
		return orderedjson.Value{}, err
	}
	manifest := build.document()
	for _, member := range fields.Members {
		switch member.Key {
		case "dependencies", "devDependencies":
			merged := copyMap(authored[member.Key])
			for name, version := range derived[member.Key] {
				merged[name] = version
			}
			manifest.Set(member.Key, stringMapValue(merged))
		default:
			manifest.Set(member.Key, member.Value)
		}
	}
	if _, exists := fields.Get("workspaces"); !exists {
		manifest.Set("workspaces", orderedjson.StringArray(workspaces))
	}
	for _, class := range []string{"dependencies", "devDependencies"} {
		if _, exists := fields.Get(class); !exists && len(derived[class]) != 0 {
			manifest.Set(class, stringMapValue(derived[class]))
		}
	}
	return manifest, nil
}

// admitWorkspaces checks authored workspace globs against the placed package
// directories and returns the derived list.
func (build *layoutBuild) admitWorkspaces(fields orderedjson.Value) ([]string, error) {
	placed := make([]string, 0, len(build.input.Packages))
	placedSet := map[string]bool{}
	for _, pkg := range build.input.Packages {
		placed = append(placed, pkg.Directory)
		placedSet[pkg.Directory] = true
	}
	sort.Strings(placed)
	value, exists := fields.Get("workspaces")
	if !exists {
		return placed, nil
	}
	if value.Kind != orderedjson.Array {
		return nil, fmt.Errorf("root packageJson workspaces must be an array of globs")
	}
	candidates := map[string]bool{}
	for _, directory := range build.input.ManifestDirectories {
		candidates[directory] = true
	}
	for directory := range placedSet {
		candidates[directory] = true
	}
	expanded := map[string]bool{}
	for _, item := range value.Items {
		if item.Kind != orderedjson.String {
			return nil, fmt.Errorf("root packageJson workspaces entries must be strings")
		}
		glob := strings.TrimSuffix(strings.TrimPrefix(item.Text(), "./"), "/")
		if _, err := path.Match(glob, ""); err != nil {
			return nil, fmt.Errorf("root packageJson workspaces glob %q: %w", item.Text(), err)
		}
		for directory := range candidates {
			if matched, _ := path.Match(glob, directory); matched {
				expanded[directory] = true
			}
		}
	}
	unplaced := []string{}
	for directory := range expanded {
		if !placedSet[directory] {
			unplaced = append(unplaced, directory)
		}
	}
	uncovered := []string{}
	for _, directory := range placed {
		if !expanded[directory] {
			uncovered = append(uncovered, directory)
		}
	}
	sort.Strings(unplaced)
	if len(unplaced) != 0 || len(uncovered) != 0 {
		return nil, fmt.Errorf("root packageJson workspaces must expand to exactly the placed package directories: %d matched but not placed %q, %d placed but not matched %q", len(unplaced), unplaced, len(uncovered), uncovered)
	}
	return placed, nil
}

// admitCatalogs checks every catalog: reference against the authored root,
// every patchedDependencies path, and reports specifier disagreements.
func (build *layoutBuild) admitCatalogs(fields orderedjson.Value, rootDependencies map[string]map[string]string) error {
	catalogs := map[string]map[string]bool{}
	readCatalog := func(name string, value orderedjson.Value) error {
		if value.Kind != orderedjson.Object {
			return fmt.Errorf("root packageJson catalog %q must be an object", name)
		}
		if catalogs[name] != nil {
			return fmt.Errorf("root packageJson declares the default catalog twice (catalog and catalogs.default)")
		}
		catalogs[name] = map[string]bool{}
		for _, key := range value.Keys() {
			catalogs[name][key] = true
		}
		return nil
	}
	if value, exists := fields.Get("catalog"); exists {
		if err := readCatalog("default", value); err != nil {
			return err
		}
	}
	if value, exists := fields.Get("catalogs"); exists {
		if value.Kind != orderedjson.Object {
			return fmt.Errorf("root packageJson catalogs must be an object")
		}
		for _, member := range value.Members {
			if err := readCatalog(member.Key, member.Value); err != nil {
				return err
			}
		}
	}
	check := func(owner, class, name, version string) error {
		if !strings.HasPrefix(version, "catalog:") {
			return nil
		}
		catalog := strings.TrimPrefix(version, "catalog:")
		if catalog == "" {
			catalog = "default"
		}
		if !catalogs[catalog][name] {
			where := "catalog"
			if catalog != "default" {
				where = "catalogs." + catalog
			}
			return fmt.Errorf("%s %s %q declares %q, but the authored root has no %s[%q]; remedy: add the key to the root %s or change the specifier", owner, class, name, version, where, name, where)
		}
		return nil
	}
	specifiers := map[string]map[string][]string{}
	for _, pkg := range build.input.Packages {
		for _, class := range dependencyClasses(pkg.Policy) {
			for _, name := range sortedMapKeys(class.values) {
				version := class.values[name]
				if err := check("package "+pkg.Name, class.name, name, version); err != nil {
					return err
				}
				if isWorkspaceProtocol(version) {
					continue
				}
				if specifiers[name] == nil {
					specifiers[name] = map[string][]string{}
				}
				specifiers[name][version] = append(specifiers[name][version], pkg.Name)
			}
		}
	}
	for _, class := range []string{"dependencies", "devDependencies"} {
		for _, name := range sortedMapKeys(rootDependencies[class]) {
			if err := check("root", class, name, rootDependencies[class][name]); err != nil {
				return err
			}
		}
	}
	for _, name := range sortedKeysOf(specifiers) {
		if len(specifiers[name]) < 2 {
			continue
		}
		parts := []string{}
		for _, version := range sortedKeysOf(specifiers[name]) {
			parts = append(parts, fmt.Sprintf("%q by %d", version, len(specifiers[name][version])))
		}
		build.notes = append(build.notes, Note{Kind: "specifierDisagreement", Detail: fmt.Sprintf("%s: %s", name, strings.Join(parts, ", "))})
	}
	if value, exists := fields.Get("patchedDependencies"); exists {
		if value.Kind != orderedjson.Object {
			return fmt.Errorf("root packageJson patchedDependencies must be an object")
		}
		for _, member := range value.Members {
			if member.Value.Kind != orderedjson.String {
				return fmt.Errorf("root packageJson patchedDependencies %q must name a path", member.Key)
			}
			patch := strings.TrimPrefix(member.Value.Text(), "./")
			if err := contract.ValidateRepositoryRelativePath(patch); err != nil || build.input.Exists == nil || !build.input.Exists(patch) {
				return fmt.Errorf("root packageJson patchedDependencies %q names %q, which does not exist", member.Key, member.Value.Text())
			}
		}
	}
	return nil
}
