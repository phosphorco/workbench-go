package contract

import (
	"fmt"
	"path"
	"regexp"
	"sort"
	"strings"

	"github.com/phosphorco/workbench-go/internal/orderedjson"
)

// Contract 0.14 TypeScript layout. Every field here is rendered only by 0.14
// declarations; earlier contracts never decode them.

var (
	roleNamePattern = regexp.MustCompile(`^[a-z][a-z0-9]*$`)
	baseNamePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]*$`)
	// roleReferencePattern is an authored reference: a package name,
	// optionally #<role>.
	roleReferencePattern = regexp.MustCompile(`^(?:@[a-z0-9][a-z0-9._-]*/)?[a-z0-9][a-z0-9._-]*(?:#[a-z][a-z0-9]*)?$`)
)

// DefaultRole is the role a package has when it declares none.
const DefaultRole = "sources"

// OwnedCompilerOptions are the compiler options Workbench writes itself.
var OwnedCompilerOptions = []string{"composite", "outDir", "rootDir", "tsBuildInfoFile"}

// Role is one TypeScript project of a package, or a standalone project.
type Role struct {
	Base             *string           `json:"base,omitempty"`
	ExtendsBase      *bool             `json:"extendsBase,omitempty"`
	Include          *[]string         `json:"include,omitempty"`
	Exclude          *[]string         `json:"exclude,omitempty"`
	Files            *[]string         `json:"files,omitempty"`
	AlsoIncludes     []string          `json:"alsoIncludes,omitempty"`
	ForeignFiles     map[string]string `json:"foreignFiles,omitempty"`
	Development      bool              `json:"development,omitempty"`
	Consumes         []string          `json:"consumes,omitempty"`
	DeriveReferences *bool             `json:"deriveReferences,omitempty"`
	References       []string          `json:"references,omitempty"`
	ReferenceRole    string            `json:"referenceRole,omitempty"`
	CompilerOptions  orderedjson.Value `json:"compilerOptions"`
}

// Derives reports whether the role's references are derived from imports.
func (role Role) Derives(name string) bool {
	if role.DeriveReferences != nil {
		return *role.DeriveReferences
	}
	return name == DefaultRole
}

// TargetRole is the dependency role referenced when an import cannot name one.
func (role Role) TargetRole() string {
	if role.ReferenceRole == "" {
		return DefaultRole
	}
	return role.ReferenceRole
}

// Extends reports whether the role renders an `extends`.
func (role Role) Extends() bool { return role.ExtendsBase == nil || *role.ExtendsBase }

// TsconfigBase is one generated tsconfig.base.json.
type TsconfigBase struct {
	Directory       string             `json:"directory"`
	Extends         *string            `json:"extends,omitempty"`
	Schema          *string            `json:"$schema,omitempty"`
	TypeAcquisition *orderedjson.Value `json:"typeAcquisition,omitempty"`
	CompilerOptions orderedjson.Value  `json:"compilerOptions"`
	Include         *[]string          `json:"include,omitempty"`
	Exclude         *[]string          `json:"exclude,omitempty"`
}

// TypeScriptLayout is the repository's TypeScript block.
type TypeScriptLayout struct {
	Bases    map[string]TsconfigBase `json:"bases"`
	Projects map[string]Role         `json:"projects"`
}

// CircularEdgeSet names the repository-generated circular-edge file.
type CircularEdgeSet struct {
	Path string `json:"path"`
}

// RootManifest is the authored root package.json.
type RootManifest struct {
	PackageJSON struct {
		Fields orderedjson.Value `json:"fields"`
	} `json:"packageJson"`
}

// Empty reports whether the layout declares nothing.
func (layout TypeScriptLayout) Empty() bool {
	return len(layout.Bases) == 0 && len(layout.Projects) == 0
}

// EffectiveRoles returns the package's roles, or the default sources role
// built from sourceRoots and sourceExcludes.
func (policy PackagePolicy) EffectiveRoles() map[string]Role {
	if len(policy.Roles) != 0 {
		return policy.Roles
	}
	include := make([]string, 0, len(policy.EffectiveSourceRoots()))
	for _, root := range policy.EffectiveSourceRoots() {
		include = append(include, "./"+root)
	}
	role := Role{Include: &include, CompilerOptions: orderedjson.ObjectValue()}
	if len(policy.SourceExcludes) != 0 {
		exclude := make([]string, 0, len(policy.SourceExcludes))
		for _, value := range policy.SourceExcludes {
			exclude = append(exclude, "./"+value)
		}
		role.Exclude = &exclude
	}
	return map[string]Role{DefaultRole: role}
}

// SortedRoleNames returns role names in sorted order.
func SortedRoleNames(roles map[string]Role) []string {
	names := make([]string, 0, len(roles))
	for name := range roles {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// ValidateTsconfigPattern checks one include, exclude or files entry. Parent
// segments are admitted only when allowParent is true (declared foreign files).
func ValidateTsconfigPattern(value string, allowParent bool) error {
	if value == "" {
		return fmt.Errorf("must not be empty")
	}
	if strings.HasPrefix(value, "/") {
		return fmt.Errorf("must be relative")
	}
	for _, character := range value {
		if character == '\\' || character < 0x20 || character == 0x7f {
			return fmt.Errorf("must not contain backslashes or control characters")
		}
	}
	if strings.Contains(value, "${") {
		return fmt.Errorf("template variables are only admitted in base include and exclude")
	}
	for _, segment := range strings.Split(strings.TrimPrefix(value, "./"), "/") {
		if segment == ".." && !allowParent {
			return fmt.Errorf("must stay inside its directory; only declared foreignFiles may name a parent")
		}
		if segment == "" {
			return fmt.Errorf("must not contain empty segments")
		}
	}
	return nil
}

// ValidateRole checks one role independently of the Pkl schema. name is the
// role name, or the project path for a standalone project.
func ValidateRole(name string, role Role, roleNames map[string]struct{}, standalone bool) error {
	if !standalone && !roleNamePattern.MatchString(name) {
		return fmt.Errorf("role name %q is not a lowercase identifier", name)
	}
	if role.Base != nil && !baseNamePattern.MatchString(*role.Base) {
		return fmt.Errorf("base %q is not a base name", *role.Base)
	}
	if role.Base != nil && !role.Extends() {
		return fmt.Errorf("names base %q but extendsBase is false", *role.Base)
	}
	for _, list := range []struct {
		name   string
		values *[]string
	}{{"include", role.Include}, {"exclude", role.Exclude}} {
		if list.values == nil {
			continue
		}
		for _, value := range *list.values {
			if err := ValidateTsconfigPattern(value, false); err != nil {
				return fmt.Errorf("%s %q: %w", list.name, value, err)
			}
		}
	}
	if role.Files != nil {
		for _, value := range *role.Files {
			_, foreign := role.ForeignFiles[value]
			if err := ValidateTsconfigPattern(value, foreign); err != nil {
				return fmt.Errorf("files %q: %w", value, err)
			}
			if strings.ContainsAny(value, "*?") {
				return fmt.Errorf("files %q: files entries are literal paths", value)
			}
		}
	}
	for foreign := range role.ForeignFiles {
		listed := false
		if role.Files != nil {
			for _, value := range *role.Files {
				listed = listed || value == foreign
			}
		}
		if !listed {
			return fmt.Errorf("foreignFiles %q is not a files entry", foreign)
		}
		if !strings.HasPrefix(path.Clean(foreign), "../") {
			return fmt.Errorf("foreignFiles %q lies inside the role's directory", foreign)
		}
	}
	for _, list := range []struct {
		name   string
		values []string
	}{{"alsoIncludes", role.AlsoIncludes}, {"consumes", role.Consumes}} {
		seen := map[string]struct{}{}
		for _, value := range list.values {
			if _, duplicate := seen[value]; duplicate {
				return fmt.Errorf("%s names %q twice", list.name, value)
			}
			seen[value] = struct{}{}
			if standalone {
				return fmt.Errorf("a standalone project has no sibling roles for %s", list.name)
			}
			if value == name {
				return fmt.Errorf("%s names the role itself", list.name)
			}
			if _, exists := roleNames[value]; !exists {
				return fmt.Errorf("%s names undeclared role %q", list.name, value)
			}
		}
	}
	if role.ReferenceRole != "" && !roleNamePattern.MatchString(role.ReferenceRole) {
		return fmt.Errorf("referenceRole %q is not a role name", role.ReferenceRole)
	}
	if role.Derives(name) && len(role.References) != 0 {
		return fmt.Errorf("derives its references from imports, so it cannot author references %q", role.References)
	}
	seen := map[string]struct{}{}
	for _, reference := range role.References {
		if !roleReferencePattern.MatchString(reference) {
			return fmt.Errorf("references %q is neither \"<package>\" nor \"<package>#<role>\"", reference)
		}
		if _, duplicate := seen[reference]; duplicate {
			return fmt.Errorf("references names %q twice", reference)
		}
		seen[reference] = struct{}{}
	}
	if err := refuseOwnedCompilerOptions(role.CompilerOptions, true); err != nil {
		return err
	}
	return nil
}

func refuseOwnedCompilerOptions(options orderedjson.Value, role bool) error {
	if options.Kind != orderedjson.Object && options.Kind != orderedjson.Null && !(options.Kind == 0 && options.Literal == "") {
		return fmt.Errorf("compilerOptions must be an object")
	}
	for _, key := range options.Keys() {
		for _, owned := range OwnedCompilerOptions {
			if key != owned {
				continue
			}
			if owned == "rootDir" && !role {
				continue
			}
			where := "a base"
			if role {
				where = "a role"
			}
			return fmt.Errorf("compilerOptions %q is owned by Workbench and cannot be authored in %s", key, where)
		}
		if key == "references" {
			return fmt.Errorf("compilerOptions %q is not a compiler option", key)
		}
	}
	return nil
}

// ValidateBase checks one base.
func ValidateBase(name string, base TsconfigBase, bases map[string]TsconfigBase) error {
	if !baseNamePattern.MatchString(name) {
		return fmt.Errorf("base name %q is invalid", name)
	}
	if base.Directory != "." {
		if err := ValidatePackagePath(base.Directory); err != nil {
			return fmt.Errorf("base %q directory %q: %w", name, base.Directory, err)
		}
	}
	if base.Extends != nil {
		if _, exists := bases[*base.Extends]; !exists {
			return fmt.Errorf("base %q extends undeclared base %q", name, *base.Extends)
		}
		seen := map[string]struct{}{name: {}}
		for current := *base.Extends; ; {
			if _, cycle := seen[current]; cycle {
				return fmt.Errorf("base %q extends itself through %q", name, current)
			}
			seen[current] = struct{}{}
			next := bases[current].Extends
			if next == nil {
				break
			}
			current = *next
		}
	}
	if err := refuseOwnedCompilerOptions(base.CompilerOptions, false); err != nil {
		return fmt.Errorf("base %q: %w", name, err)
	}
	return nil
}

// ValidateTypeScriptLayout checks bases, base directories and standalone projects.
func (layout TypeScriptLayout) Validate() error {
	directories := map[string]string{}
	for _, name := range sortedKeys(layout.Bases) {
		base := layout.Bases[name]
		if err := ValidateBase(name, base, layout.Bases); err != nil {
			return err
		}
		if previous, exists := directories[base.Directory]; exists {
			return fmt.Errorf("bases %q and %q are both written to %q", previous, name, path.Join(base.Directory, "tsconfig.base.json"))
		}
		directories[base.Directory] = name
	}
	for _, projectPath := range sortedKeys(layout.Projects) {
		if err := ValidateProjectPath(projectPath); err != nil {
			return fmt.Errorf("project %q: %w", projectPath, err)
		}
		project := layout.Projects[projectPath]
		if err := ValidateRole(projectPath, project, nil, true); err != nil {
			return fmt.Errorf("project %q: %w", projectPath, err)
		}
		if project.Base != nil {
			if _, exists := layout.Bases[*project.Base]; !exists {
				return fmt.Errorf("project %q names undeclared base %q", projectPath, *project.Base)
			}
		}
	}
	return nil
}

// ValidateProjectPath checks a standalone project's tsconfig path.
func ValidateProjectPath(value string) error {
	if !strings.HasSuffix(value, ".json") {
		return fmt.Errorf("must name a .json file")
	}
	base := path.Base(value)
	if base == "tsconfig.base.json" || base == "package.json" {
		return fmt.Errorf("must not name %s", base)
	}
	if value == "tsconfig.json" {
		return fmt.Errorf("the root tsconfig.json is the generated solution")
	}
	if strings.HasPrefix(value, "/") || strings.Contains(value, "\\") {
		return fmt.Errorf("must be a relative slash-separated path")
	}
	for _, segment := range strings.Split(value, "/") {
		if segment == "" || segment == "." || segment == ".." || segment == "node_modules" {
			return fmt.Errorf("segment %q is not admitted", segment)
		}
	}
	return nil
}

// ValidatePackageRoles checks the 0.14 per-package fields.
func (policy PackagePolicy) ValidatePackageRoles() error {
	names := make(map[string]struct{}, len(policy.Roles))
	for name := range policy.Roles {
		names[name] = struct{}{}
	}
	for _, name := range SortedRoleNames(policy.Roles) {
		if err := ValidateRole(name, policy.Roles[name], names, false); err != nil {
			return fmt.Errorf("role %q: %w", name, err)
		}
	}
	if len(policy.Roles) != 0 && (len(policy.SourceRoots) != 0 || len(policy.SourceExcludes) != 0) {
		return fmt.Errorf("sourceRoots and sourceExcludes build only the default sources role; declare include and exclude on the roles instead")
	}
	if policy.Fields.Kind == orderedjson.Object {
		for _, key := range policy.Fields.Keys() {
			if ownedPackageField(key) {
				return fmt.Errorf("fields %q is owned by Workbench", key)
			}
		}
	}
	return nil
}

// OwnedPackageFields are the package.json keys Workbench renders itself.
var OwnedPackageFields = []string{"//", "name", "private", "type", "imports", "exports", "dependencies", "devDependencies", "peerDependencies", "optionalDependencies", "workspaces"}

func ownedPackageField(key string) bool {
	for _, owned := range OwnedPackageFields {
		if key == owned {
			return true
		}
	}
	return false
}

func sortedKeys[Value any](values map[string]Value) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

// ValidateRepositoryRelativePath accepts a confined relative file path.
func ValidateRepositoryRelativePath(value string) error {
	if value == "" || strings.HasPrefix(value, "/") || strings.Contains(value, "\\") {
		return fmt.Errorf("must be a relative slash-separated path")
	}
	for _, segment := range strings.Split(value, "/") {
		if segment == "" || segment == "." || segment == ".." {
			return fmt.Errorf("must not contain empty, . or .. segments")
		}
	}
	return nil
}

// InPlaceSubject is a Workbench whose context is the repository holding it.
type InPlaceSubject struct {
	Entrypoint string `json:"entrypoint"`
	Install    bool   `json:"install"`
}

// DecodeInPlaceSubject decodes and validates an evaluated InPlaceSubject.
func DecodeInPlaceSubject(encoded []byte) (InPlaceSubject, error) {
	var subject InPlaceSubject
	if err := decodeStrict(encoded, &subject); err != nil {
		return InPlaceSubject{}, fmt.Errorf("decode InPlaceSubject: %w", err)
	}
	if _, err := GitHubIdentity(subject.Entrypoint); err != nil {
		return InPlaceSubject{}, fmt.Errorf("InPlaceSubject entrypoint %q: %w", subject.Entrypoint, err)
	}
	return subject, nil
}

// GitHubRemoteIdentity normalizes an origin URL in HTTPS, SSH or scp form.
func GitHubRemoteIdentity(remote string) (string, error) {
	remote = strings.TrimSpace(remote)
	for _, prefix := range []string{"git@github.com:", "ssh://git@github.com/"} {
		if strings.HasPrefix(remote, prefix) {
			identity := strings.Trim(strings.TrimSuffix(strings.TrimPrefix(remote, prefix), ".git"), "/")
			if !githubNamePattern.MatchString(identity) {
				return "", fmt.Errorf("invalid GitHub repository %q", remote)
			}
			return strings.ToLower(identity), nil
		}
	}
	return GitHubIdentity(remote)
}
