package evaluate

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

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

	allowedModules := contractModulePatterns(schema.uri)
	allowedResources, err := releasedResourcePatterns(ctx, schema.packageResources)
	if err != nil {
		return "", err
	}
	var readers []pkl.ModuleReader
	if schema.local {
		readers = append(readers, exactModuleReader{uri: *schema.uri, contents: schema.contents})
	}
	evaluator, err := pkl.NewEvaluator(ctx, func(options *pkl.EvaluatorOptions) {
		options.AllowedModules = allowedModules
		// Pkl's standard JSON renderer reads this evaluator-owned property.
		options.AllowedResources = allowedResources
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
	if packageModule.Host != "github.com" || packageModule.User != nil || packageModule.RawPath != "" || packageModule.RawQuery != "" || packageModule.ForceQuery {
		return nil, fmt.Errorf("released contract URI %q does not have a capability-safe package authority", packageModule.String())
	}
	parts := strings.Split(packageModule.Path, "/")
	if len(parts) != 7 || parts[0] != "" || parts[1] != "phosphorco" || parts[2] != "workbench-go" || parts[3] != "releases" || parts[4] != "download" || parts[5] == "" || parts[6] != "workbench@"+parts[5] {
		return nil, fmt.Errorf("released contract URI %q does not designate a phosphorco/workbench-go release", packageModule.String())
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

func contractModulePatterns(module *url.URL) []string {
	patterns := []string{"^" + regexp.QuoteMeta(module.String()) + "$"}
	if module.Scheme != "package" || module.Fragment == "" || strings.Contains(strings.TrimPrefix(module.Fragment, "/"), "/") {
		return patterns
	}
	backing := *module
	backing.Fragment = "/pkl/" + strings.TrimPrefix(module.Fragment, "/")
	backing.RawFragment = ""
	return append(patterns, "^"+regexp.QuoteMeta(backing.String())+"$")
}

func releasedResourcePatterns(ctx context.Context, resources []string) ([]string, error) {
	patterns := exactResourcePatterns(resources...)
	client := http.Client{
		Timeout: 15 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	for _, resource := range resources {
		request, err := http.NewRequestWithContext(ctx, http.MethodHead, resource, nil)
		if err != nil {
			return nil, fmt.Errorf("prepare released package transport observation %q: %w", resource, err)
		}
		response, err := client.Do(request)
		if err != nil {
			return nil, fmt.Errorf("observe released package transport %q: %w", resource, err)
		}
		closeErr := response.Body.Close()
		if closeErr != nil && !errors.Is(closeErr, io.EOF) {
			return nil, fmt.Errorf("close released package transport observation %q: %w", resource, closeErr)
		}
		if response.StatusCode < http.StatusMultipleChoices || response.StatusCode >= http.StatusBadRequest {
			continue
		}
		location, err := response.Location()
		if err != nil {
			return nil, fmt.Errorf("read released package redirect for %q: %w", resource, err)
		}
		pattern, err := releaseAssetRedirectPattern(resource, location.String())
		if err != nil {
			return nil, err
		}
		patterns = append(patterns, pattern)
	}
	return patterns, nil
}

func releaseAssetRedirectPattern(resource, location string) (string, error) {
	source, err := url.Parse(resource)
	if err != nil {
		return "", fmt.Errorf("parse released package resource %q: %w", resource, err)
	}
	target, err := url.Parse(location)
	if err != nil {
		return "", fmt.Errorf("parse released package redirect %q: %w", location, err)
	}
	if target.Scheme != "https" || target.Host != "release-assets.githubusercontent.com" || target.User != nil || target.Fragment != "" || target.RawPath != "" {
		return "", fmt.Errorf("released package resource %q redirected outside GitHub release assets: %q", resource, location)
	}
	assetParts := strings.Split(target.Path, "/")
	if len(assetParts) != 4 || assetParts[0] != "" || assetParts[1] != "github-production-release-asset" || !decimalPattern.MatchString(assetParts[2]) || !uuidPattern.MatchString(assetParts[3]) {
		return "", fmt.Errorf("released package resource %q redirected to an invalid GitHub release asset path: %q", resource, location)
	}
	queryPattern, err := signedReleaseQueryPattern(target.RawQuery, source.Path)
	if err != nil {
		return "", fmt.Errorf("released package resource %q redirected without a valid read-only signature: %w", resource, err)
	}
	return "^" + regexp.QuoteMeta("https://release-assets.githubusercontent.com"+target.Path+"?") + queryPattern + "$", nil
}

var (
	decimalPattern = regexp.MustCompile(`^[0-9]+$`)
	uuidPattern    = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
)

func signedReleaseQueryPattern(rawQuery, sourcePath string) (string, error) {
	values, err := url.ParseQuery(rawQuery)
	if err != nil {
		return "", fmt.Errorf("parse signed query: %w", err)
	}
	for key, want := range map[string]string{"sp": "r", "spr": "https", "sr": "b"} {
		if got := values[key]; len(got) != 1 || got[0] != want {
			return "", fmt.Errorf("query parameter %q does not grant exactly %q", key, want)
		}
	}
	for _, key := range []string{"sig", "jwt"} {
		if got := values[key]; len(got) != 1 || got[0] == "" {
			return "", fmt.Errorf("query parameter %q is absent or ambiguous", key)
		}
	}
	if got := values["response-content-disposition"]; len(got) != 1 || got[0] != "attachment; filename="+sourcePath[strings.LastIndex(sourcePath, "/")+1:] {
		return "", fmt.Errorf("response filename does not match the designated package resource")
	}

	components := strings.Split(rawQuery, "&")
	seen := make(map[string]struct{}, len(components))
	patterns := make([]string, 0, len(components))
	for _, component := range components {
		key, encodedValue, found := strings.Cut(component, "=")
		if !found || key == "" || encodedValue == "" {
			return "", fmt.Errorf("signed query contains an empty component")
		}
		decodedKey, err := url.QueryUnescape(key)
		if err != nil {
			return "", fmt.Errorf("decode query key %q: %w", key, err)
		}
		if _, duplicate := seen[decodedKey]; duplicate {
			return "", fmt.Errorf("signed query repeats parameter %q", decodedKey)
		}
		seen[decodedKey] = struct{}{}
		if decodedKey == "sp" || decodedKey == "spr" || decodedKey == "sr" {
			patterns = append(patterns, regexp.QuoteMeta(component))
		} else {
			patterns = append(patterns, regexp.QuoteMeta(key+"=")+`[^&#]+`)
		}
	}
	if len(seen) != len(values) {
		return "", fmt.Errorf("signed query representation is ambiguous")
	}
	return strings.Join(patterns, "&"), nil
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
