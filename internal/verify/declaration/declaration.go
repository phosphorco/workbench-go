package declaration

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	evaluator "github.com/apple/pkl-go/pkl"
	contracts "github.com/phosphorco/workbench-go/pkl"
)

const Document = "workbench.pkl"

// Declaration retains evaluated manifest JSON in authored property order for version parity.
type Declaration struct {
	Repository contracts.VerifyRepository
	Manifests  map[string]json.RawMessage
}

type Registered struct {
	Manifest       contracts.Verifier
	ImageDigest    string
	ManifestDigest string
	Version        string
}

// Pkl designates the executable that evaluates declarations. The composition
// root grants an absolute path; AmbientPkl is for development only.
type Pkl struct{ executable string }

// NewPkl grants evaluation to one absolute Pkl executable.
func NewPkl(executable string) (Pkl, error) {
	if !filepath.IsAbs(executable) {
		return Pkl{}, fmt.Errorf("Pkl executable %q is not an absolute path", executable)
	}
	return Pkl{executable: filepath.Clean(executable)}, nil
}

// AmbientPkl resolves pkl through PATH at evaluation time.
func AmbientPkl() Pkl { return Pkl{executable: "pkl"} }

// Evaluate reads only supplied tracked Pkl bytes and the committed lock bytes.
// verifyHome designates the pre-populated offline package cache, never a checkout.
func Evaluate(ctx context.Context, pkl Pkl, files map[string][]byte, verifyHome string) (Declaration, error) {
	if _, ok := files[Document]; !ok {
		return Declaration{}, fmt.Errorf("%s is not tracked at the repository root", Document)
	}
	frozen, err := os.MkdirTemp("", "verify-pkl-")
	if err != nil {
		return Declaration{}, err
	}
	defer os.RemoveAll(frozen)
	frozen, err = filepath.EvalSymlinks(frozen)
	if err != nil {
		return Declaration{}, err
	}
	schema, err := os.MkdirTemp("", "verify-schema-")
	if err != nil {
		return Declaration{}, err
	}
	defer os.RemoveAll(schema)
	for name, source := range map[string]string{"Repository.pkl": contracts.RepositorySource, "Verifier.pkl": contracts.VerifierSource, "Focus.pkl": contracts.FocusSource} {
		if err := os.WriteFile(filepath.Join(schema, name), []byte(source), 0600); err != nil {
			return Declaration{}, err
		}
	}
	for name, source := range files {
		if !strings.HasSuffix(name, ".pkl") && name != "PklProject" && name != "PklProject.deps.json" {
			continue
		}
		if !validPath(name) {
			return Declaration{}, fmt.Errorf("invalid tracked Pkl path %q", name)
		}
		target := filepath.Join(frozen, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(target), 0700); err != nil {
			return Declaration{}, err
		}
		if err := os.WriteFile(target, source, 0600); err != nil {
			return Declaration{}, err
		}
	}
	modules := []string{"^" + regexp.QuoteMeta((&url.URL{Scheme: "file", Path: frozen + "/"}).String()), "^modulepath:/"}
	packages, err := loadPackages(files["PklProject.deps.json"], verifyHome)
	if err != nil {
		return Declaration{}, err
	}
	for _, uri := range packages.uris {
		modules = append(modules, "^"+regexp.QuoteMeta(uri)+"#/.+$")
	}
	cacheDir, removeCache, err := packages.stage(verifyHome)
	if err != nil {
		return Declaration{}, err
	}
	defer removeCache()
	manager := evaluator.NewEvaluatorManagerWithCommand([]string{"/usr/bin/env", "-i", "PATH=" + os.Getenv("PATH"), pkl.executable})
	defer manager.Close()
	configure := func(options *evaluator.EvaluatorOptions) {
		options.AllowedModules = modules
		options.AllowedResources = []string{`^prop:pkl\.outputFormat$`}
		options.ModulePaths = []string{schema}
		options.RootDir = frozen
		options.OutputFormat = "json"
		options.Env = map[string]string{"PATH": os.Getenv("PATH")}
		options.CacheDir = cacheDir
		options.Logger = evaluator.NoopLogger
	}
	ev, err := manager.NewEvaluator(ctx, configure)
	if err != nil {
		return Declaration{}, fmt.Errorf("create declaration evaluator: %w", err)
	}
	if ev == nil {
		return Declaration{}, ctx.Err()
	}
	defer ev.Close()
	if _, present := files["PklProject"]; present {
		loader, err := manager.NewEvaluator(ctx, configure, func(options *evaluator.EvaluatorOptions) {
			options.AllowedModules = append(append([]string{}, options.AllowedModules...), `^pkl:(Project|EvaluatorSettings)$`)
		})
		if err != nil {
			return Declaration{}, err
		}
		if loader == nil {
			return Declaration{}, ctx.Err()
		}
		defer loader.Close()
		project, err := evaluator.LoadProjectFromEvaluator(ctx, loader, evaluator.FileSource(filepath.Join(frozen, "PklProject")))
		if err != nil {
			return Declaration{}, fmt.Errorf("load tracked PklProject: %w", err)
		}
		deps := project.Dependencies()
		if len(deps.LocalDependencies) != 0 {
			return Declaration{}, fmt.Errorf("project dependencies must be remote pinned packages")
		}
		for name, dep := range deps.RemoteDependencies {
			metadata, _, err := PackageCachePaths(verifyHome, dep.PackageUri)
			if err != nil {
				return Declaration{}, err
			}
			content, ok := packages.files[metadata]
			if !ok {
				return Declaration{}, fmt.Errorf("PklProject dependency %s (%s) is not pinned in PklProject.deps.json", name, dep.PackageUri)
			}
			if dep.Checksums != nil && dep.Checksums.Sha256 != sha(content) {
				return Declaration{}, fmt.Errorf("PklProject dependency %s checksum differs from lock", name)
			}
		}
		projectEv, err := manager.NewEvaluator(ctx, configure, func(options *evaluator.EvaluatorOptions) {
			options.ProjectBaseURI = (&url.URL{Scheme: "file", Path: frozen}).String()
			options.DeclaredProjectDependencies = deps
			for _, uri := range packages.uris {
				options.AllowedModules = append(options.AllowedModules, "^"+regexp.QuoteMeta(strings.Replace(uri, "package:", "projectpackage:", 1))+"#/.+$")
			}
		})
		if err != nil {
			return Declaration{}, fmt.Errorf("create project evaluator: %w", err)
		}
		if projectEv == nil {
			return Declaration{}, ctx.Err()
		}
		defer projectEv.Close()
		ev = projectEv
	}
	output, err := ev.EvaluateOutputText(ctx, evaluator.FileSource(filepath.Join(frozen, Document)))
	if err != nil {
		return Declaration{}, fmt.Errorf("evaluate declaration: %w", err)
	}
	return Decode([]byte(output))
}

func validPath(name string) bool {
	return name != "." && !strings.ContainsAny(name, "\\\x00\r\n") && !strings.HasPrefix(name, "/") && path.Clean(name) == name && !strings.HasPrefix(name, "../") && name != ".."
}

// Decode retains field order while decoding the shared schema's typed policy.
func Decode(data []byte) (Declaration, error) {
	repository, err := contracts.DecodeVerifyRepository(data)
	if err != nil {
		return Declaration{}, err
	}
	var raw struct {
		Verifiers map[string]json.RawMessage `json:"verifiers"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return Declaration{}, err
	}
	return Declaration{Repository: repository, Manifests: raw.Verifiers}, nil
}

// Register hashes JSON.stringify-compatible bytes, including authored object key order.
func Register(data json.RawMessage) (Registered, error) {
	manifest, err := contracts.DecodeVerifier(data)
	if err != nil {
		return Registered{}, err
	}
	canonical, err := stringify(data)
	if err != nil {
		return Registered{}, err
	}
	image := manifest.Image[strings.LastIndex(manifest.Image, "@")+1:]
	digest := sha(canonical)
	return Registered{Manifest: manifest, ImageDigest: image, ManifestDigest: digest, Version: sha([]byte(image + "\x00" + digest))}, nil
}

func sha(data []byte) string { sum := sha256.Sum256(data); return hex.EncodeToString(sum[:]) }

// Order traverses sorted IDs and upstream IDs, refusing cycles with their traversal path.
func Order(declaration Declaration) ([]Registered, error) {
	ordered := []Registered{}
	state := map[string]string{}
	var visit func(string, []string) error
	visit = func(id string, trail []string) error {
		if state[id] == "done" {
			return nil
		}
		if state[id] == "visiting" {
			return fmt.Errorf("requires cycle: %s", strings.Join(append(trail, id), " -> "))
		}
		manifest, ok := declaration.Repository.Verifiers[id]
		if !ok {
			return fmt.Errorf("requires unregistered verifier: %s", id)
		}
		state[id] = "visiting"
		upstream := make([]string, 0, len(manifest.Requires))
		for _, value := range manifest.Requires {
			upstream = append(upstream, value)
		}
		sort.Strings(upstream)
		for _, value := range upstream {
			if err := visit(value, append(append([]string{}, trail...), id)); err != nil {
				return err
			}
		}
		registered, err := Register(declaration.Manifests[id])
		if err != nil {
			return fmt.Errorf("register %s: %w", id, err)
		}
		state[id] = "done"
		ordered = append(ordered, registered)
		return nil
	}
	ids := make([]string, 0, len(declaration.Repository.Verifiers))
	for id := range declaration.Repository.Verifiers {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		if err := visit(id, nil); err != nil {
			return nil, err
		}
	}
	return ordered, nil
}
