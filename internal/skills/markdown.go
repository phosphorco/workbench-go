package skills

import (
	"fmt"
	"regexp"
	"strings"
	"unicode"
)

var markdownLinkPattern = regexp.MustCompile(`\[([^\]]*)\]\(([^)]+\.md(?:#[^)]+)?)\)`)
var externalLinkPattern = regexp.MustCompile(`^[a-z]+://`)
var fencedCodePattern = regexp.MustCompile("(?s)```.*?```")
var markdownDestinationPattern = regexp.MustCompile(`\]\((\\.|[^)])*\)`)
var inlineCodePattern = regexp.MustCompile("`([^`\\n]*)`")

type markdownLink struct {
	label       string
	target      string
	line        int
	external    bool
	composition bool
}

func extractLinks(source string) []markdownLink {
	matches := markdownLinkPattern.FindAllStringSubmatchIndex(source, -1)
	links := make([]markdownLink, 0, len(matches))
	for _, match := range matches {
		label := source[match[2]:match[3]]
		target := source[match[4]:match[5]]
		if hash := strings.LastIndex(target, "#"); hash >= 0 {
			target = target[:hash]
		}
		links = append(links, markdownLink{
			label:       label,
			target:      target,
			line:        lineAt(source, match[0]),
			external:    externalLinkPattern.MatchString(target),
			composition: strings.HasSuffix(target, "SKILL.md"),
		})
	}
	return links
}

func hasSkillLabel(label string, skillName string) bool {
	needle := "$" + skillName
	for offset := 0; offset <= len(label)-len(needle); {
		relative := strings.Index(label[offset:], needle)
		if relative < 0 {
			break
		}
		index := offset + relative
		if index > 0 && isSkillNameCharacter(rune(label[index-1])) {
			offset = index + 1
			continue
		}
		after := index + len(needle)
		if after == len(label) || !isSkillNameCharacter(rune(label[after])) {
			return true
		}
		offset = index + 1
	}
	return false
}

func checkSkillReferences(document catalogDocument, skillNames []string, report *Report) {
	markdown := maskMatches(document.contents, fencedCodePattern)
	markdown = maskMatches(markdown, markdownDestinationPattern)
	for _, skillName := range skillNames {
		if skillName == document.skillName {
			continue
		}
		visible := maskNonReferenceInlineCode(markdown, skillName)
		for _, index := range stringOccurrences(visible, skillName) {
			if !isBareReferenceAt(visible, index, skillName) {
				continue
			}
			if !strings.Contains(skillName, "-") && !isSingleTokenReferenceContext(document.contents, index, skillName) {
				continue
			}
			reference := skillName
			if index > 0 && visible[index-1] == '/' {
				reference = "/" + reference
			}
			report.Issues = append(report.Issues, Diagnostic{Source: document.source.Name, Path: document.relativePath, Line: lineAt(document.contents, index), Message: fmt.Sprintf("skill reference %q must use %q", reference, "$"+skillName)})
		}
		spacedName := strings.ReplaceAll(skillName, "-", " ")
		if spacedName == skillName {
			continue
		}
		for _, index := range stringOccurrences(visible, spacedName) {
			if !isBareReferenceAt(visible, index, spacedName) {
				continue
			}
			report.Warnings = append(report.Warnings, Diagnostic{Source: document.source.Name, Path: document.relativePath, Line: lineAt(document.contents, index), Message: fmt.Sprintf("possible skill reference %q; use %q if the phrase names the skill", spacedName, "$"+skillName)})
		}
	}
}

func maskNonReferenceInlineCode(source string, skillName string) string {
	return replaceMatches(source, inlineCodePattern, func(match string, groups []string) bool {
		candidate := strings.TrimSpace(groups[0])
		return candidate != skillName && candidate != "/"+skillName && candidate != "$"+skillName
	})
}

func maskMatches(source string, pattern *regexp.Regexp) string {
	return replaceMatches(source, pattern, func(string, []string) bool { return true })
}

func replaceMatches(source string, pattern *regexp.Regexp, shouldMask func(string, []string) bool) string {
	result := []byte(source)
	for _, indices := range pattern.FindAllStringSubmatchIndex(source, -1) {
		groups := make([]string, 0, len(indices)/2-1)
		for index := 2; index+1 < len(indices); index += 2 {
			if indices[index] < 0 {
				groups = append(groups, "")
			} else {
				groups = append(groups, source[indices[index]:indices[index+1]])
			}
		}
		if !shouldMask(source[indices[0]:indices[1]], groups) {
			continue
		}
		for index := indices[0]; index < indices[1]; index++ {
			if result[index] != '\n' {
				result[index] = ' '
			}
		}
	}
	return string(result)
}

func isBareReferenceAt(source string, index int, value string) bool {
	if index > 0 {
		previous := rune(source[index-1])
		if previous == '$' || isSkillNameCharacter(previous) {
			return false
		}
	}
	after := index + len(value)
	return after == len(source) || !isSkillNameCharacter(rune(source[after]))
}

func isSkillNameCharacter(character rune) bool {
	return character == '_' || character == '-' || unicode.IsLetter(character) || unicode.IsDigit(character)
}

func isSingleTokenReferenceContext(source string, index int, skillName string) bool {
	lineStart := strings.LastIndex(source[:index], "\n") + 1
	nextNewline := strings.Index(source[index:], "\n")
	lineEnd := len(source)
	if nextNewline >= 0 {
		lineEnd = index + nextNewline
	}
	line := source[lineStart:lineEnd]
	column := index - lineStart
	trimmedLeft := strings.TrimLeft(line, " \t")
	if len(trimmedLeft) < len(line) && (strings.HasPrefix(trimmedLeft, "entry:") || strings.HasPrefix(trimmedLeft, "companion:") || strings.HasPrefix(trimmedLeft, "related:")) {
		return true
	}
	linkOpen := strings.LastIndex(line[:column], "[")
	linkClose := strings.Index(line[column+len(skillName):], "](")
	if linkOpen >= 0 && linkClose >= 0 {
		return true
	}
	inlineOpen := strings.LastIndex(line[:column], "`")
	inlineClose := strings.Index(line[column+len(skillName):], "`")
	return inlineOpen >= 0 && inlineClose >= 0
}

func lineAt(source string, index int) int {
	return strings.Count(source[:index], "\n") + 1
}

func stringOccurrences(source string, value string) []int {
	indices := make([]int, 0)
	for start := 0; start <= len(source)-len(value); {
		index := strings.Index(source[start:], value)
		if index < 0 {
			break
		}
		index += start
		indices = append(indices, index)
		start = index + len(value)
	}
	return indices
}
