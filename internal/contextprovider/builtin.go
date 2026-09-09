package contextprovider

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/bmatcuk/doublestar/v4"
	"github.com/pelletier/go-toml/v2"
	"github.com/phosphorco/workbench-go/internal/contextapi"
	"gopkg.in/yaml.v3"
)

// BuiltinLimits bounds one ai-context contribution call. Zero fields use the
// conservative defaults below. The hard ceilings keep a caller from turning
// the builtin into an unbounded directory or memory reader.
type BuiltinLimits struct {
	MaxDepth             uint32
	MaxContextFiles      uint32
	MaxManifestBytes     uint64
	MaxIncludeBytes      uint64
	MaxMentionBytes      uint64
	MaxIncludes          uint32
	MaxContributions     uint32
	MaxBodyBytes         uint64
	MaxOutputBytes       uint64
	MaxReasonBytes       uint64
	MaxObservedResources uint32
	MaxResourcePathBytes uint64
	MaxSelectors         uint32
	MaxSelectorBytes     uint64
}

const (
	defaultBuiltinDepth         = 64
	defaultBuiltinContextFiles  = 64
	defaultBuiltinManifest      = 256 * 1024
	defaultBuiltinInclude       = 256 * 1024
	defaultBuiltinMention       = 256 * 1024
	defaultBuiltinIncludes      = 64
	defaultBuiltinContributions = 64
	defaultBuiltinBody          = 256 * 1024
	defaultBuiltinOutput        = 1 << 20
	defaultBuiltinResources     = 64
	defaultBuiltinResourcePath  = 16 * 1024
	defaultBuiltinSelectors     = 64
	defaultBuiltinSelectorBytes = 16 * 1024
	builtinHardDepth            = 512
	builtinHardFiles            = 512
	builtinHardFileBytes        = 16 << 20
	builtinHardIncludes         = 512
	builtinHardContributions    = 512
	builtinHardOutput           = 64 << 20
	builtinHardReasons          = 1 << 20
	builtinHardObservationCount = 4096
	builtinHardSelectorBytes    = 1 << 20
)

// ContributeBuiltin reads current ai-context.md manifests under root and
// returns complete contribution bodies. It does not execute commands or
// retain a stat/content cache: every call observes manifest, include, and
// deletion changes in the scoped filesystem.
func ContributeBuiltin(
	ctx context.Context,
	request contextapi.ContributionRequest,
	provider contextapi.ProviderID,
	root string,
	limits BuiltinLimits,
) contextapi.ContributionResponse {
	response := contextapi.ContributionResponse{}
	limits = normalizeBuiltinLimits(limits)
	budget := &builtinBudget{
		contextFiles: limits.MaxContextFiles,
		includes:     limits.MaxIncludes,
		mentions:     limits.MaxMentionBytes,
		reasons:      limits.MaxReasonBytes,
		manifestMemo: make(map[string]manifestCacheEntry),
		includeMemo:  make(map[string]includeSnapshot),
		observedMemo: make(map[string]rootFileRead),
	}
	if ctx == nil {
		appendBuiltinReason(&response, builtinReason(contextapi.ReasonInvalidInput, "nil builtin context", request, provider, "builtin-input"), budget)
		return response
	}
	if root == "" {
		appendBuiltinReason(&response, builtinReason(contextapi.ReasonInvalidInput, "builtin root is empty", request, provider, "builtin-root"), budget)
		return response
	}
	rootFS, err := os.OpenRoot(root)
	if err != nil {
		appendBuiltinReason(&response, builtinReason(contextapi.ReasonProviderFailed, "open builtin root: "+err.Error(), request, provider, "builtin-root"), budget)
		return response
	}
	defer rootFS.Close()

	request.Observation.Selectors = boundedSelectors(request.Observation.Selectors, limits)

	attentions := builtinAttentions(request, limits)
	if len(attentions) == 0 {
		appendBuiltinReason(&response, builtinReason(contextapi.ReasonNoMatch, "no usable file or selector attention", request, provider, "builtin-attention"), budget)
		return response
	}
	maxContributions := minUint32NonZero(limits.MaxContributions, request.Limits.MaxContributions)
	maxBodyBytes := minUint64NonZero(limits.MaxBodyBytes, request.Limits.MaxBodyBytes)
	if maxBodyBytes == 0 {
		maxBodyBytes = limits.MaxBodyBytes
	}
	var outputBytes uint64
	for _, attention := range attentions {
		if err := ctx.Err(); err != nil {
			appendBuiltinReason(&response, builtinReason(contextapi.ReasonProviderFailed, "builtin contribution canceled", request, provider, "builtin-cancel"), budget)
			break
		}
		reads, readReasons := readManifests(ctx, rootFS, attention, limits, request, provider, budget)
		appendBuiltinReasons(&response, readReasons, budget)
		observedText := observedTextFor(ctx, rootFS, attention, reads, limits, budget)
		for _, read := range reads {
			if read.kind != manifestParsed {
				if read.kind == manifestUnavailable {
					appendBuiltinContribution(&response, diagnosticContribution(read, request, provider), provider, maxContributions, maxBodyBytes, limits.MaxOutputBytes, &outputBytes, budget)
				}
				continue
			}
			for index, section := range read.manifest.Docs {
				if !sectionMatches(section.Files, section.Mentions, read.target, request.Observation.Selectors, observedText) {
					continue
				}
				body := docsBody(section, read.includes)
				contribution := builtinContribution(read, request, provider, "docs", index, body, matchedReason(request, read.path, provider, "docs", index))
				appendBuiltinContribution(&response, contribution, provider, maxContributions, maxBodyBytes, limits.MaxOutputBytes, &outputBytes, budget)
			}
			for index, section := range read.manifest.Commands {
				if read.target.kind == attentionSelectors && hasFileCommandVariable(section.Command) {
					continue
				}
				if !sectionMatches(section.Files, section.Mentions, read.target, request.Observation.Selectors, observedText) {
					continue
				}
				body := commandBody(section, read.target)
				contribution := builtinContribution(read, request, provider, "command", index, body, matchedReason(request, read.path, provider, "command", index))
				contribution.Slot.Key = contextapi.SlotKey(fmt.Sprintf("%s:command:%d:%s", read.path, index, read.target.subject()))
				contribution.Source.Identity.ID = contextapi.SourceID(contribution.Slot.Key)
				contribution.SourceRevision.Source.ID = contribution.Source.Identity.ID
				appendBuiltinContribution(&response, contribution, provider, maxContributions, maxBodyBytes, limits.MaxOutputBytes, &outputBytes, budget)
			}
		}
		if maxContributions != 0 && uint32(len(response.Contributions)) >= maxContributions {
			break
		}
	}
	if len(response.Contributions) == 0 {
		appendBuiltinReason(&response, builtinReason(contextapi.ReasonNoMatch, "no ai-context rule matched the observed attention", request, provider, "builtin-match"), budget)
	}
	return response
}

// Revalidate reports whether the exact builtin source revision is still
// current. SourceRevision covers the complete manifest bytes and every
// declared include's path, status, and complete bounded bytes, so manifest
// edits, include edits, and include deletion all invalidate a pending source.
// A deleted manifest returns (false, nil); it is a normal freshness result.
func Revalidate(
	ctx context.Context,
	provider contextapi.ProviderID,
	root string,
	source contextapi.SourceRef,
	expected contextapi.SourceRevision,
	limits BuiltinLimits,
) (bool, error) {
	if ctx == nil {
		return false, errors.New("contextprovider: nil revalidation context")
	}
	if provider == "" || source.Identity.Provider != provider || expected.Source != source.Identity || source.Path == "" {
		return false, fmt.Errorf("%w: builtin source identity is invalid", ErrInvalidProviderConfig)
	}
	relative, ok := safeRelative(source.Path)
	if !ok || path.Base(relative) != "ai-context.md" {
		return false, fmt.Errorf("%w: builtin source path is outside the root", ErrInvalidProviderConfig)
	}
	rootFS, err := os.OpenRoot(root)
	if err != nil {
		return false, fmt.Errorf("contextprovider: open builtin root for revalidation: %w", err)
	}
	defer rootFS.Close()
	limits = normalizeBuiltinLimits(limits)
	read, err := readRootText(ctx, rootFS, relative, limits.MaxManifestBytes)
	if err != nil {
		return false, err
	}
	if read.kind == fileMissing {
		return false, nil
	}
	if read.kind != fileFound {
		return false, nil
	}
	manifest, err := parseManifest(read.body)
	if err != nil {
		return false, nil
	}
	directory := path.Dir(relative)
	budget := &builtinBudget{
		includes:    limits.MaxIncludes,
		includeMemo: make(map[string]includeSnapshot),
	}
	includes := includeSnapshots(ctx, rootFS, directory, manifest, limits, budget)
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if budget.incomplete {
		return false, nil
	}
	current := contextapi.SourceRevision{Source: source.Identity, Revision: sourceRevisionDigest(read.body, includes)}
	return current == expected, nil
}

type builtinAttention struct {
	kind     attentionKind
	resource string
}

type attentionKind uint8

const (
	attentionResource attentionKind = iota + 1
	attentionSelectors
)

func builtinAttentions(request contextapi.ContributionRequest, limits BuiltinLimits) []builtinAttention {
	capacity := len(request.Observation.Resources)
	if uint64(capacity) > uint64(limits.MaxObservedResources) {
		capacity = int(limits.MaxObservedResources)
	}
	attentions := make([]builtinAttention, 0, capacity)
	for index, resource := range request.Observation.Resources {
		if index >= builtinHardObservationCount {
			break
		}
		if uint32(len(attentions)) >= limits.MaxObservedResources {
			break
		}
		if resource.Kind != contextapi.ResourceFile || resource.Outcome == contextapi.ResourceFailed {
			continue
		}
		if limits.MaxResourcePathBytes != 0 && uint64(len([]byte(resource.Path))) > limits.MaxResourcePathBytes {
			continue
		}
		relative, ok := safeRelative(resource.Path)
		if !ok {
			continue
		}
		attentions = append(attentions, builtinAttention{kind: attentionResource, resource: relative})
	}
	if len(attentions) == 0 && len(request.Observation.Selectors) > 0 {
		return []builtinAttention{{kind: attentionSelectors}}
	}
	return attentions
}

func boundedSelectors(input []contextapi.ObservedSelector, limits BuiltinLimits) []contextapi.ObservedSelector {
	capacity := len(input)
	if uint64(capacity) > uint64(limits.MaxSelectors) {
		capacity = int(limits.MaxSelectors)
	}
	output := make([]contextapi.ObservedSelector, 0, capacity)
	var bytes uint64
	for index, selector := range input {
		if uint32(index) >= limits.MaxSelectors {
			break
		}
		selectorBytes := uint64(len([]byte(selector.Raw)))
		if limits.MaxSelectorBytes != 0 && selectorBytes > limits.MaxSelectorBytes-bytes {
			break
		}
		bytes += selectorBytes
		selector.Interpretations = append([]contextapi.SelectorInterpretation(nil), selector.Interpretations...)
		output = append(output, selector)
	}
	return output
}

type manifestKind uint8

const (
	manifestParsed manifestKind = iota + 1
	manifestUnavailable
	manifestMalformed
)

type manifestRead struct {
	kind       manifestKind
	directory  string
	path       string
	target     manifestTarget
	manifest   contextapi.AIContextManifest
	raw        []byte
	includes   []includeSnapshot
	diagnostic string
}

type manifestCacheEntry struct {
	kind       manifestKind
	raw        []byte
	manifest   contextapi.AIContextManifest
	includes   []includeSnapshot
	diagnostic string
}

type builtinBudget struct {
	contextFiles uint32
	includes     uint32
	mentions     uint64
	reasons      uint64
	reasonBytes  uint64
	incomplete   bool
	manifestMemo map[string]manifestCacheEntry
	includeMemo  map[string]includeSnapshot
	observedMemo map[string]rootFileRead
}

type manifestTarget struct {
	kind             attentionKind
	relativeObserved string
	repoObserved     string
}

func (target manifestTarget) subject() string {
	if target.kind == attentionSelectors {
		return "selectors"
	}
	return target.repoObserved
}

func readManifests(
	ctx context.Context,
	root *os.Root,
	attention builtinAttention,
	limits BuiltinLimits,
	request contextapi.ContributionRequest,
	provider contextapi.ProviderID,
	budget *builtinBudget,
) ([]manifestRead, []contextapi.Reason) {
	directory := "."
	if attention.kind == attentionResource {
		directory = path.Dir(attention.resource)
	}
	reads := make([]manifestRead, 0, limits.MaxContextFiles)
	reasons := []contextapi.Reason{}
	for depth := uint32(0); depth < limits.MaxDepth; depth++ {
		candidate := path.Join(directory, "ai-context.md")
		cached, exists := budget.manifestMemo[candidate]
		if !exists {
			if budget.contextFiles == 0 {
				break
			}
			file, err := readRootText(ctx, root, candidate, limits.MaxManifestBytes)
			if err != nil {
				reasons = append(reasons, builtinReason(contextapi.ReasonProviderFailed, "read "+candidate+": "+err.Error(), request, provider, "builtin-read"))
				break
			}
			// Count every uncached candidate lookup, including a missing
			// manifest. Missing ancestors are still filesystem work and must
			// not multiply by attention count.
			budget.contextFiles--
			cached = manifestCacheEntry{}
			switch file.kind {
			case fileMissing:
				cached.kind = 0
			case fileOversized:
				cached.kind = manifestUnavailable
				cached.diagnostic = fmt.Sprintf("%s exceeds the %d-byte guidance limit", candidate, limits.MaxManifestBytes)
			case fileUnavailable:
				cached.kind = manifestUnavailable
				cached.diagnostic = file.diagnostic
			case fileFound:
				cached.raw = file.body
				manifest, parseErr := parseManifest(file.body)
				if parseErr != nil {
					cached.kind = manifestMalformed
					reasons = append(reasons, builtinReason(contextapi.ReasonMalformedContribution, candidate+": "+parseErr.Error(), request, provider, "ai-context-frontmatter"))
				} else {
					cached.kind = manifestParsed
					cached.manifest = manifest
					cached.includes = includeSnapshots(ctx, root, directory, manifest, limits, budget)
				}
			}
			budget.manifestMemo[candidate] = cached
		}
		if cached.kind != 0 {
			target := manifestTarget{kind: attention.kind, repoObserved: attention.resource}
			if attention.kind == attentionResource {
				if directory == "." {
					target.relativeObserved = attention.resource
				} else {
					target.relativeObserved = strings.TrimPrefix(path.Join(".", attention.resource[len(directory):]), "./")
					if !strings.HasPrefix(attention.resource, directory+"/") && attention.resource != directory {
						target.relativeObserved = attention.resource
					}
				}
			}
			read := manifestRead{
				kind: cached.kind, directory: directory, path: candidate, target: target,
				raw: cached.raw, manifest: cached.manifest, includes: cached.includes, diagnostic: cached.diagnostic,
			}
			reads = append(reads, read)
			if read.kind == manifestParsed && read.manifest.Root {
				break
			}
		}
		if directory == "." {
			break
		}
		directory = path.Dir(directory)
	}
	return reads, reasons
}

func observedTextFor(ctx context.Context, root *os.Root, attention builtinAttention, reads []manifestRead, limits BuiltinLimits, budget *builtinBudget) string {
	if attention.kind != attentionResource || !readsNeedMentions(reads) {
		return ""
	}
	if read, ok := budget.observedMemo[attention.resource]; ok {
		if read.kind != fileFound {
			return ""
		}
		return string(read.body)
	}
	if budget.mentions == 0 {
		budget.observedMemo[attention.resource] = rootFileRead{kind: fileOversized}
		return ""
	}
	maximum := limits.MaxMentionBytes
	if budget.mentions < maximum {
		maximum = budget.mentions
	}
	read, err := readRootText(ctx, root, attention.resource, maximum)
	if err != nil || read.kind != fileFound {
		budget.observedMemo[attention.resource] = read
		return ""
	}
	budget.mentions -= uint64(len(read.body))
	budget.observedMemo[attention.resource] = read
	return string(read.body)
}

func readsNeedMentions(reads []manifestRead) bool {
	for _, read := range reads {
		if read.kind != manifestParsed {
			continue
		}
		for _, section := range read.manifest.Docs {
			if len(section.Mentions) > 0 {
				return true
			}
		}
		for _, section := range read.manifest.Commands {
			if len(section.Mentions) > 0 {
				return true
			}
		}
	}
	return false
}

func sectionMatches(files, mentions []string, target manifestTarget, selectors []contextapi.ObservedSelector, observedText string) bool {
	if len(files) == 0 && len(mentions) == 0 {
		return true
	}
	if target.kind == attentionResource {
		for _, pattern := range files {
			matched, err := doublestar.Match(strings.ReplaceAll(pattern, "\\", "/"), target.relativeObserved)
			if err == nil && matched {
				return true
			}
		}
	}
	for _, mention := range mentions {
		if mention != "" && (strings.Contains(observedText, mention) || selectorMentions(selectors, mention)) {
			return true
		}
	}
	return false
}

func selectorMentions(selectors []contextapi.ObservedSelector, mention string) bool {
	for _, selector := range selectors {
		if strings.Contains(selector.Raw, mention) {
			return true
		}
	}
	return false
}

func docsBody(section contextapi.AIDocumentRule, includes []includeSnapshot) string {
	parts := []string{}
	if section.Message != "" {
		parts = append(parts, section.Message)
	}
	for _, includePath := range section.Include {
		found := findInclude(includes, includePath)
		if found == nil {
			parts = append(parts, includePath+":")
			continue
		}
		switch found.status {
		case includeFound:
			parts = append(parts, includePath+":\n"+string(found.body))
		case includeOutside:
			parts = append(parts, "Context Magnet diagnostic: includes out-of-root file "+includePath)
		case includeMissing:
			parts = append(parts, "Context Magnet diagnostic: includes missing file "+includePath)
		case includeOversized:
			parts = append(parts, "Context Magnet diagnostic: includes oversized file "+includePath)
		case includeUnavailable:
			parts = append(parts, "Context Magnet diagnostic: cannot read included file "+includePath)
		case includeBounded:
			parts = append(parts, "Context Magnet diagnostic: include read budget exceeded for "+includePath)
		}
	}
	return strings.Join(parts, "\n")
}

func commandBody(section contextapi.AICommandRule, target manifestTarget) string {
	command := section.Command
	if target.kind == attentionResource {
		variables := commandVariables(target)
		command = interpolateCommand(command, variables)
	}
	prefix := "command"
	if section.Label != "" {
		prefix += " " + section.Label
	}
	if section.CWD != "" {
		prefix += " (cwd: " + section.CWD + ")"
	}
	return prefix + ": " + command
}

func commandVariables(target manifestTarget) map[string]string {
	fileRelative := target.relativeObserved
	fileRelativeToRoot := target.repoObserved
	segments := strings.Split(fileRelative, "/")
	fileName := segments[len(segments)-1]
	fileDir := "."
	if len(segments) > 1 {
		fileDir = strings.Join(segments[:len(segments)-1], "/")
	}
	extension := path.Ext(fileName)
	fileBaseName := fileName
	fileExt := ""
	if extension != "" && extension != "." {
		fileBaseName = strings.TrimSuffix(fileName, extension)
		fileExt = strings.TrimPrefix(extension, ".")
	}
	dirname := fileDir
	if fileDir != "." {
		dirname = path.Base(fileDir)
	}
	return map[string]string{
		"file":               fileRelative,
		"fileRelative":       fileRelative,
		"fileRelativeToRoot": fileRelativeToRoot,
		"fileDir":            fileDir,
		"fileDirRelative":    fileDir,
		"dirname":            dirname,
		"fileBaseName":       fileBaseName,
		"fileName":           fileName,
		"fileExt":            fileExt,
	}
}

func interpolateCommand(command string, variables map[string]string) string {
	var builder strings.Builder
	for index := 0; index < len(command); {
		start := strings.IndexByte(command[index:], '{')
		if start < 0 {
			builder.WriteString(command[index:])
			break
		}
		start += index
		builder.WriteString(command[index:start])
		end := strings.IndexByte(command[start+1:], '}')
		if end < 0 {
			builder.WriteString(command[start:])
			break
		}
		end += start + 1
		key := command[start+1 : end]
		value, ok := variables[key]
		if ok {
			builder.WriteString(value)
		} else {
			builder.WriteString(command[start : end+1])
		}
		index = end + 1
	}
	return builder.String()
}

func hasFileCommandVariable(command string) bool {
	for key := range commandVariables(manifestTarget{kind: attentionResource, relativeObserved: "file", repoObserved: "file"}) {
		if strings.Contains(command, "{"+key+"}") {
			return true
		}
	}
	return false
}

func builtinContribution(
	read manifestRead,
	request contextapi.ContributionRequest,
	provider contextapi.ProviderID,
	kind string,
	index int,
	body string,
	reason contextapi.Reason,
) contextapi.Contribution {
	sourceID := contextapi.SourceID(fmt.Sprintf("%s:%s:%d", read.path, kind, index))
	content := contentIdentity(body)
	return contextapi.Contribution{
		Slot:            contextapi.ContributionSlot{Provider: provider, Key: contextapi.SlotKey(sourceID)},
		Source:          contextapi.SourceRef{Identity: contextapi.SourceIdentity{Provider: provider, ID: sourceID}, Kind: contextapi.SourceFile, Path: read.path},
		SourceRevision:  contextapi.SourceRevision{Source: contextapi.SourceIdentity{Provider: provider, ID: sourceID}, Revision: contextapi.SourceRevisionID(sourceRevisionDigest(read.raw, read.includes))},
		Content:         content,
		Body:            body,
		Recruitment:     recruitmentFor(read.target),
		Contributor:     provider,
		ConfigDigest:    request.ConfigDigest,
		ProfileRevision: request.Profile.Revision,
		Reasons:         []contextapi.Reason{reason},
	}
}

func diagnosticContribution(read manifestRead, request contextapi.ContributionRequest, provider contextapi.ProviderID) contextapi.Contribution {
	body := "Context Magnet diagnostic: " + read.diagnostic
	return builtinContribution(read, request, provider, "diagnostic", 0, body, matchedReason(request, read.path, provider, "diagnostic", 0))
}

func recruitmentFor(target manifestTarget) contextapi.SourceRecruitment {
	if target.kind == attentionSelectors {
		return contextapi.SourceRecruitment{Kind: contextapi.RecruitmentSelectors}
	}
	return contextapi.SourceRecruitment{Kind: contextapi.RecruitmentObservedFile, Resource: target.repoObserved}
}

func matchedReason(request contextapi.ContributionRequest, sourcePath string, provider contextapi.ProviderID, kind string, index int) contextapi.Reason {
	params := []contextapi.ReasonParameter{
		{Key: "source", Value: contextapi.ReasonValue{Kind: contextapi.ReasonValueText, Text: sourcePath}},
		{Key: "section", Value: contextapi.ReasonValue{Kind: contextapi.ReasonValueText, Text: kind}},
		{Key: "index", Value: contextapi.ReasonValue{Kind: contextapi.ReasonValueNumber, Number: int64(index)}},
	}
	return contextapi.Reason{
		Code:     contextapi.ReasonNoMatch,
		Origin:   contextapi.ReasonProvider,
		Provider: provider,
		Rule:     "ai-context." + kind,
		Summary:  "matched ai-context rule",
		Params:   params,
		Evidence: evidenceForRequest(request),
		At:       requestAt(request),
	}
}

func appendBuiltinContribution(
	response *contextapi.ContributionResponse,
	contribution contextapi.Contribution,
	provider contextapi.ProviderID,
	maxContributions uint32,
	maxBodyBytes, maxOutputBytes uint64,
	outputBytes *uint64,
	budget *builtinBudget,
) {
	bodyBytes := uint64(len([]byte(contribution.Body)))
	if maxContributions != 0 && uint32(len(response.Contributions)) >= maxContributions {
		return
	}
	if maxBodyBytes != 0 && bodyBytes > maxBodyBytes {
		appendBuiltinReason(response, contextapi.Reason{
			Code: contextapi.ReasonMalformedContribution, Origin: contextapi.ReasonProvider, Provider: provider,
			Rule: "ai-context.body", Summary: "complete builtin contribution exceeds body bound", At: time.Now().UTC(),
		}, budget)
		return
	}
	if maxOutputBytes != 0 && bodyBytes > maxOutputBytes-*outputBytes {
		appendBuiltinReason(response, contextapi.Reason{
			Code: contextapi.ReasonMalformedContribution, Origin: contextapi.ReasonProvider, Provider: provider,
			Rule: "ai-context.output", Summary: "complete builtin output exceeds output bound", At: time.Now().UTC(),
		}, budget)
		return
	}
	if contribution.Reasons != nil {
		remaining := budget.reasons - budget.reasonBytes
		if budget.reasons != 0 && reasonBytes(contribution.Reasons) > remaining {
			contribution.Reasons = nil
		}
		budget.reasonBytes += reasonBytes(contribution.Reasons)
	}
	response.Contributions = append(response.Contributions, contribution)
	*outputBytes += bodyBytes
}

func appendBuiltinReasons(response *contextapi.ContributionResponse, reasons []contextapi.Reason, budget *builtinBudget) {
	for _, reason := range reasons {
		appendBuiltinReason(response, reason, budget)
	}
}

func appendBuiltinReason(response *contextapi.ContributionResponse, reason contextapi.Reason, budget *builtinBudget) {
	if budget.reasons == 0 {
		response.Reasons = append(response.Reasons, reason)
		return
	}
	size := reasonBytes([]contextapi.Reason{reason})
	if size > budget.reasons-budget.reasonBytes {
		return
	}
	response.Reasons = append(response.Reasons, reason)
	budget.reasonBytes += size
}

type fileKind uint8

const (
	fileMissing fileKind = iota + 1
	fileFound
	fileOversized
	fileUnavailable
)

type rootFileRead struct {
	kind       fileKind
	body       []byte
	diagnostic string
}

func readRootText(ctx context.Context, root *os.Root, relative string, maximum uint64) (rootFileRead, error) {
	if err := ctx.Err(); err != nil {
		return rootFileRead{}, err
	}
	file, err := root.Open(relative)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return rootFileRead{kind: fileMissing}, nil
		}
		return rootFileRead{kind: fileUnavailable, diagnostic: err.Error()}, nil
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return rootFileRead{kind: fileUnavailable, diagnostic: err.Error()}, nil
	}
	if !info.Mode().IsRegular() {
		return rootFileRead{kind: fileUnavailable, diagnostic: "path is not a regular file"}, nil
	}
	if maximum == 0 {
		return rootFileRead{kind: fileOversized}, nil
	}
	reader := io.LimitReader(file, int64(maximum)+1)
	body, err := io.ReadAll(reader)
	if err != nil {
		return rootFileRead{kind: fileUnavailable, diagnostic: err.Error()}, nil
	}
	if uint64(len(body)) > maximum {
		return rootFileRead{kind: fileOversized}, nil
	}
	if !utf8.Valid(body) {
		return rootFileRead{kind: fileUnavailable, diagnostic: "file is not valid UTF-8"}, nil
	}
	return rootFileRead{kind: fileFound, body: body}, nil
}

type includeKind uint8

const (
	includeFound includeKind = iota + 1
	includeMissing
	includeOutside
	includeOversized
	includeUnavailable
	includeBounded
)

type includeSnapshot struct {
	path     string
	authored string
	status   includeKind
	body     []byte
}

func includeSnapshots(ctx context.Context, root *os.Root, directory string, manifest contextapi.AIContextManifest, limits BuiltinLimits, budget *builtinBudget) []includeSnapshot {
	targets := make([]struct {
		authored string
		relative string
		outside  bool
	}, 0, limits.MaxIncludes)
	seen := map[string]struct{}{}
	for _, section := range manifest.Docs {
		for _, include := range section.Include {
			if uint32(len(targets)) >= limits.MaxIncludes {
				budget.incomplete = true
				break
			}
			relative, ok := includeRelative(directory, include)
			if !ok {
				targets = append(targets, struct {
					authored string
					relative string
					outside  bool
				}{authored: include, relative: "!outside:" + include, outside: true})
				continue
			}
			if _, exists := seen[relative]; exists {
				continue
			}
			seen[relative] = struct{}{}
			targets = append(targets, struct {
				authored string
				relative string
				outside  bool
			}{authored: include, relative: relative})
		}
	}
	includes := make([]includeSnapshot, 0, len(targets))
	for _, target := range targets {
		if target.outside {
			cached, exists := budget.includeMemo[target.relative]
			if !exists {
				if budget.includes == 0 {
					budget.incomplete = true
					includes = append(includes, includeSnapshot{path: target.relative, authored: target.authored, status: includeBounded})
					continue
				}
				budget.includes--
				cached = includeSnapshot{path: target.relative, status: includeOutside}
				budget.includeMemo[target.relative] = cached
			}
			cached.authored = target.authored
			includes = append(includes, cached)
			continue
		}
		if cached, exists := budget.includeMemo[target.relative]; exists {
			cached.authored = target.authored
			includes = append(includes, cached)
			continue
		}
		if budget.includes == 0 {
			budget.incomplete = true
			includes = append(includes, includeSnapshot{path: target.relative, authored: target.authored, status: includeBounded})
			continue
		}
		budget.includes--
		read, err := readRootText(ctx, root, target.relative, limits.MaxIncludeBytes)
		if err != nil {
			cached := includeSnapshot{path: target.relative, status: includeUnavailable}
			budget.includeMemo[target.relative] = cached
			cached.authored = target.authored
			includes = append(includes, cached)
			continue
		}
		status := includeUnavailable
		switch read.kind {
		case fileFound:
			status = includeFound
		case fileMissing:
			status = includeMissing
		case fileOversized:
			status = includeOversized
		}
		cached := includeSnapshot{path: target.relative, status: status, body: read.body}
		budget.includeMemo[target.relative] = cached
		cached.authored = target.authored
		includes = append(includes, cached)
	}
	return includes
}

func findInclude(includes []includeSnapshot, authoredPath string) *includeSnapshot {
	for index := range includes {
		if includes[index].authored == authoredPath || includes[index].path == authoredPath || path.Base(includes[index].path) == authoredPath {
			return &includes[index]
		}
	}
	return nil
}

func includeRelative(directory, authored string) (string, bool) {
	if authored == "" || path.IsAbs(strings.ReplaceAll(authored, "\\", "/")) {
		return "", false
	}
	relative := path.Clean(path.Join(directory, strings.ReplaceAll(authored, "\\", "/")))
	return safeRelative(relative)
}

func parseManifest(source []byte) (contextapi.AIContextManifest, error) {
	lines := strings.Split(strings.ReplaceAll(string(source), "\r\n", "\n"), "\n")
	if len(lines) == 0 || strings.TrimSpace(lines[0]) != "---" {
		return contextapi.AIContextManifest{}, errors.New("ai-context.md must start with TOML or YAML frontmatter delimited by ---")
	}
	closing := -1
	for index := 1; index < len(lines); index++ {
		if strings.TrimSpace(lines[index]) == "---" {
			closing = index
			break
		}
	}
	if closing < 0 {
		return contextapi.AIContextManifest{}, errors.New("ai-context.md frontmatter is missing the closing --- delimiter")
	}
	frontmatter := strings.Join(lines[1:closing], "\n")
	var tomlManifest manifestWire
	tomlErr := toml.Unmarshal([]byte(frontmatter), &tomlManifest)
	if tomlErr == nil {
		return tomlManifest.domain(), nil
	}
	var yamlManifest manifestWire
	yamlErr := yaml.Unmarshal([]byte(frontmatter), &yamlManifest)
	if yamlErr == nil {
		return yamlManifest.domain(), nil
	}
	return contextapi.AIContextManifest{}, fmt.Errorf("frontmatter is neither valid TOML nor YAML (TOML: %s; YAML: %s)", shortDiagnostic(tomlErr), shortDiagnostic(yamlErr))
}

type manifestWire struct {
	Root     bool           `toml:"root" yaml:"root"`
	Docs     []documentWire `toml:"docs" yaml:"docs"`
	Commands []commandWire  `toml:"commands" yaml:"commands"`
}

type documentWire struct {
	Files    []string `toml:"files" yaml:"files"`
	Mentions []string `toml:"mentions" yaml:"mentions"`
	Include  []string `toml:"include" yaml:"include"`
	Message  string   `toml:"message" yaml:"message"`
}

type commandWire struct {
	Files    []string `toml:"files" yaml:"files"`
	Mentions []string `toml:"mentions" yaml:"mentions"`
	Command  string   `toml:"command" yaml:"command"`
	Label    string   `toml:"label" yaml:"label"`
	CWD      string   `toml:"cwd" yaml:"cwd"`
}

func (manifest manifestWire) domain() contextapi.AIContextManifest {
	output := contextapi.AIContextManifest{Root: manifest.Root}
	output.Docs = make([]contextapi.AIDocumentRule, len(manifest.Docs))
	for index, section := range manifest.Docs {
		output.Docs[index] = contextapi.AIDocumentRule{Files: append([]string(nil), section.Files...), Mentions: append([]string(nil), section.Mentions...), Include: append([]string(nil), section.Include...), Message: section.Message}
	}
	output.Commands = make([]contextapi.AICommandRule, len(manifest.Commands))
	for index, section := range manifest.Commands {
		output.Commands[index] = contextapi.AICommandRule{Files: append([]string(nil), section.Files...), Mentions: append([]string(nil), section.Mentions...), Command: section.Command, Label: section.Label, CWD: section.CWD}
	}
	return output
}

func safeRelative(input string) (string, bool) {
	input = strings.ReplaceAll(input, "\\", "/")
	if input == "" || strings.HasPrefix(input, "/") || strings.ContainsRune(input, 0) {
		return "", false
	}
	clean := path.Clean(input)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") {
		if clean == "." {
			return clean, true
		}
		return "", false
	}
	return strings.TrimPrefix(clean, "./"), true
}

func normalizeBuiltinLimits(input BuiltinLimits) BuiltinLimits {
	if input.MaxDepth == 0 {
		input.MaxDepth = defaultBuiltinDepth
	}
	if input.MaxContextFiles == 0 {
		input.MaxContextFiles = defaultBuiltinContextFiles
	}
	if input.MaxManifestBytes == 0 {
		input.MaxManifestBytes = defaultBuiltinManifest
	}
	if input.MaxIncludeBytes == 0 {
		input.MaxIncludeBytes = defaultBuiltinInclude
	}
	if input.MaxMentionBytes == 0 {
		input.MaxMentionBytes = defaultBuiltinMention
	}
	if input.MaxIncludes == 0 {
		input.MaxIncludes = defaultBuiltinIncludes
	}
	if input.MaxContributions == 0 {
		input.MaxContributions = defaultBuiltinContributions
	}
	if input.MaxBodyBytes == 0 {
		input.MaxBodyBytes = defaultBuiltinBody
	}
	if input.MaxOutputBytes == 0 {
		input.MaxOutputBytes = defaultBuiltinOutput
	}
	if input.MaxReasonBytes == 0 {
		input.MaxReasonBytes = 64 * 1024
	}
	if input.MaxObservedResources == 0 {
		input.MaxObservedResources = defaultBuiltinResources
	}
	if input.MaxResourcePathBytes == 0 {
		input.MaxResourcePathBytes = defaultBuiltinResourcePath
	}
	if input.MaxSelectors == 0 {
		input.MaxSelectors = defaultBuiltinSelectors
	}
	if input.MaxSelectorBytes == 0 {
		input.MaxSelectorBytes = defaultBuiltinSelectorBytes
	}
	input.MaxDepth = minBuiltin(input.MaxDepth, builtinHardDepth)
	input.MaxContextFiles = minBuiltin(input.MaxContextFiles, builtinHardFiles)
	input.MaxManifestBytes = minBuiltin64(input.MaxManifestBytes, builtinHardFileBytes)
	input.MaxIncludeBytes = minBuiltin64(input.MaxIncludeBytes, builtinHardFileBytes)
	input.MaxMentionBytes = minBuiltin64(input.MaxMentionBytes, builtinHardFileBytes)
	input.MaxIncludes = minBuiltin(input.MaxIncludes, builtinHardIncludes)
	input.MaxContributions = minBuiltin(input.MaxContributions, builtinHardContributions)
	input.MaxBodyBytes = minBuiltin64(input.MaxBodyBytes, builtinHardFileBytes)
	input.MaxOutputBytes = minBuiltin64(input.MaxOutputBytes, builtinHardOutput)
	input.MaxReasonBytes = minBuiltin64(input.MaxReasonBytes, builtinHardReasons)
	input.MaxObservedResources = minBuiltin(input.MaxObservedResources, builtinHardObservationCount)
	input.MaxResourcePathBytes = minBuiltin64(input.MaxResourcePathBytes, builtinHardSelectorBytes)
	input.MaxSelectors = minBuiltin(input.MaxSelectors, builtinHardObservationCount)
	input.MaxSelectorBytes = minBuiltin64(input.MaxSelectorBytes, builtinHardSelectorBytes)
	return input
}

func minBuiltin(a, b uint32) uint32 {
	if a < b {
		return a
	}
	return b
}

func minBuiltin64(a, b uint64) uint64 {
	if a < b {
		return a
	}
	return b
}

func builtinReason(code contextapi.ReasonCode, summary string, request contextapi.ContributionRequest, provider contextapi.ProviderID, rule string) contextapi.Reason {
	return contextapi.Reason{Code: code, Origin: contextapi.ReasonProvider, Provider: provider, Rule: rule, Summary: summary, Evidence: evidenceForRequest(request), At: requestAt(request)}
}

func shortDiagnostic(err error) string {
	if err == nil {
		return ""
	}
	message := strings.Join(strings.Fields(err.Error()), " ")
	if len(message) > 500 {
		return message[:500]
	}
	return message
}
