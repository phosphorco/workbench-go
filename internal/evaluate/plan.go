package evaluate

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/apple/pkl-go/pkl"
	"github.com/phosphorco/workbench-go/internal/plan"
	contracts "github.com/phosphorco/workbench-go/pkl"
)

// EvaluatePlan grants imports only within the explicitly selected module root.
// Resources, environment, network and external readers are unavailable.
func (runtime Evaluator) EvaluatePlan(ctx context.Context, filename, moduleRoot string) (_ plan.Definition, resultErr error) {
	if err := ctx.Err(); err != nil {
		return plan.Definition{}, err
	}
	if runtime.pklExecutable == "" {
		return plan.Definition{}, fmt.Errorf("Pkl evaluator is uninitialized")
	}
	relative, err := filepath.Rel(moduleRoot, filename)
	if err != nil || !filepath.IsLocal(relative) {
		return plan.Definition{}, fmt.Errorf("plan %q is outside module root %q", filename, moduleRoot)
	}
	root, err := os.OpenRoot(moduleRoot)
	if err != nil {
		return plan.Definition{}, fmt.Errorf("open plan module root: %w", err)
	}
	defer func() { resultErr = errors.Join(resultErr, root.Close()) }()
	manager := pkl.NewEvaluatorManagerWithCommand([]string{runtime.pklExecutable})
	defer func() { resultErr = errors.Join(resultErr, manager.Close()) }()
	schemaURI, _ := url.Parse("workbench:plan")
	// pkl-go starts an unjoined sender in NewEvaluator. Cancellation there can
	// race manager shutdown; finish the handshake before observing cancellation.
	evaluator, err := manager.NewEvaluator(context.WithoutCancel(ctx), func(options *pkl.EvaluatorOptions) {
		options.AllowedModules = []string{`^workbench:plan$`, `^plan-local:/`, `^pkl:[A-Za-z0-9]+$`}
		options.AllowedResources = []string{`^prop:pkl.outputFormat$`}
		options.Env = map[string]string{}
		options.Properties = map[string]string{}
		options.ModuleReaders = []pkl.ModuleReader{exactModuleReader{uri: *schemaURI, contents: contracts.Plan}, planModuleReader{root: root}}
		options.OutputFormat = "json"
	})
	if err != nil {
		return plan.Definition{}, fmt.Errorf("start plan evaluator: %w", err)
	}
	if evaluator == nil {
		return plan.Definition{}, fmt.Errorf("start plan evaluator: %w", ctx.Err())
	}
	defer func() { resultErr = errors.Join(resultErr, evaluator.Close()) }()
	if err := ctx.Err(); err != nil {
		return plan.Definition{}, err
	}
	uri := url.URL{Scheme: "plan-local", Path: "/" + filepath.ToSlash(relative)}
	encoded, err := evaluator.EvaluateOutputText(ctx, pkl.UriSource(uri.String()))
	if cancelErr := ctx.Err(); cancelErr != nil {
		return plan.Definition{}, cancelErr
	}
	if err != nil {
		return plan.Definition{}, fmt.Errorf("evaluate %s: %w", filename, err)
	}
	return plan.Decode([]byte(encoded))
}

type planModuleReader struct{ root *os.Root }

func (planModuleReader) Scheme() string            { return "plan-local" }
func (planModuleReader) IsGlobbable() bool         { return false }
func (planModuleReader) HasHierarchicalUris() bool { return true }
func (planModuleReader) IsLocal() bool             { return true }
func (planModuleReader) ListElements(url.URL) ([]pkl.PathElement, error) {
	return nil, fmt.Errorf("plan imports must name explicit modules")
}
func (r planModuleReader) Read(uri url.URL) (string, error) {
	name := strings.TrimPrefix(uri.Path, "/")
	if uri.Host != "" || uri.RawQuery != "" || uri.Fragment != "" || !filepath.IsLocal(name) || !strings.HasSuffix(name, ".pkl") {
		return "", fmt.Errorf("plan import %q must name a .pkl file within the module root", uri.String())
	}
	data, err := r.root.ReadFile(name)
	if err != nil {
		return "", fmt.Errorf("read plan import %q: %w", name, err)
	}
	return string(data), nil
}
