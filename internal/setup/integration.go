package setup

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/phosphorco/workbench-go/internal/buildable"
	"github.com/phosphorco/workbench-go/internal/contract"
	"github.com/phosphorco/workbench-go/internal/orientation"
	"github.com/phosphorco/workbench-go/internal/orphan"
	"github.com/phosphorco/workbench-go/internal/repositoryclosure"
)

func encodeBuildableProjection(resources []Resource, declarations map[string]contract.Declaration, sourceRoots map[string]string) ([]byte, error) {
	owners := make([]buildable.ProjectionOwner, 0, len(resources))
	for _, resource := range resources {
		root, exists := sourceRoots[resource.Identity]
		if !exists {
			return nil, fmt.Errorf("buildable projection source for %q is absent", resource.Identity)
		}
		source, err := os.ReadFile(filepath.Join(root, "workbench.pkl"))
		if err != nil {
			return nil, fmt.Errorf("read %q workbench.pkl for buildable projection: %w", resource.Identity, err)
		}
		owners = append(owners, buildable.ProjectionOwner{
			Identity: resource.Identity, RepositoryPath: resource.CanonicalPath, Source: source,
			Buildables: declarations[resource.GitHub].Buildables,
		})
	}
	return buildable.EncodeProjection(owners)
}

func legacyResources(value repositoryclosure.Closure) []Resource {
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

func declaredResources(value repositoryclosure.DeclaredClosure) []Resource {
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

func reportOrphans(root string, current []Resource, previous managedCheckoutReceipt) ([]orphan.Candidate, error) {
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

func projectOrientation(ctx context.Context, root string, subject contract.Subject, resources []Resource, evaluator versionedEvaluator, contractVersion string) (bool, error) {
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
	if version != contractVersion {
		return false, fmt.Errorf("AGENTS.pkl uses Workbench contract %s, want exact %s", version, contractVersion)
	}

	combined := append([]byte(nil), source...)
	combined = append(combined, explicitAgentFacts(subject, resources)...)
	instructions, err := evaluator.EvaluateAgentInstructions(ctx, combined, schema)
	if err != nil {
		return false, fmt.Errorf("evaluate tracked AGENTS.pkl with explicit participating-repository facts: %w", err)
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
	output.WriteString("}\ngeneratedPaths { \".agents/skills\"; \".workbench/buildables.json\"; \".workbench/managed-checkouts.json\"; \"AGENTS.md\"; \"bun.lock\"; \"node_modules\"; \"package.json\"; \"tsconfig.json\" }\n")
	output.WriteString("handOwnedPaths { \"AGENTS.pkl\"; \"workbench-subject.pkl\"")
	for _, resource := range sorted {
		fmt.Fprintf(&output, "; %s", strconv.Quote(resource.CanonicalPath))
		fmt.Fprintf(&output, "; %s", strconv.Quote(filepath.ToSlash(filepath.Join(resource.CanonicalPath, "skills"))))
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
