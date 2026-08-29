package contract

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"regexp"
	"sort"
	"strings"
)

var (
	packageScopePattern = regexp.MustCompile(`^@[a-z0-9][a-z0-9._-]*$`)
	githubNamePattern   = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`)
)

type Subject struct {
	WorkLine    WorkLine `json:"workLine"`
	Entrypoints []string `json:"entrypoints"`
}

type WorkLine struct {
	Branch     string `json:"branch"`
	BaseBranch string `json:"baseBranch"`
}

type PackageScopeRepository struct {
	Scope    string                   `json:"scope"`
	Includes map[string]Include       `json:"includes"`
	Packages map[string]PackagePolicy `json:"packages"`
}

type Include struct {
	GitHub string       `json:"github"`
	Skills *SkillPolicy `json:"skills,omitempty"`
}

type SkillPolicy struct {
	Editing   *SkillSelection `json:"editing,omitempty"`
	Workbench *SkillSelection `json:"workbench,omitempty"`
}

type SkillSelection struct {
	All     bool
	Domains []string
	Names   []string
}

type PackagePolicy struct {
	Dependencies             map[string]string `json:"dependencies"`
	DevDependencies          map[string]string `json:"devDependencies"`
	RequiredButNotReferenced map[string]string `json:"requiredButNotReferenced"`
	PeerDependencies         map[string]string `json:"peerDependencies"`
	OptionalDependencies     map[string]string `json:"optionalDependencies"`
	Imports                  map[string]string `json:"imports"`
	Exports                  map[string]string `json:"exports"`
}

func (selection *SkillSelection) UnmarshalJSON(encoded []byte) error {
	if bytes.Equal(encoded, []byte(`"all"`)) {
		*selection = SkillSelection{All: true}
		return nil
	}
	var roots struct {
		Domains []string `json:"domains"`
		Names   []string `json:"names"`
	}
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&roots); err != nil {
		return fmt.Errorf("decode skill roots: %w", err)
	}
	selection.All = false
	selection.Domains = sortedUnique(roots.Domains)
	selection.Names = sortedUnique(roots.Names)
	return nil
}

func DecodeSubject(encoded []byte) (Subject, error) {
	var subject Subject
	if err := decodeStrict(encoded, &subject); err != nil {
		return Subject{}, fmt.Errorf("decode Subject: %w", err)
	}
	if err := subject.Validate(); err != nil {
		return Subject{}, err
	}
	return subject, nil
}

func DecodePackageScopeRepository(encoded []byte) (PackageScopeRepository, error) {
	var repository PackageScopeRepository
	if err := decodeStrict(encoded, &repository); err != nil {
		return PackageScopeRepository{}, fmt.Errorf("decode package-scope repository: %w", err)
	}
	if err := repository.Validate(); err != nil {
		return PackageScopeRepository{}, err
	}
	return repository, nil
}

func (subject Subject) Validate() error {
	if strings.TrimSpace(subject.WorkLine.Branch) == "" {
		return fmt.Errorf("Subject workLine.branch is empty")
	}
	if strings.TrimSpace(subject.WorkLine.BaseBranch) == "" {
		return fmt.Errorf("Subject workLine.baseBranch is empty")
	}
	if len(subject.Entrypoints) == 0 {
		return fmt.Errorf("Subject entrypoints is empty")
	}
	for _, entrypoint := range subject.Entrypoints {
		if _, err := GitHubIdentity(entrypoint); err != nil {
			return fmt.Errorf("Subject entrypoint %q: %w", entrypoint, err)
		}
	}
	return nil
}

func (repository PackageScopeRepository) Validate() error {
	if !packageScopePattern.MatchString(repository.Scope) {
		return fmt.Errorf("repository scope %q is not a package scope", repository.Scope)
	}
	for designation, include := range repository.Includes {
		if !packageScopePattern.MatchString(designation) {
			return fmt.Errorf("include designation %q is not a package scope", designation)
		}
		if !githubNamePattern.MatchString(include.GitHub) {
			return fmt.Errorf("include %q GitHub repository %q is invalid", designation, include.GitHub)
		}
	}
	return nil
}

func GitHubIdentity(designation string) (string, error) {
	parsed, err := url.Parse(designation)
	if err != nil {
		return "", fmt.Errorf("parse GitHub URL: %w", err)
	}
	if parsed.Scheme != "https" || parsed.Host != "github.com" || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", fmt.Errorf("expected an HTTPS github.com repository URL")
	}
	identity := strings.Trim(strings.TrimSuffix(parsed.Path, ".git"), "/")
	if !githubNamePattern.MatchString(identity) {
		return "", fmt.Errorf("invalid GitHub repository path %q", parsed.Path)
	}
	return strings.ToLower(identity), nil
}

func (repository PackageScopeRepository) CanonicalPath() string {
	return "pkg/" + repository.Scope
}

func decodeStrict(encoded []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return fmt.Errorf("trailing JSON value")
		}
		return fmt.Errorf("decode trailing JSON: %w", err)
	}
	return nil
}

func sortedUnique(values []string) []string {
	set := make(map[string]struct{}, len(values))
	for _, value := range values {
		set[value] = struct{}{}
	}
	result := make([]string, 0, len(set))
	for value := range set {
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}
