// Package tsfiles selects the files a tsconfig names, following TypeScript's
// `include`, `exclude` and `files` matching (typescript.js matchFiles and
// getSubPatternFromSpec) over a given list of repository files rather than a
// directory walk. Contract 0.14 observes exactly the files the generated
// tsconfig would compile, and only files Git tracks.
package tsfiles

import (
	"path"
	"sort"
	"strings"
)

// commonPackageFolders are never matched by a wildcard.
var commonPackageFolders = []string{"node_modules", "bower_components", "jspm_packages"}

// SourceExtensions are the TypeScript source extensions compiled without
// allowJs, longest first so declaration files are recognized.
var SourceExtensions = []string{".d.ts", ".d.mts", ".d.cts", ".ts", ".tsx", ".mts", ".cts"}

// HasSourceExtension reports whether name has a TypeScript source extension.
func HasSourceExtension(name string) bool {
	for _, extension := range SourceExtensions {
		if strings.HasSuffix(name, extension) {
			return true
		}
	}
	return false
}

// Spec is one resolved project file selection. Every path is slash-separated
// and relative to the repository root ("" is the root); include and exclude
// patterns are already resolved against the directory of the config file that
// declared them.
type Spec struct {
	// Include is nil when no include applies; Files is nil likewise. When both
	// are nil TypeScript includes everything below Directory.
	Include []string
	Exclude []string
	Files   []string
	// Directory is the directory of the leaf config file.
	Directory string
}

// Select returns the tracked files the spec compiles, in sorted order. Only
// TypeScript sources are returned for include matches; explicit files are
// returned whatever their extension when they are tracked.
func Select(spec Spec, tracked []string) ([]string, error) {
	include := spec.Include
	if include == nil && spec.Files == nil {
		include = []string{join(spec.Directory, "**/*")}
	}
	includes := make([]pattern, 0, len(include))
	for _, value := range include {
		compiled, ok := compile(value, false)
		if ok {
			includes = append(includes, compiled)
		}
	}
	excludes := compileExcludes(spec.Exclude)
	// matched holds each wildcard match with the index of the first include
	// that matched it: TypeScript groups readDirectory results by include.
	type match struct {
		file    string
		include int
	}
	matched := []match{}
	if len(includes) != 0 {
		for _, file := range tracked {
			if !HasSourceExtension(file) {
				continue
			}
			segments := strings.Split(file, "/")
			if excludes.matches(segments) {
				continue
			}
			for index, candidate := range includes {
				if candidate.matchWhole(segments) {
					matched = append(matched, match{file, index})
					break
				}
			}
		}
	}
	literal := make(map[string]struct{}, len(spec.Files))
	if len(spec.Files) != 0 {
		trackedSet := make(map[string]struct{}, len(tracked))
		for _, file := range tracked {
			trackedSet[file] = struct{}{}
		}
		for _, file := range spec.Files {
			if _, exists := trackedSet[file]; exists {
				literal[file] = struct{}{}
			}
		}
	}
	// Replay getFileNamesFromConfigSpecs over readDirectory's order (by
	// include, then directory traversal: a directory's files sorted before
	// its subdirectories) so extension priority resolves as TypeScript's does.
	sort.SliceStable(matched, func(left, right int) bool {
		if matched[left].include != matched[right].include {
			return matched[left].include < matched[right].include
		}
		return traversalLess(matched[left].file, matched[right].file)
	})
	selected := make(map[string]struct{}, len(matched)+len(literal))
	for _, candidate := range matched {
		if hasFileWithHigherPriorityExtension(candidate.file, literal, selected) {
			continue
		}
		removeWildcardFilesWithLowerPriorityExtension(candidate.file, selected)
		if _, exists := literal[candidate.file]; !exists {
			selected[candidate.file] = struct{}{}
		}
	}
	for file := range literal {
		selected[file] = struct{}{}
	}
	result := make([]string, 0, len(selected))
	for file := range selected {
		result = append(result, file)
	}
	sort.Strings(result)
	return result, nil
}

// extensionGroups are TypeScript's supported extensions without allowJs, in
// priority order within each group.
var extensionGroups = [][]string{{".ts", ".tsx", ".d.ts"}, {".cts", ".d.cts"}, {".mts", ".d.mts"}}

// removableExtensions is TypeScript's extensionsToRemove, in order.
var removableExtensions = []string{".d.ts", ".d.mts", ".d.cts", ".mjs", ".mts", ".cjs", ".cts", ".ts", ".js", ".tsx", ".jsx", ".json"}

func extensionGroup(file string) []string {
	for _, group := range extensionGroups {
		for _, extension := range group {
			if strings.HasSuffix(file, extension) {
				return group
			}
		}
	}
	return nil
}

func changeExtension(file, extension string) string {
	for _, removable := range removableExtensions {
		if strings.HasSuffix(file, removable) {
			return strings.TrimSuffix(file, removable) + extension
		}
	}
	return file + extension
}

// hasFileWithHigherPriorityExtension ports TypeScript's function of that
// name (JavaScript cases omitted: allowJs is refused).
func hasFileWithHigherPriorityExtension(file string, literal, wildcard map[string]struct{}) bool {
	for _, extension := range extensionGroup(file) {
		if strings.HasSuffix(file, extension) && (extension != ".ts" || !strings.HasSuffix(file, ".d.ts")) {
			return false
		}
		higher := changeExtension(file, extension)
		if _, exists := literal[higher]; exists {
			return true
		}
		if _, exists := wildcard[higher]; exists {
			return true
		}
	}
	return false
}

// removeWildcardFilesWithLowerPriorityExtension ports TypeScript's function.
func removeWildcardFilesWithLowerPriorityExtension(file string, wildcard map[string]struct{}) {
	group := extensionGroup(file)
	for index := len(group) - 1; index >= 0; index-- {
		if strings.HasSuffix(file, group[index]) {
			return
		}
		delete(wildcard, changeExtension(file, group[index]))
	}
}

// traversalLess orders paths as a sorted directory walk visits them: files
// of a directory (sorted) before its subdirectories (sorted).
func traversalLess(left, right string) bool {
	leftSegments, rightSegments := strings.Split(left, "/"), strings.Split(right, "/")
	for index := 0; index < len(leftSegments) && index < len(rightSegments); index++ {
		if leftSegments[index] == rightSegments[index] {
			continue
		}
		leftFile, rightFile := index == len(leftSegments)-1, index == len(rightSegments)-1
		if leftFile != rightFile {
			return leftFile
		}
		return leftSegments[index] < rightSegments[index]
	}
	return len(leftSegments) < len(rightSegments)
}

// Excluded reports whether a repository-relative path matches the excludes.
func Excluded(excludes []string, file string) bool {
	return compileExcludes(excludes).matches(strings.Split(file, "/"))
}

type part struct {
	double  bool
	literal bool
	text    string
	// wildcard parts of a non-exclude spec never match a common package
	// folder, and a leading * or ? never matches a leading ".".
	guardPackageFolder bool
	guardLeadingDot    bool
}

type pattern struct {
	parts   []part
	exclude bool
}

type excludeSet []pattern

func compileExcludes(values []string) excludeSet {
	result := make(excludeSet, 0, len(values))
	for _, value := range values {
		if compiled, ok := compile(value, true); ok {
			result = append(result, compiled)
		}
	}
	return result
}

func (set excludeSet) matches(segments []string) bool {
	for _, candidate := range set {
		// The exclude terminator (?:$|/) admits any path below a match.
		for end := 1; end <= len(segments); end++ {
			if candidate.match(0, segments[:end]) {
				return true
			}
		}
	}
	return false
}

// compile mirrors getSubPatternFromSpec for one repository-relative spec.
func compile(spec string, exclude bool) (pattern, bool) {
	components := strings.Split(spec, "/")
	if spec == "" {
		components = nil
	}
	last := ""
	if len(components) != 0 {
		last = components[len(components)-1]
	}
	if !exclude && last == "**" {
		return pattern{}, false
	}
	if isImplicitGlob(last) {
		components = append(components, "**", "*")
	}
	parts := make([]part, 0, len(components))
	for _, component := range components {
		if component == "**" {
			parts = append(parts, part{double: true})
			continue
		}
		wildcard := strings.ContainsAny(component, "*?")
		parts = append(parts, part{
			literal:            !wildcard,
			text:               component,
			guardPackageFolder: wildcard && !exclude,
			guardLeadingDot:    !exclude && (strings.HasPrefix(component, "*") || strings.HasPrefix(component, "?")),
		})
	}
	return pattern{parts: parts, exclude: exclude}, true
}

func (candidate pattern) matchWhole(segments []string) bool {
	return candidate.match(0, segments)
}

func (candidate pattern) match(index int, segments []string) bool {
	if index == len(candidate.parts) {
		return len(segments) == 0
	}
	current := candidate.parts[index]
	if current.double {
		if candidate.match(index+1, segments) {
			return true
		}
		for consumed := 1; consumed <= len(segments); consumed++ {
			segment := segments[consumed-1]
			if !candidate.exclude && (strings.HasPrefix(segment, ".") || isPackageFolder(segment)) {
				return false
			}
			if candidate.match(index+1, segments[consumed:]) {
				return true
			}
		}
		return false
	}
	if len(segments) == 0 {
		return false
	}
	segment := segments[0]
	if current.literal {
		if segment != current.text {
			return false
		}
	} else {
		if current.guardPackageFolder && isPackageFolder(segment) {
			return false
		}
		if current.guardLeadingDot && strings.HasPrefix(segment, ".") {
			return false
		}
		if !wildcardMatch(current.text, segment) {
			return false
		}
	}
	return candidate.match(index+1, segments[1:])
}

func isPackageFolder(segment string) bool {
	for _, folder := range commonPackageFolders {
		if segment == folder {
			return true
		}
	}
	return false
}

// wildcardMatch matches * (any run) and ? (one character) within a segment.
func wildcardMatch(pattern, text string) bool {
	if pattern == "" {
		return text == ""
	}
	switch pattern[0] {
	case '*':
		for skip := 0; skip <= len(text); skip++ {
			if wildcardMatch(pattern[1:], text[skip:]) {
				return true
			}
		}
		return false
	case '?':
		return text != "" && wildcardMatch(pattern[1:], text[1:])
	default:
		return text != "" && text[0] == pattern[0] && wildcardMatch(pattern[1:], text[1:])
	}
}

func isImplicitGlob(component string) bool {
	return !strings.ContainsAny(component, ".*?")
}

func join(directory, relative string) string {
	if directory == "" || directory == "." {
		return relative
	}
	return path.Join(directory, relative)
}
