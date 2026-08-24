package evaluate

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"regexp"

	"github.com/apple/pkl-go/pkl"
	"github.com/phosphorco/workbench-go/internal/contract"
)

var amendsPattern = regexp.MustCompile(`^\s*amends\s+"([^"\r\n]+)"`)

// Contract designates the sole schema that source may amend.
type Contract struct {
	uri              *url.URL
	contents         string
	local            bool
	packageResources []string
}

// LocalContract grants evaluation access to exactly one in-memory schema module.
func LocalContract(uri, contents string) (Contract, error) {
	parsed, err := parseContractURI(uri)
	if err != nil {
		return Contract{}, err
	}
	if parsed.Scheme == "package" {
		return Contract{}, fmt.Errorf("local contract URI %q uses the reserved package scheme", uri)
	}
	if contents == "" {
		return Contract{}, fmt.Errorf("local contract %q is empty", uri)
	}
	return Contract{uri: parsed, contents: contents, local: true}, nil
}

// ReleasedContract designates one immutable package module.
func ReleasedContract(uri string) (Contract, error) {
	parsed, err := parseContractURI(uri)
	if err != nil {
		return Contract{}, err
	}
	if parsed.Scheme != "package" || parsed.Fragment == "" {
		return Contract{}, fmt.Errorf("released contract URI %q is not an immutable package module", uri)
	}
	resources, err := packageResourceURLs(parsed)
	if err != nil {
		return Contract{}, err
	}
	return Contract{uri: parsed, packageResources: resources}, nil
}

func EvaluateSubject(ctx context.Context, source []byte, schema Contract) (contract.Subject, error) {
	encoded, err := evaluateJSON(ctx, source, schema)
	if err != nil {
		return contract.Subject{}, fmt.Errorf("evaluate Subject: %w", err)
	}
	value, err := contract.DecodeSubject([]byte(encoded))
	if err != nil {
		return contract.Subject{}, fmt.Errorf("decode evaluated Subject: %w", err)
	}
	return value, nil
}

func EvaluatePackageScopeRepository(ctx context.Context, source []byte, schema Contract) (contract.PackageScopeRepository, error) {
	encoded, err := evaluateJSON(ctx, source, schema)
	if err != nil {
		return contract.PackageScopeRepository{}, fmt.Errorf("evaluate package-scope repository: %w", err)
	}
	value, err := contract.DecodePackageScopeRepository([]byte(encoded))
	if err != nil {
		return contract.PackageScopeRepository{}, fmt.Errorf("decode evaluated package-scope repository: %w", err)
	}
	return value, nil
}

func evaluateJSON(ctx context.Context, source []byte, schema Contract) (_ string, resultErr error) {
	if schema.uri == nil {
		return "", fmt.Errorf("contract is uninitialized")
	}
	if err := requireAmends(source, schema.uri.String()); err != nil {
		return "", err
	}

	allowedModules := []string{"^" + regexp.QuoteMeta(schema.uri.String()) + "$"}
	var readers []pkl.ModuleReader
	if schema.local {
		readers = append(readers, exactModuleReader{uri: *schema.uri, contents: schema.contents})
	}
	evaluator, err := pkl.NewEvaluator(ctx, func(options *pkl.EvaluatorOptions) {
		options.AllowedModules = allowedModules
		// Pkl's standard JSON renderer reads this evaluator-owned property.
		options.AllowedResources = exactResourcePatterns(schema.packageResources...)
		options.Env = map[string]string{}
		options.Properties = map[string]string{}
		options.ModuleReaders = readers
		options.OutputFormat = "json"
	})
	if err != nil {
		return "", fmt.Errorf("start capability-constrained Pkl evaluator: %w", err)
	}
	defer func() {
		if closeErr := evaluator.Close(); closeErr != nil {
			resultErr = errors.Join(resultErr, fmt.Errorf("close Pkl evaluator: %w", closeErr))
		}
	}()

	encoded, err := evaluator.EvaluateOutputText(ctx, pkl.TextSource(string(source)))
	if err != nil {
		return "", fmt.Errorf("evaluate Pkl module: %w", err)
	}
	return encoded, nil
}

func packageResourceURLs(packageModule *url.URL) ([]string, error) {
	if packageModule.Host == "" || packageModule.User != nil || packageModule.RawQuery != "" {
		return nil, fmt.Errorf("released contract URI %q does not have a capability-safe package authority", packageModule.String())
	}

	metadata := *packageModule
	metadata.Scheme = "https"
	metadata.Fragment = ""
	metadata.RawFragment = ""

	archive := metadata
	archive.Path += ".zip"
	if archive.RawPath != "" {
		archive.RawPath += ".zip"
	}
	return []string{metadata.String(), archive.String()}, nil
}

func exactResourcePatterns(resources ...string) []string {
	patterns := make([]string, 0, len(resources)+1)
	patterns = append(patterns, "^"+regexp.QuoteMeta("prop:pkl.outputFormat")+"$")
	for _, resource := range resources {
		patterns = append(patterns, "^"+regexp.QuoteMeta(resource)+"$")
	}
	return patterns
}

func parseContractURI(raw string) (*url.URL, error) {
	parsed, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("parse contract URI %q: %w", raw, err)
	}
	if parsed.Scheme == "" || parsed.Scheme == "file" || parsed.Scheme == "http" || parsed.Scheme == "https" || parsed.Scheme == "env" {
		return nil, fmt.Errorf("contract URI %q does not designate a capability-safe module", raw)
	}
	return parsed, nil
}

func requireAmends(source []byte, contractURI string) error {
	match := amendsPattern.FindSubmatch(source)
	if match == nil {
		return fmt.Errorf("source must begin by amending %q", contractURI)
	}
	if string(match[1]) != contractURI {
		return fmt.Errorf("source amends %q, not designated contract %q", match[1], contractURI)
	}
	return nil
}

type exactModuleReader struct {
	uri      url.URL
	contents string
}

func (reader exactModuleReader) Scheme() string     { return reader.uri.Scheme }
func (exactModuleReader) IsGlobbable() bool         { return false }
func (exactModuleReader) HasHierarchicalUris() bool { return true }
func (exactModuleReader) IsLocal() bool             { return false }
func (exactModuleReader) ListElements(url.URL) ([]pkl.PathElement, error) {
	return nil, nil
}

func (reader exactModuleReader) Read(request url.URL) (string, error) {
	if request.String() != reader.uri.String() {
		return "", fmt.Errorf("module %q is outside the designated contract", request.String())
	}
	return reader.contents, nil
}
