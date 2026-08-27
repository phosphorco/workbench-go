package skills

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

var validDomains = map[string]struct{}{
	"engineering":   {},
	"general":       {},
	"orchestration": {},
}

var validMarkerKinds = map[string]struct{}{
	"boundary":  {},
	"contract":  {},
	"encounter": {},
	"request":   {},
}

type skillFrontmatter struct {
	Name        string
	Description string
	Domain      string
}

func parseFrontmatter(source string) (skillFrontmatter, string) {
	if !strings.HasPrefix(source, "---\n") {
		return skillFrontmatter{}, "missing YAML frontmatter"
	}
	end := strings.Index(source[4:], "\n---")
	if end < 0 {
		return skillFrontmatter{}, "missing YAML frontmatter"
	}
	var value struct {
		Name        string `yaml:"name"`
		Description string `yaml:"description"`
		Metadata    struct {
			Domain string `yaml:"domain"`
		} `yaml:"metadata"`
	}
	if err := yaml.Unmarshal([]byte(source[4:4+end]), &value); err != nil {
		return skillFrontmatter{}, "frontmatter is not valid YAML"
	}
	if value.Name == "" {
		return skillFrontmatter{}, "frontmatter must declare name and a valid metadata.domain"
	}
	if _, valid := validDomains[value.Metadata.Domain]; !valid {
		return skillFrontmatter{}, "frontmatter must declare name and a valid metadata.domain"
	}
	return skillFrontmatter{Name: value.Name, Description: value.Description, Domain: value.Metadata.Domain}, ""
}

func checkSkillMetadata(source Source, skillName string, files map[string][]byte, description string, report *Report) {
	selected := ""
	for _, metadataName := range []string{".skill-meta.yaml", ".skill-meta.yml"} {
		contents, exists := files[metadataName]
		if !exists {
			continue
		}
		path := filepath.ToSlash(filepath.Join(skillName, metadataName))
		if selected != "" {
			report.Warnings = append(report.Warnings, Diagnostic{Source: source.Name, Path: path, Message: "skill has both .skill-meta.yaml and .skill-meta.yml; keep one"})
			continue
		}
		selected = metadataName
		var value any
		if err := yaml.Unmarshal(contents, &value); err != nil {
			report.Issues = append(report.Issues, Diagnostic{Source: source.Name, Path: path, Message: "skill metadata is not valid YAML"})
			continue
		}
		object, valid := stringMap(value)
		if !valid {
			report.Issues = append(report.Issues, Diagnostic{Source: source.Name, Path: path, Message: "skill metadata must be an object"})
			continue
		}
		checkDescriptionMarkers(source, path, object, description, report)
	}
}

func checkDescriptionMarkers(source Source, path string, value map[string]any, description string, report *Report) {
	maintenanceValue, exists := value["maintenance"]
	if !exists {
		return
	}
	maintenance, valid := stringMap(maintenanceValue)
	if !valid {
		report.Issues = append(report.Issues, Diagnostic{Source: source.Name, Path: path + ":maintenance", Message: "maintenance must be an object"})
		return
	}
	markersValue, exists := maintenance["description-markers"]
	if !exists {
		return
	}
	markers, valid := markersValue.([]any)
	if !valid {
		report.Issues = append(report.Issues, Diagnostic{Source: source.Name, Path: path + ":maintenance.description-markers", Message: "maintenance.description-markers must be an array"})
		return
	}

	seen := make(map[string]struct{})
	normalizedDescription := strings.Join(strings.Fields(description), " ")
	for index, markerValue := range markers {
		markerPath := fmt.Sprintf("%s:maintenance.description-markers[%d]", path, index)
		marker, valid := stringMap(markerValue)
		if !valid {
			report.Issues = append(report.Issues, Diagnostic{Source: source.Name, Path: markerPath, Message: fmt.Sprintf("maintenance.description-markers[%d] must be an object", index)})
			continue
		}
		unknown := make([]string, 0)
		for key := range marker {
			if key != "text" && key != "kind" && key != "why" {
				unknown = append(unknown, key)
			}
		}
		sort.Strings(unknown)
		for _, key := range unknown {
			report.Issues = append(report.Issues, Diagnostic{Source: source.Name, Path: markerPath, Message: fmt.Sprintf("maintenance.description-markers[%d] has unknown field %q; allowed fields are text, kind, why", index, key)})
		}

		text, textExists := marker["text"]
		textString, textValid := text.(string)
		if !textExists {
			report.Issues = append(report.Issues, Diagnostic{Source: source.Name, Path: markerPath, Message: fmt.Sprintf("maintenance.description-markers[%d] is missing required field %q", index, "text")})
		} else if !textValid || strings.TrimSpace(textString) == "" {
			report.Issues = append(report.Issues, Diagnostic{Source: source.Name, Path: markerPath, Message: fmt.Sprintf("maintenance.description-markers[%d].text must be a non-empty string", index)})
		}

		kind, kindExists := marker["kind"]
		kindString, kindValid := kind.(string)
		_, knownKind := validMarkerKinds[kindString]
		if !kindExists {
			report.Issues = append(report.Issues, Diagnostic{Source: source.Name, Path: markerPath, Message: fmt.Sprintf("maintenance.description-markers[%d] is missing required field %q", index, "kind")})
		} else if !kindValid || !knownKind {
			report.Issues = append(report.Issues, Diagnostic{Source: source.Name, Path: markerPath, Message: fmt.Sprintf("maintenance.description-markers[%d].kind must be one of request, encounter, contract, boundary", index)})
		}

		why, whyExists := marker["why"]
		whyString, whyValid := why.(string)
		if !whyExists {
			report.Issues = append(report.Issues, Diagnostic{Source: source.Name, Path: markerPath, Message: fmt.Sprintf("maintenance.description-markers[%d] is missing required field %q", index, "why")})
		} else if !whyValid || strings.TrimSpace(whyString) == "" {
			report.Issues = append(report.Issues, Diagnostic{Source: source.Name, Path: markerPath, Message: fmt.Sprintf("maintenance.description-markers[%d].why must be a non-empty string", index)})
		}

		if !textValid || strings.TrimSpace(textString) == "" {
			continue
		}
		if _, duplicate := seen[textString]; duplicate {
			report.Warnings = append(report.Warnings, Diagnostic{Source: source.Name, Path: markerPath, Message: fmt.Sprintf("description marker text %q is duplicated within this skill", textString)})
		} else {
			seen[textString] = struct{}{}
		}
		if !strings.Contains(normalizedDescription, textString) {
			report.Warnings = append(report.Warnings, Diagnostic{Source: source.Name, Path: markerPath, Message: fmt.Sprintf("description marker text %q does not occur in the normalized frontmatter description", textString)})
		}
	}
}

func stringMap(value any) (map[string]any, bool) {
	result, valid := value.(map[string]any)
	return result, valid
}
