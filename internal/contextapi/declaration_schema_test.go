package contextapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os/exec"
	"regexp"
	"testing"

	"github.com/apple/pkl-go/pkl"
	contracts "github.com/phosphorco/workbench-go/pkl"
)

func TestProjectDeclarationEvaluatesThroughBundledSchema(t *testing.T) {
	encoded := evaluateBundledContext(t, contracts.ContextURI, contracts.Context, contracts.ContextTypesURI, contracts.ContextTypes, `amends "workbench:context"
contributors {
  ["project-guidance"] = new AiContext { enabled = false }
  ["profile-tool"] = new Executable {
    executable = "/opt/context-provider"
    arguments { "--direct"; "profile" }
    capabilities { "profile"; "contribute" }
    settings {
      [""] = "preserve-empty-key"
      ["enabled"] = true
      ["threshold"] = 1.5
      ["nothing"] = null
      ["nested"] = new {
        ["items"] { "first"; false; 7 }
      }
    }
  }
}
profile {
  role = "reviewer"
  preferences { new { name = "tone"; value = "terse" } }
}
`)

	project, err := DecodeProjectDeclaration([]byte(encoded))
	if err != nil {
		t.Fatal(err)
	}
	if !project.Enabled || project.Scope != DeclarationScopeSubtree || project.Profile.Role != "reviewer" || len(project.Profile.Preferences) != 1 {
		t.Fatalf("project defaults/profile = %#v", project)
	}
	preference := project.Profile.Preferences[0]
	if preference.Name != "tone" || preference.Value.Kind != FactText || preference.Value.Text != "terse" {
		t.Fatalf("profile preference = %#v", preference)
	}
	builtin := project.Contributors[ContributorName("project-guidance")]
	if builtin.Enabled || len(CapabilitiesForContributor(builtin)) != 0 {
		t.Fatalf("disabled builtin = %#v", builtin)
	}
	executable := project.Contributors[ContributorName("profile-tool")].Executable
	if executable.Executable != "/opt/context-provider" || len(executable.Arguments) != 2 || len(executable.Capabilities) != 2 {
		t.Fatalf("executable payload = %#v", executable)
	}
	var settings map[string]any
	if err := json.Unmarshal(executable.Settings, &settings); err != nil {
		t.Fatal(err)
	}
	if settings[""] != "preserve-empty-key" || settings["enabled"] != true || settings["threshold"] != 1.5 || settings["nothing"] != nil {
		t.Fatalf("settings payload was not preserved: %#v", settings)
	}
	nested, ok := settings["nested"].(map[string]any)
	if !ok {
		t.Fatalf("nested settings = %#v", settings["nested"])
	}
	items, ok := nested["items"].([]any)
	if !ok || len(items) != 3 || items[0] != "first" || items[1] != false || items[2] != float64(7) {
		t.Fatalf("nested settings items = %#v", nested["items"])
	}
}

func TestHomeDeclarationEvaluatesThroughBundledSchema(t *testing.T) {
	encoded := evaluateBundledContext(t, contracts.ContextHomeURI, contracts.ContextHome, contracts.ContextTypesURI, contracts.ContextTypes, `amends "workbench:context-home"
directories {
  new {
    root = "/workspace/project"
    scope = "directory"
    contributors { ["project-guidance"] = new AiContext {} }
  }
}
exclusions { new { root = "/workspace/project/private"; scope = "subtree" } }
limits {
  providerPolicy { mode = "allowlist"; allowed { "profile-tool" } }
  defaults { selection { role = "reviewer"; preferences { new { name = "tone"; value = "terse" } } } }
  delivery { maxRetainedBytes = 1024; queue { maxPendingItems = 2 } }
  runtime { wholeHookDeadlineMs = 500; providerDefaults { maxFacts = 3 } }
  cache { diskCapBytes = 4096; maxBlocks = 7 }
}
`)
	home, err := DecodeHomeDeclaration([]byte(encoded))
	if err != nil {
		t.Fatal(err)
	}
	if len(home.Directories) != 1 || home.Directories[0].Root != "/workspace/project" || home.Directories[0].Scope != DeclarationScopeDirectory || len(home.Directories[0].Contributors) != 1 {
		t.Fatalf("directory selection = %#v", home.Directories)
	}
	if len(home.Exclusions) != 1 || home.Exclusions[0].Root != "/workspace/project/private" || !home.Exclusions[0].IncludeChildren {
		t.Fatalf("exclusions = %#v", home.Exclusions)
	}
	if home.Limits.ProviderPolicy.Mode != ProviderPolicyAllowlist || len(home.Limits.ProviderPolicy.Allowed) != 1 || home.Limits.ProviderPolicy.Allowed[0] != "profile-tool" {
		t.Fatalf("home provider policy = %#v", home.Limits.ProviderPolicy)
	}
	if home.Limits.Defaults.Selection.Preferences[0].Value.Kind != FactText || home.Limits.Defaults.Selection.Preferences[0].Value.Text != "terse" {
		t.Fatalf("home profile defaults = %#v", home.Limits.Defaults)
	}
	if home.Limits.Delivery.MaxRetainedBytes != 1024 || home.Limits.Delivery.Queue.MaxPendingItems != 2 || home.Limits.Runtime.WholeHookDeadlineMs != 500 || home.Limits.Runtime.ProviderDefaults.MaxFacts != 3 || home.Limits.Cache.DiskCapBytes != 4096 || home.Limits.Cache.MaxBlocks != 7 {
		t.Fatalf("home limits lost fields = %#v", home.Limits)
	}
}

func TestDeclarationSchemasRejectInvalidScopeKindCapabilitiesAndHomeLimits(t *testing.T) {
	for name, test := range map[string]struct {
		uri    string
		schema string
		types  string
		source string
	}{
		"scope": {
			uri: contracts.ContextURI, schema: contracts.Context, types: contracts.ContextTypes,
			source: `amends "workbench:context"
scope = "invalid"
`,
		},
		"kind": {
			uri: contracts.ContextURI, schema: contracts.Context, types: contracts.ContextTypes,
			source: `amends "workbench:context"
contributors { ["bad"] = new AiContext { kind = "invalid" } }
`,
		},
		"capabilities": {
			uri: contracts.ContextURI, schema: contracts.Context, types: contracts.ContextTypes,
			source: `amends "workbench:context"
contributors { ["bad"] = new Executable { executable = "/opt/provider"; capabilities {} } }
`,
		},
		"project-limits": {
			uri: contracts.ContextURI, schema: contracts.Context, types: contracts.ContextTypes,
			source: `amends "workbench:context"
limits {}
`,
		},
		"project-runtime": {
			uri: contracts.ContextURI, schema: contracts.Context, types: contracts.ContextTypes,
			source: `amends "workbench:context"
runtime {}
`,
		},
		"project-cache": {
			uri: contracts.ContextURI, schema: contracts.Context, types: contracts.ContextTypes,
			source: `amends "workbench:context"
cache {}
`,
		},
		"home-limits": {
			uri: contracts.ContextHomeURI, schema: contracts.ContextHome, types: contracts.ContextTypes,
			source: `amends "workbench:context-home"
limits { cache { diskCapBytes = -1 } }
`,
		},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := runBundledContext(test.uri, test.schema, test.types, test.source); err == nil {
				t.Fatal("invalid declaration unexpectedly evaluated")
			}
		})
	}
}

func evaluateBundledContext(t *testing.T, schemaURI, schema, typesURI, types, source string) string {
	t.Helper()
	encoded, err := runBundledContext(schemaURI, schema, types, source)
	if err != nil {
		if errors.Is(err, exec.ErrNotFound) {
			t.Skipf("pkl is not installed: %v", err)
		}
		t.Fatal(err)
	}
	return encoded
}

func runBundledContext(schemaURI, schema, types, source string) (encoded string, resultErr error) {
	executable, err := exec.LookPath("pkl")
	if err != nil {
		return "", err
	}
	manager := pkl.NewEvaluatorManagerWithCommand([]string{executable})
	defer func() { resultErr = errors.Join(resultErr, manager.Close()) }()
	evaluator, err := manager.NewEvaluator(context.Background(), func(options *pkl.EvaluatorOptions) {
		options.AllowedModules = []string{
			"^" + regexp.QuoteMeta(contracts.ContextURI) + "$",
			"^" + regexp.QuoteMeta(contracts.ContextHomeURI) + "$",
			"^" + regexp.QuoteMeta(contracts.ContextTypesURI) + "$",
			`^pkl:[A-Za-z0-9]+$`,
		}
		options.AllowedResources = []string{`^prop:pkl.outputFormat$`}
		options.Env = map[string]string{}
		options.Properties = map[string]string{}
		options.ModuleReaders = []pkl.ModuleReader{
			bundledContextReader{modules: map[string]string{
				schemaURI:                 schema,
				contracts.ContextTypesURI: types,
			}},
		}
		options.OutputFormat = "json"
	})
	if err != nil {
		return "", err
	}
	if evaluator == nil {
		return "", errors.New("evaluator is nil")
	}
	defer func() { resultErr = errors.Join(resultErr, evaluator.Close()) }()
	encoded, err = evaluator.EvaluateOutputText(context.Background(), pkl.TextSource(source))
	if err != nil {
		return "", err
	}
	return encoded, nil
}

type bundledContextReader struct {
	modules map[string]string
}

func (reader bundledContextReader) Scheme() string     { return "workbench" }
func (bundledContextReader) IsGlobbable() bool         { return false }
func (bundledContextReader) HasHierarchicalUris() bool { return false }
func (bundledContextReader) IsLocal() bool             { return false }
func (bundledContextReader) ListElements(url.URL) ([]pkl.PathElement, error) {
	return nil, nil
}
func (reader bundledContextReader) Read(request url.URL) (string, error) {
	contents, ok := reader.modules[request.String()]
	if !ok {
		return "", fmt.Errorf("module %q is outside bundled declaration schemas", request.String())
	}
	return contents, nil
}
