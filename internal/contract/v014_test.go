package contract

import (
	"strings"
	"testing"

	"github.com/phosphorco/workbench-go/internal/orderedjson"
)

func object(t *testing.T, source string) orderedjson.Value {
	t.Helper()
	value, err := orderedjson.Decode([]byte(source))
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func listing(values ...string) *[]string { return &values }

func TestValidateRoleRefusals(t *testing.T) {
	derive := true
	for name, test := range map[string]struct {
		role Role
		want string
	}{
		"owned composite in a role":           {Role{CompilerOptions: object(t, `{"composite": true}`)}, `"composite" is owned`},
		"owned outDir in a role":              {Role{CompilerOptions: object(t, `{"outDir": "x"}`)}, `"outDir" is owned`},
		"rootDir in a role":                   {Role{CompilerOptions: object(t, `{"rootDir": "."}`)}, `"rootDir" is owned`},
		"tsBuildInfoFile in a role":           {Role{CompilerOptions: object(t, `{"tsBuildInfoFile": "x"}`)}, `"tsBuildInfoFile" is owned`},
		"parent segment in include":           {Role{Include: listing("../other")}, "only declared foreignFiles may name a parent"},
		"undeclared foreign file":             {Role{Files: listing("../../@x/y/a.ts")}, "only declared foreignFiles"},
		"foreign file not in files":           {Role{ForeignFiles: map[string]string{"../a.ts": "@x/y"}}, "is not a files entry"},
		"glob in files":                       {Role{Files: listing("src/*.ts")}, "literal paths"},
		"template in a role include":          {Role{Include: listing("${configDir}/src")}, "template variables"},
		"references on a deriving role":       {Role{References: []string{"@x/y"}}, "cannot author references"},
		"explicitly deriving with references": {Role{DeriveReferences: &derive, References: []string{"@x/y"}}, "cannot author references"},
		"consumes itself":                     {Role{Consumes: []string{"sources"}}, "names the role itself"},
		"consumes an undeclared role":         {Role{Consumes: []string{"tools"}}, "undeclared role"},
	} {
		t.Run(name, func(t *testing.T) {
			if err := ValidateRole("sources", test.role, map[string]struct{}{"sources": {}, "test": {}}, false); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want %q", err, test.want)
			}
		})
	}
	if err := ValidateRole("test", Role{References: []string{"@x/y#tools", "z"}, Files: listing("../../@x/y/a.ts"), ForeignFiles: map[string]string{"../../@x/y/a.ts": "@x/y"}}, map[string]struct{}{"test": {}}, false); err != nil {
		t.Fatalf("authored role references and a declared foreign file: %v", err)
	}
	if err := ValidateRole("test", Role{References: []string{"@x/y#Tools"}}, map[string]struct{}{"test": {}}, false); err == nil {
		t.Fatal("a malformed role reference was admitted")
	}
}

func TestValidateBaseOwnershipAdmitsRootDir(t *testing.T) {
	bases := map[string]TsconfigBase{"root": {Directory: ".", CompilerOptions: object(t, `{"rootDir": "./", "strict": true}`)}}
	if err := (TypeScriptLayout{Bases: bases}).Validate(); err != nil {
		t.Fatalf("rootDir is an authored base option: %v", err)
	}
	for _, key := range []string{"composite", "outDir", "tsBuildInfoFile"} {
		bases := map[string]TsconfigBase{"root": {Directory: ".", CompilerOptions: object(t, `{"`+key+`": "x"}`)}}
		if err := (TypeScriptLayout{Bases: bases}).Validate(); err == nil || !strings.Contains(err.Error(), "owned by Workbench") {
			t.Fatalf("base %s: %v", key, err)
		}
	}
	if err := (TypeScriptLayout{Bases: map[string]TsconfigBase{"root": {Directory: ".", CompilerOptions: object(t, `{"allowJs": true}`)}}}).Validate(); err == nil {
		t.Fatal("allowJs in a base was admitted")
	}
	if err := (TypeScriptLayout{Bases: map[string]TsconfigBase{"root": {Directory: ".", Include: listing("${configDir}/**")}}}).Validate(); err == nil {
		t.Fatal("a base include ending in ** was admitted")
	}
	if err := (TypeScriptLayout{Bases: map[string]TsconfigBase{"root": {Directory: ".", CompilerOptions: object(t, `{"allowJs": false, "checkJs": false}`)}}}).Validate(); err != nil {
		t.Fatalf("explicitly disabled allowJs/checkJs: %v", err)
	}
	parent := "missing"
	if err := (TypeScriptLayout{Bases: map[string]TsconfigBase{"a": {Directory: "pkg", Extends: &parent}}}).Validate(); err == nil {
		t.Fatal("a base extending an undeclared base was admitted")
	}
	a, b := "b", "a"
	if err := (TypeScriptLayout{Bases: map[string]TsconfigBase{"a": {Directory: "x", Extends: &a}, "b": {Directory: "y", Extends: &b}}}).Validate(); err == nil || !strings.Contains(err.Error(), "extends itself") {
		t.Fatalf("base cycle: %v", err)
	}
	if err := (TypeScriptLayout{Bases: map[string]TsconfigBase{"a": {Directory: "x"}, "b": {Directory: "x"}}}).Validate(); err == nil {
		t.Fatal("two bases in one directory were admitted")
	}
	if err := (TypeScriptLayout{Projects: map[string]Role{"tsconfig.json": {}}}).Validate(); err == nil {
		t.Fatal("the root solution was admitted as a project")
	}
}

func TestValidatePackageRolesRefusals(t *testing.T) {
	for name, test := range map[string]struct {
		policy PackagePolicy
		want   string
	}{
		"roles with sourceRoots": {PackagePolicy{Roles: map[string]Role{"sources": {}}, SourceRoots: []string{"src"}}, "build only the default sources role"},
		"owned package field":    {PackagePolicy{Fields: object(t, `{"exports": {}}`)}, `fields "exports" is owned`},
		"workspaces field":       {PackagePolicy{Fields: object(t, `{"workspaces": []}`)}, `fields "workspaces" is owned`},
		"bad role name":          {PackagePolicy{Roles: map[string]Role{"Sources": {}}}, "not a lowercase identifier"},
	} {
		t.Run(name, func(t *testing.T) {
			if err := test.policy.ValidatePackageRoles(); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestEffectiveRolesDefaultsToSources(t *testing.T) {
	roles := PackagePolicy{SourceRoots: []string{"src", "__tests__"}, SourceExcludes: []string{"__tests__/fixtures"}}.EffectiveRoles()
	sources := roles[DefaultRole]
	if len(roles) != 1 || strings.Join(*sources.Include, ",") != "./src,./__tests__" || strings.Join(*sources.Exclude, ",") != "./__tests__/fixtures" || !sources.Derives(DefaultRole) {
		t.Fatalf("default role = %#v", roles)
	}
}

func TestGitHubRemoteIdentityAcceptsEveryOriginForm(t *testing.T) {
	for _, remote := range []string{"https://github.com/Phosphorco/Monorepo.git", "git@github.com:phosphorco/monorepo.git", "ssh://git@github.com/phosphorco/monorepo"} {
		if identity, err := GitHubRemoteIdentity(remote); err != nil || identity != "phosphorco/monorepo" {
			t.Fatalf("%s = %q %v", remote, identity, err)
		}
	}
	if _, err := GitHubRemoteIdentity("file:///tmp/x"); err == nil {
		t.Fatal("a file remote was admitted")
	}
}
