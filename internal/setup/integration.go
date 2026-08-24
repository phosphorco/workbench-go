package setup

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/phosphorco/workbench-go/internal/contract"
	"github.com/phosphorco/workbench-go/internal/orientation"
	"github.com/phosphorco/workbench-go/internal/orphan"
	"github.com/phosphorco/workbench-go/internal/world"
)

func legacyResources(value world.World) []Resource {
	resources := make([]Resource, 0, len(value.Resources))
	for _, resource := range value.Resources {
		includes := make([]contract.SkillPolicy, 0, len(resource.Declaration.Includes))
		for _, include := range resource.Declaration.Includes {
			if include.Skills != nil {
				includes = append(includes, *include.Skills)
			}
		}
		resources = append(resources, Resource{
			Identity:      resource.Designation,
			GitHub:        resource.Identity,
			CanonicalPath: resource.CanonicalPath,
			Shape:         contract.ResourceShape{Kind: contract.PackageScopeShape, Scope: resource.Designation},
			Packages:      resource.Declaration.Packages,
			Includes:      includes,
		})
	}
	return resources
}

func declaredResources(value world.DeclaredWorld) []Resource {
	resources := make([]Resource, 0, len(value.Resources))
	for _, resource := range value.Resources {
		includes := make([]contract.SkillPolicy, 0, len(resource.Declaration.Includes))
		for _, include := range resource.Declaration.Includes {
			if include.Skills != nil {
				includes = append(includes, *include.Skills)
			}
		}
		resources = append(resources, Resource{
			Identity: resource.Identity, GitHub: resource.GitHub,
			CanonicalPath: resource.CanonicalPath, Shape: resource.Declaration.Shape,
			Packages: resource.Declaration.Packages, Includes: includes,
		})
	}
	return resources
}

type worldReceipt struct {
	Version   int               `json:"version"`
	Resources []receiptResource `json:"resources"`
}

type receiptResource struct {
	Identity           string                 `json:"identity"`
	GitHub             string                 `json:"github"`
	Shape              contract.ResourceShape `json:"shape"`
	CanonicalPath      string                 `json:"canonicalPath"`
	CreatedByWorkbench bool                   `json:"createdByWorkbench"`
}

// ManagedCheckout is durable evidence that setup either created or merely
// adopted a canonical checkout. Prune may consider only CreatedByWorkbench;
// it must still prove every live Git recoverability predicate independently.
type ManagedCheckout struct {
	Identity           string
	GitHub             string
	Shape              contract.ResourceShape
	CanonicalPath      string
	CreatedByWorkbench bool
}

// ReadManagedCheckouts exposes provenance without deletion authority.
func ReadManagedCheckouts(root string) ([]ManagedCheckout, error) {
	receipt, err := readWorldReceipt(root)
	if err != nil {
		return nil, err
	}
	result := make([]ManagedCheckout, 0, len(receipt.Resources))
	for _, resource := range receipt.Resources {
		result = append(result, ManagedCheckout{
			Identity: resource.Identity, GitHub: resource.GitHub, Shape: resource.Shape,
			CanonicalPath: resource.CanonicalPath, CreatedByWorkbench: resource.CreatedByWorkbench,
		})
	}
	return result, nil
}

func readWorldReceipt(root string) (worldReceipt, error) {
	encoded, err := os.ReadFile(filepath.Join(root, ".workbench", "world.json"))
	if errors.Is(err, os.ErrNotExist) {
		return worldReceipt{Version: 1}, nil
	}
	if err != nil {
		return worldReceipt{}, fmt.Errorf("read Workbench World receipt: %w", err)
	}
	var receipt worldReceipt
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&receipt); err != nil {
		return worldReceipt{}, fmt.Errorf("decode Workbench World receipt: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return worldReceipt{}, fmt.Errorf("decode Workbench World receipt: trailing JSON value")
		}
		return worldReceipt{}, fmt.Errorf("decode Workbench World receipt trailing data: %w", err)
	}
	if receipt.Version != 1 {
		return worldReceipt{}, fmt.Errorf("unsupported Workbench World receipt version %d", receipt.Version)
	}
	identities := make(map[string]struct{}, len(receipt.Resources))
	paths := make(map[string]struct{}, len(receipt.Resources))
	for index, resource := range receipt.Resources {
		declaration := contract.Declaration{Shape: resource.Shape}
		identity, identityErr := declaration.Identity(resource.GitHub)
		canonicalPath, pathErr := declaration.CanonicalPath(resource.GitHub)
		normalizedGitHub, githubErr := contract.NormalizeGitHubRepository(resource.GitHub)
		if identityErr != nil || pathErr != nil || githubErr != nil || normalizedGitHub != resource.GitHub || identity != resource.Identity || canonicalPath != resource.CanonicalPath {
			return worldReceipt{}, fmt.Errorf("Workbench World receipt resource %d disagrees with closed shape identity or placement", index)
		}
		if _, duplicate := identities[resource.Identity]; duplicate {
			return worldReceipt{}, fmt.Errorf("Workbench World receipt duplicates identity %q", resource.Identity)
		}
		if _, duplicate := paths[resource.CanonicalPath]; duplicate {
			return worldReceipt{}, fmt.Errorf("Workbench World receipt duplicates canonical path %q", resource.CanonicalPath)
		}
		identities[resource.Identity] = struct{}{}
		paths[resource.CanonicalPath] = struct{}{}
	}
	return receipt, nil
}

func writeWorldReceipt(root string, resources []Resource, previous worldReceipt, created map[string]bool) (bool, error) {
	previousOwnership := make(map[string]bool, len(previous.Resources))
	for _, resource := range previous.Resources {
		previousOwnership[resource.Identity] = resource.CreatedByWorkbench
	}
	receipt := worldReceipt{Version: 1, Resources: make([]receiptResource, 0, len(resources))}
	current := make(map[string]struct{}, len(resources))
	for _, resource := range resources {
		current[resource.Identity] = struct{}{}
		receipt.Resources = append(receipt.Resources, receiptResource{
			Identity: resource.Identity, GitHub: resource.GitHub, Shape: resource.Shape,
			CanonicalPath:      resource.CanonicalPath,
			CreatedByWorkbench: previousOwnership[resource.Identity] || created[resource.Identity],
		})
	}
	// Keep retired entries until an explicit prune action removes its checkout
	// and provenance together. Ordinary setup has neither authority.
	for _, resource := range previous.Resources {
		if _, member := current[resource.Identity]; !member {
			receipt.Resources = append(receipt.Resources, resource)
		}
	}
	sort.Slice(receipt.Resources, func(i, j int) bool { return receipt.Resources[i].Identity < receipt.Resources[j].Identity })
	encoded, err := json.MarshalIndent(receipt, "", "  ")
	if err != nil {
		return false, fmt.Errorf("encode Workbench World receipt: %w", err)
	}
	encoded = append(encoded, '\n')
	return writeWholeOutput(filepath.Join(root, ".workbench", "world.json"), encoded)
}

func reportOrphans(root string, current []Resource, previous worldReceipt) ([]orphan.Candidate, error) {
	members := make([]orphan.Resource, 0, len(current))
	currentIdentities := make(map[string]struct{}, len(current))
	for _, resource := range current {
		currentIdentities[resource.Identity] = struct{}{}
		members = append(members, orphan.Resource{Identity: resource.Identity, GitHub: resource.GitHub, Shape: resource.Shape, CanonicalPath: resource.CanonicalPath})
	}
	candidates := make([]orphan.Candidate, 0)
	for _, resource := range previous.Resources {
		if _, exists := currentIdentities[resource.Identity]; exists {
			continue
		}
		path := filepath.Join(root, filepath.FromSlash(resource.CanonicalPath))
		info, err := os.Stat(path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("observe possible orphan %q: %w", resource.Identity, err)
		}
		if !info.IsDir() {
			return nil, fmt.Errorf("possible orphan path %q is not a directory", resource.CanonicalPath)
		}
		candidates = append(candidates, orphan.Candidate{Identity: resource.Identity, GitHub: resource.GitHub, Shape: resource.Shape, CanonicalPath: resource.CanonicalPath, Path: path})
	}
	report, err := orphan.Report(members, candidates)
	if err != nil {
		return nil, fmt.Errorf("report orphaned checkouts: %w", err)
	}
	return report.Orphans, nil
}

func projectOrientation(ctx context.Context, root string, subject contract.Subject, resources []Resource, evaluator v020Evaluator) (bool, error) {
	path := filepath.Join(root, "AGENTS.pkl")
	source, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("read tracked AGENTS.pkl: %w", err)
	}
	schema, version, err := schemaForSource(source, "AgentInstructions.pkl")
	if err != nil {
		return false, fmt.Errorf("select AGENTS.pkl contract: %w", err)
	}
	if version != "0.2.0" {
		return false, fmt.Errorf("AGENTS.pkl uses Workbench contract %s, want exact 0.2.0", version)
	}

	combined := append([]byte(nil), source...)
	combined = append(combined, explicitAgentFacts(subject, resources)...)
	instructions, err := evaluator.EvaluateAgentInstructions(ctx, combined, schema)
	if err != nil {
		return false, fmt.Errorf("evaluate tracked AGENTS.pkl with explicit World facts: %w", err)
	}
	output, err := orientation.Render(instructions)
	if err != nil {
		return false, err
	}
	return writeWholeOutput(filepath.Join(root, "AGENTS.md"), output)
}

func explicitAgentFacts(subject contract.Subject, resources []Resource) []byte {
	var output strings.Builder
	output.WriteString("\n\n// Facts below are supplied deterministically by Workbench.\n")
	fmt.Fprintf(&output, "subject { workLine { branch = %s; baseBranch = %s }; entrypoints {", strconv.Quote(subject.WorkLine.Branch), strconv.Quote(subject.WorkLine.BaseBranch))
	entrypoints := append([]string(nil), subject.Entrypoints...)
	sort.Strings(entrypoints)
	for _, entrypoint := range entrypoints {
		fmt.Fprintf(&output, " %s", strconv.Quote(entrypoint))
	}
	output.WriteString(" } }\nresources {\n")
	sorted := append([]Resource(nil), resources...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Identity < sorted[j].Identity })
	for _, resource := range sorted {
		fmt.Fprintf(&output, "  new { identity = %s; github = %s; ", strconv.Quote(resource.Identity), strconv.Quote(resource.GitHub))
		if resource.Shape.Kind == contract.PackageScopeShape {
			fmt.Fprintf(&output, "shape = new PackageScopeShape { scope = %s }; ", strconv.Quote(resource.Shape.Scope))
		} else {
			output.WriteString("shape = new RepositoryShape {}; ")
		}
		fmt.Fprintf(&output, "canonicalPath = %s; branch = %s; health = \"healthy\" }\n", strconv.Quote(resource.CanonicalPath), strconv.Quote(subject.WorkLine.Branch))
	}
	output.WriteString("}\ngeneratedPaths { \".agents/skills\"; \".workbench/world.json\"; \"AGENTS.md\"; \"bun.lock\"; \"node_modules\"; \"package.json\"; \"tsconfig.json\" }\n")
	output.WriteString("handOwnedPaths { \"AGENTS.pkl\"; \"workbench-subject.pkl\"")
	for _, resource := range sorted {
		fmt.Fprintf(&output, "; %s", strconv.Quote(resource.CanonicalPath))
	}
	output.WriteString(" }\n")
	return []byte(output.String())
}

func writeWholeOutput(path string, contents []byte) (bool, error) {
	existing, err := os.ReadFile(path)
	if err == nil && bytes.Equal(existing, contents) {
		return false, nil
	}
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return false, fmt.Errorf("read generated output %q: %w", path, err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return false, fmt.Errorf("create generated output parent: %w", err)
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), ".workbench-output-*")
	if err != nil {
		return false, fmt.Errorf("create generated output temporary file: %w", err)
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if _, err := temporary.Write(contents); err != nil {
		temporary.Close()
		return false, fmt.Errorf("write generated output temporary file: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return false, fmt.Errorf("sync generated output temporary file: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return false, fmt.Errorf("close generated output temporary file: %w", err)
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return false, fmt.Errorf("replace generated output %q: %w", path, err)
	}
	return true, nil
}
