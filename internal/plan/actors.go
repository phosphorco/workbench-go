package plan

import (
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// Actor is a resolved identity. An empty kind preserves untyped, legacy plans.
// Identity policy is applied on writes, never while replaying historical events.
type Actor struct {
	Kind string `json:"kind"`
	ID   string `json:"id"`
}

func (a Actor) String() string {
	if a.Kind == "" {
		return a.ID
	}
	return a.Kind + ":" + a.ID
}

func (a Actor) Validate() error {
	if strings.TrimSpace(a.ID) == "" || a.ID == "@me" {
		return fmt.Errorf("actor requires a resolved, nonempty id")
	}
	if a.Kind != "" && !actorKindName.MatchString(a.Kind) {
		return fmt.Errorf("invalid actor kind %q", a.Kind)
	}
	return nil
}

type ActorAlias struct {
	Pattern     string `json:"pattern"`
	Replacement string `json:"replacement"`
}

type ActorInference struct {
	Env string `json:"env"`
}

type ActorKind struct {
	Pattern string          `json:"pattern"`
	Infer   *ActorInference `json:"infer,omitempty"`
	Aliases []ActorAlias    `json:"aliases"`
}

type Actors struct {
	Kinds map[string]ActorKind `json:"kinds"`
}

var actorKindName = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_-]*$`)

func fullPattern(pattern string) (*regexp.Regexp, error) {
	if pattern == "" {
		return nil, fmt.Errorf("empty actor pattern")
	}
	return regexp.Compile(`\A(?:` + pattern + `)\z`)
}

func (a Actors) names() []string {
	names := make([]string, 0, len(a.Kinds))
	for name := range a.Kinds {
		names = append(names, name)
	}
	slices.Sort(names)
	return names
}

func (a Actors) Validate() error {
	for _, name := range a.names() {
		k := a.Kinds[name]
		if !actorKindName.MatchString(name) {
			return fmt.Errorf("invalid kind name %q", name)
		}
		if _, err := fullPattern(k.Pattern); err != nil {
			return fmt.Errorf("%s.pattern: %w", name, err)
		}
		for i, alias := range k.Aliases {
			re, err := fullPattern(alias.Pattern)
			if err != nil {
				return fmt.Errorf("%s.aliases[%d]: %w", name, i, err)
			}
			if err := validateReplacement(re, alias.Replacement); err != nil {
				return fmt.Errorf("%s.aliases[%d]: %w", name, i, err)
			}
		}
	}
	return nil
}

// Go's expander consumes Unicode letters/digits in unbraced capture names.
// Share that token grammar; reject malformed/unknown references before expansion.
var replacementToken = regexp.MustCompile(`^\$(\$|\{[\pL\p{Nd}_]+\}|[\pL\p{Nd}_]+)`)

func validateReplacement(re *regexp.Regexp, replacement string) error {
	for {
		at := strings.IndexByte(replacement, '$')
		if at < 0 {
			return nil
		}
		replacement = replacement[at:]
		token := replacementToken.FindString(replacement)
		if token == "" || !strings.HasPrefix(replacement, token) {
			return fmt.Errorf("invalid replacement reference in %q", replacement)
		}
		name := strings.TrimSuffix(strings.TrimPrefix(token[1:], "{"), "}")
		if name != "$" {
			number, err := strconv.Atoi(name)
			// Match Go's numeric-reference limit and leading-zero rule.
			numeric := err == nil && number <= 999999999 && (len(name) == 1 || name[0] != '0')
			if numeric {
				if number > re.NumSubexp() {
					return fmt.Errorf("unknown capture %q", name)
				}
			} else if re.SubexpIndex(name) < 0 {
				return fmt.Errorf("unknown capture %q", name)
			}
		}
		replacement = replacement[len(token):]
	}
}

func (a Actors) candidates(name, value string) ([]Actor, error) {
	k := a.Kinds[name]
	canonical, err := fullPattern(k.Pattern)
	if err != nil {
		return nil, err
	}
	candidates := []Actor{}
	add := func(id string) error {
		actor := Actor{Kind: name, ID: id}
		if actor.Validate() != nil || !canonical.MatchString(id) {
			return fmt.Errorf("alias for %s produced an invalid canonical identity", name)
		}
		if !slices.Contains(candidates, actor) {
			candidates = append(candidates, actor)
		}
		return nil
	}
	if canonical.MatchString(value) {
		if err := add(value); err != nil {
			return nil, err
		}
	}
	for _, alias := range k.Aliases {
		re, err := fullPattern(alias.Pattern)
		if err != nil {
			return nil, err
		}
		if match := re.FindStringSubmatchIndex(value); match != nil {
			if err := add(string(re.ExpandString(nil, alias.Replacement, value, match))); err != nil {
				return nil, err
			}
		}
	}
	return candidates, nil
}

func uniqueActor(candidates []Actor) (Actor, error) {
	switch len(candidates) {
	case 0:
		return Actor{}, fmt.Errorf("actor does not match any configured kind")
	case 1:
		return candidates[0], nil
	default:
		names := make([]string, len(candidates))
		for i, actor := range candidates {
			names[i] = actor.String()
		}
		return Actor{}, fmt.Errorf("ambiguous actor (%s); qualify with KIND:ID, or supply a canonical id", strings.Join(names, ", "))
	}
}

// Resolve recognizes aliases once; aliases do not recursively rewrite identities.
func (a Actors) Resolve(value string) (Actor, error) {
	if len(a.Kinds) == 0 {
		actor := Actor{ID: value}
		return actor, actor.Validate()
	}
	names := a.names()
	if prefix, rest, ok := strings.Cut(value, ":"); ok {
		if _, known := a.Kinds[prefix]; known {
			// Qualification is also a canonical escape: canonical IDs remain
			// usable even if an alias happens to match their spelling.
			pattern, err := fullPattern(a.Kinds[prefix].Pattern)
			if err != nil {
				return Actor{}, err
			}
			if pattern.MatchString(rest) {
				actor := Actor{Kind: prefix, ID: rest}
				return actor, actor.Validate()
			}
			names = []string{prefix}
			value = rest
		}
	}
	candidates := []Actor{}
	for _, name := range names {
		found, err := a.candidates(name, value)
		if err != nil {
			return Actor{}, err
		}
		candidates = append(candidates, found...)
	}
	return uniqueActor(candidates)
}

// Infer consults only explicitly configured variables. An explicit --by bypasses
// this operation, including invalid or conflicting ambient identities.
func (a Actors) Infer(lookup func(string) (string, bool)) (Actor, error) {
	candidates := []Actor{}
	for _, name := range a.names() {
		k := a.Kinds[name]
		if k.Infer == nil || k.Infer.Env == "" {
			continue
		}
		value, set := lookup(k.Infer.Env)
		if !set || value == "" {
			continue
		}
		found, err := a.candidates(name, value)
		if err != nil {
			return Actor{}, fmt.Errorf("infer %s from %s: %w", name, k.Infer.Env, err)
		}
		if len(found) == 0 {
			return Actor{}, fmt.Errorf("%s does not contain a valid %s actor", k.Infer.Env, name)
		}
		candidates = append(candidates, found...)
	}
	actor, err := uniqueActor(candidates)
	if err != nil {
		return Actor{}, fmt.Errorf("cannot infer identity: %w; supply --by KIND:ID", err)
	}
	return actor, nil
}
