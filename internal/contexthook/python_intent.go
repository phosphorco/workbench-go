package contexthook

import (
	"path"
	"strings"
	"unicode/utf8"

	"github.com/phosphorco/workbench-go/internal/contextapi"
)

const (
	pythonIntentMaxSourceBytes = 64 * 1024
	pythonIntentMaxTokens      = 8192
	pythonIntentMaxNesting     = 32
	pythonIntentMaxBindings    = 256
	pythonIntentMaxIntents     = 256
	pythonIntentMaxValueBytes  = 64 * 1024
)

type pythonFileIntent struct {
	path      string
	operation contextapi.ResourceOperation
}

// pythonFileIntents recognizes only source-level file intent. It has no
// filesystem, process, or environment authority. A false result invalidates
// the complete Python program so callers never consume a partial parse.
func pythonFileIntents(source string) ([]pythonFileIntent, bool) {
	if len(source) > pythonIntentMaxSourceBytes || strings.IndexByte(source, 0) >= 0 || pythonHasCodingDeclaration(source) || !utf8.ValidString(source) {
		return nil, false
	}
	tokens, ok := lexPythonIntent(source)
	if !ok {
		return nil, false
	}
	parser := pythonIntentParser{
		tokens: tokens,
		bindings: map[string]pythonBinding{
			"open":    {kind: pythonBindingOpen},
			"print":   {kind: pythonBindingBuiltin, name: "print"},
			"compile": {kind: pythonBindingBuiltin, name: "compile"},
		},
	}
	if !parser.parseProgram() {
		return nil, false
	}
	return parser.intents, true
}

type pythonTokenKind uint8

const (
	pythonTokenEOF pythonTokenKind = iota
	pythonTokenName
	pythonTokenString
	pythonTokenNumber
	pythonTokenNewline
	pythonTokenSemicolon
	pythonTokenComma
	pythonTokenDot
	pythonTokenEqual
	pythonTokenPlus
	pythonTokenSlash
	pythonTokenLParen
	pythonTokenRParen
	pythonTokenLBracket
	pythonTokenRBracket
	pythonTokenLBrace
	pythonTokenRBrace
	pythonTokenColon
)

type pythonToken struct {
	kind  pythonTokenKind
	value string
}

func lexPythonIntent(source string) ([]pythonToken, bool) {
	tokens := make([]pythonToken, 0, min(pythonIntentMaxTokens, len(source)/2+1))
	lineStart := true
	nesting := 0
	for index := 0; index < len(source); {
		current := source[index]
		if lineStart && (current == ' ' || current == '\t') {
			for index < len(source) && (source[index] == ' ' || source[index] == '\t') {
				index++
			}
			if index == len(source) {
				break
			}
			if source[index] != '#' && source[index] != '\n' && source[index] != '\r' {
				return nil, false
			}
			continue
		}
		if current == '\r' {
			index++
			if index < len(source) && source[index] == '\n' {
				continue
			}
			if !appendPythonToken(&tokens, pythonToken{kind: pythonTokenNewline}) {
				return nil, false
			}
			lineStart = true
			continue
		}
		if current == '\n' {
			index++
			if !appendPythonToken(&tokens, pythonToken{kind: pythonTokenNewline}) {
				return nil, false
			}
			lineStart = true
			continue
		}
		if current == ' ' || current == '\t' {
			index++
			continue
		}
		if current == '#' {
			for index < len(source) && source[index] != '\n' && source[index] != '\r' {
				index++
			}
			continue
		}
		lineStart = false

		if current == '\'' || current == '"' {
			value, next, ok := scanPythonString(source, index, false)
			if !ok || !appendPythonToken(&tokens, pythonToken{kind: pythonTokenString, value: value}) {
				return nil, false
			}
			index = next
			continue
		}
		if isPythonNameStart(current) {
			start := index
			for index < len(source) && isPythonNamePart(source[index]) {
				index++
			}
			name := source[start:index]
			if index < len(source) && (source[index] == '\'' || source[index] == '"') {
				if name != "r" && name != "u" {
					return nil, false
				}
				value, next, ok := scanPythonString(source, index, name == "r")
				if !ok || !appendPythonToken(&tokens, pythonToken{kind: pythonTokenString, value: value}) {
					return nil, false
				}
				index = next
				continue
			}
			if !appendPythonToken(&tokens, pythonToken{kind: pythonTokenName, value: name}) {
				return nil, false
			}
			continue
		}
		if current >= '0' && current <= '9' {
			start := index
			for index < len(source) && source[index] >= '0' && source[index] <= '9' {
				index++
			}
			if !appendPythonToken(&tokens, pythonToken{kind: pythonTokenNumber, value: source[start:index]}) {
				return nil, false
			}
			continue
		}

		kind := pythonTokenKind(0)
		switch current {
		case ';':
			kind = pythonTokenSemicolon
		case ',':
			kind = pythonTokenComma
		case '.':
			kind = pythonTokenDot
		case '=':
			kind = pythonTokenEqual
		case '+':
			kind = pythonTokenPlus
		case '/':
			kind = pythonTokenSlash
		case '(':
			kind = pythonTokenLParen
			nesting++
		case ')':
			kind = pythonTokenRParen
			nesting--
		case '[':
			kind = pythonTokenLBracket
			nesting++
		case ']':
			kind = pythonTokenRBracket
			nesting--
		case '{':
			kind = pythonTokenLBrace
			nesting++
		case '}':
			kind = pythonTokenRBrace
			nesting--
		case ':':
			kind = pythonTokenColon
		default:
			return nil, false
		}
		if nesting < 0 || nesting > pythonIntentMaxNesting || !appendPythonToken(&tokens, pythonToken{kind: kind}) {
			return nil, false
		}
		index++
	}
	if nesting != 0 || !appendPythonToken(&tokens, pythonToken{kind: pythonTokenEOF}) {
		return nil, false
	}
	return tokens, true
}

func pythonHasCodingDeclaration(source string) bool {
	lineStart := 0
	for line := 0; line < 2 && lineStart <= len(source); line++ {
		lineEnd := lineStart
		for lineEnd < len(source) && source[lineEnd] != '\n' && source[lineEnd] != '\r' {
			lineEnd++
		}
		comment := strings.TrimLeft(source[lineStart:lineEnd], " \t")
		if len(comment) > 0 && comment[0] == '#' && pythonCommentHasCodingMarker(comment[1:]) {
			return true
		}
		if lineEnd == len(source) {
			break
		}
		lineStart = lineEnd + 1
		if source[lineEnd] == '\r' && lineStart < len(source) && source[lineStart] == '\n' {
			lineStart++
		}
	}
	return false
}

func pythonCommentHasCodingMarker(comment string) bool {
	for offset := 0; offset+len("coding") < len(comment); offset++ {
		if comment[offset:offset+len("coding")] != "coding" {
			continue
		}
		if offset > 0 && isPythonNamePart(comment[offset-1]) {
			continue
		}
		markerEnd := offset + len("coding")
		if comment[markerEnd] == ':' || comment[markerEnd] == '=' {
			return true
		}
	}
	return false
}

func appendPythonToken(tokens *[]pythonToken, token pythonToken) bool {
	if len(*tokens) >= pythonIntentMaxTokens {
		return false
	}
	*tokens = append(*tokens, token)
	return true
}

func scanPythonString(source string, start int, raw bool) (string, int, bool) {
	if start >= len(source) || (source[start] != '\'' && source[start] != '"') {
		return "", start, false
	}
	quote := source[start]
	if start+2 < len(source) && source[start+1] == quote && source[start+2] == quote {
		return "", start, false
	}
	var value strings.Builder
	for index := start + 1; index < len(source); index++ {
		current := source[index]
		if current == quote {
			return value.String(), index + 1, true
		}
		if current == '\n' || current == '\r' {
			return "", index, false
		}
		if current != '\\' {
			value.WriteByte(current)
			continue
		}
		if index+1 >= len(source) || source[index+1] == '\n' || source[index+1] == '\r' {
			return "", index, false
		}
		index++
		next := source[index]
		if raw {
			value.WriteByte('\\')
			value.WriteByte(next)
			continue
		}
		switch next {
		case 'a':
			value.WriteByte('\a')
		case 'b':
			value.WriteByte('\b')
		case 'f':
			value.WriteByte('\f')
		case 'n':
			value.WriteByte('\n')
		case 'r':
			value.WriteByte('\r')
		case 't':
			value.WriteByte('\t')
		case 'v':
			value.WriteByte('\v')
		case '\\', '\'', '"':
			value.WriteByte(next)
		case '0':
			value.WriteByte(0)
		case 'x':
			if index+2 >= len(source) {
				return "", index, false
			}
			first, firstOK := pythonHexValue(source[index+1])
			second, secondOK := pythonHexValue(source[index+2])
			if !firstOK || !secondOK {
				return "", index, false
			}
			value.WriteRune(rune(first<<4 | second))
			index += 2
		case 'u', 'U':
			digits := 4
			if next == 'U' {
				digits = 8
			}
			if index+digits >= len(source) {
				return "", index, false
			}
			var codePoint uint32
			for offset := 1; offset <= digits; offset++ {
				digit, ok := pythonHexValue(source[index+offset])
				if !ok {
					return "", index, false
				}
				codePoint = codePoint<<4 | uint32(digit)
			}
			if codePoint > uint32(utf8.MaxRune) || codePoint >= 0xd800 && codePoint <= 0xdfff {
				return "", index, false
			}
			value.WriteRune(rune(codePoint))
			index += digits
		default:
			return "", index, false
		}
	}
	return "", len(source), false
}

func pythonHexValue(value byte) (byte, bool) {
	switch {
	case value >= '0' && value <= '9':
		return value - '0', true
	case value >= 'a' && value <= 'f':
		return value - 'a' + 10, true
	case value >= 'A' && value <= 'F':
		return value - 'A' + 10, true
	default:
		return 0, false
	}
}

func isPythonNameStart(value byte) bool {
	return value == '_' || value >= 'a' && value <= 'z' || value >= 'A' && value <= 'Z'
}

func isPythonNamePart(value byte) bool {
	return isPythonNameStart(value) || value >= '0' && value <= '9'
}

type pythonBindingKind uint8

const (
	pythonBindingUnknown pythonBindingKind = iota
	pythonBindingString
	pythonBindingStringUnknown
	pythonBindingNumber
	pythonBindingPath
	pythonBindingPathConstructor
	pythonBindingPathlib
	pythonBindingOpen
	pythonBindingBuiltin
	pythonBindingHandle
)

type pythonBinding struct {
	kind  pythonBindingKind
	value string
	name  string
}

type pythonExpr struct {
	kind     pythonBindingKind
	value    string
	member   string
	receiver *pythonExpr
}

type pythonCallArgs struct {
	positional []pythonExpr
	keywords   map[string]pythonExpr
}

type pythonIntentParser struct {
	tokens   []pythonToken
	position int
	bindings map[string]pythonBinding
	intents  []pythonFileIntent
}

func (parser *pythonIntentParser) parseProgram() bool {
	parser.skipNewlines()
	for !parser.at(pythonTokenEOF) {
		if !parser.parseSimpleStatement() {
			return false
		}
		if parser.at(pythonTokenSemicolon) {
			parser.position++
			if parser.at(pythonTokenEOF) {
				return true
			}
			parser.skipNewlines()
			continue
		}
		if !parser.at(pythonTokenNewline) && !parser.at(pythonTokenEOF) {
			return false
		}
		parser.skipNewlines()
	}
	return true
}

func (parser *pythonIntentParser) parseSimpleStatement() bool {
	if parser.at(pythonTokenName) {
		switch parser.current().value {
		case "import":
			return parser.parseImport()
		case "from":
			return parser.parseFromImport()
		case "if", "for", "while", "def", "class", "try", "with", "match", "async", "else", "elif", "except", "finally", "return", "yield", "raise", "assert", "global", "nonlocal", "del", "pass", "break", "continue":
			return false
		}
		if parser.looksLikeAssignment() {
			target := parser.current().value
			if isPythonReservedName(target) {
				return false
			}
			parser.position += 2
			expression, ok := parser.parseExpression()
			if !ok || !parser.atExpressionEnd() {
				return false
			}
			return parser.assign(target, expression)
		}
	}
	_, ok := parser.parseExpression()
	return ok && parser.atExpressionEnd()
}

func (parser *pythonIntentParser) parseImport() bool {
	parser.position++
	if !parser.require(pythonTokenName, "pathlib") {
		return false
	}
	alias := "pathlib"
	if parser.atName("as") {
		parser.position++
		if !parser.at(pythonTokenName) || isPythonReservedName(parser.current().value) {
			return false
		}
		alias = parser.current().value
		parser.position++
	}
	if parser.at(pythonTokenComma) {
		return false
	}
	return parser.bind(alias, pythonBinding{kind: pythonBindingPathlib})
}

func (parser *pythonIntentParser) parseFromImport() bool {
	parser.position++
	if !parser.require(pythonTokenName, "pathlib") || !parser.require(pythonTokenName, "import") || !parser.require(pythonTokenName, "Path") {
		return false
	}
	alias := "Path"
	if parser.atName("as") {
		parser.position++
		if !parser.at(pythonTokenName) || isPythonReservedName(parser.current().value) {
			return false
		}
		alias = parser.current().value
		parser.position++
	}
	if parser.at(pythonTokenComma) {
		return false
	}
	return parser.bind(alias, pythonBinding{kind: pythonBindingPathConstructor})
}

func (parser *pythonIntentParser) parseExpression() (pythonExpr, bool) {
	left, ok := parser.parsePostfix()
	if !ok {
		return pythonExpr{}, false
	}
	for parser.at(pythonTokenPlus) || parser.at(pythonTokenSlash) {
		operator := parser.current().kind
		parser.position++
		right, rightOK := parser.parsePostfix()
		if !rightOK {
			return pythonExpr{}, false
		}
		left, ok = parser.combineExpressions(left, right, operator)
		if !ok {
			return pythonExpr{}, false
		}
	}
	return left, true
}

func (parser *pythonIntentParser) parsePostfix() (pythonExpr, bool) {
	expression, ok := parser.parsePrimary()
	if !ok {
		return pythonExpr{}, false
	}
	for {
		switch {
		case parser.at(pythonTokenDot):
			parser.position++
			if !parser.at(pythonTokenName) {
				return pythonExpr{}, false
			}
			member := parser.current().value
			parser.position++
			if parser.at(pythonTokenLParen) {
				args, argsOK := parser.parseCallArgs()
				if !argsOK {
					return pythonExpr{}, false
				}
				expression, ok = parser.callMember(expression, member, args)
				if !ok {
					return pythonExpr{}, false
				}
			} else {
				receiver := expression
				expression = pythonExpr{kind: pythonBindingUnknown, member: member, receiver: &receiver}
			}
		case parser.at(pythonTokenLParen):
			args, argsOK := parser.parseCallArgs()
			if !argsOK {
				return pythonExpr{}, false
			}
			expression, ok = parser.call(expression, args)
			if !ok {
				return pythonExpr{}, false
			}
		default:
			return expression, true
		}
	}
}

func (parser *pythonIntentParser) parsePrimary() (pythonExpr, bool) {
	switch parser.current().kind {
	case pythonTokenString:
		value := parser.current().value
		parser.position++
		return pythonExpr{kind: pythonBindingString, value: value}, true
	case pythonTokenNumber:
		parser.position++
		return pythonExpr{kind: pythonBindingNumber}, true
	case pythonTokenName:
		name := parser.current().value
		if isPythonUnsupportedExpressionName(name) {
			return pythonExpr{}, false
		}
		parser.position++
		binding := parser.bindings[name]
		return pythonExpr{kind: binding.kind, value: binding.value, member: binding.name}, true
	case pythonTokenLParen:
		parser.position++
		if parser.at(pythonTokenRParen) {
			return pythonExpr{}, false
		}
		expression, ok := parser.parseExpression()
		if !ok || !parser.require(pythonTokenRParen, "") {
			return pythonExpr{}, false
		}
		return expression, true
	default:
		return pythonExpr{}, false
	}
}

func (parser *pythonIntentParser) parseCallArgs() (pythonCallArgs, bool) {
	if !parser.require(pythonTokenLParen, "") {
		return pythonCallArgs{}, false
	}
	args := pythonCallArgs{keywords: make(map[string]pythonExpr)}
	sawKeyword := false
	if parser.at(pythonTokenRParen) {
		parser.position++
		return args, true
	}
	for {
		if parser.at(pythonTokenName) && parser.peek(1).kind == pythonTokenEqual {
			sawKeyword = true
			name := parser.current().value
			if isPythonReservedName(name) {
				return pythonCallArgs{}, false
			}
			parser.position += 2
			if _, exists := args.keywords[name]; exists {
				return pythonCallArgs{}, false
			}
			expression, ok := parser.parseExpression()
			if !ok {
				return pythonCallArgs{}, false
			}
			args.keywords[name] = expression
		} else {
			if sawKeyword {
				return pythonCallArgs{}, false
			}
			expression, ok := parser.parseExpression()
			if !ok {
				return pythonCallArgs{}, false
			}
			args.positional = append(args.positional, expression)
		}
		if parser.at(pythonTokenRParen) {
			parser.position++
			return args, true
		}
		if !parser.require(pythonTokenComma, "") {
			return pythonCallArgs{}, false
		}
		if parser.at(pythonTokenRParen) {
			parser.position++
			return args, true
		}
	}
}

func (parser *pythonIntentParser) call(expression pythonExpr, args pythonCallArgs) (pythonExpr, bool) {
	switch expression.kind {
	case pythonBindingPathConstructor:
		if len(args.positional) != 1 || len(args.keywords) != 0 || !isPythonPathValue(args.positional[0]) {
			return pythonExpr{}, false
		}
		return pythonExpr{kind: pythonBindingPath, value: args.positional[0].value}, true
	case pythonBindingOpen:
		pathValue, mode, ok := parser.openArguments(args, true)
		if !ok || !parser.appendModeIntents(pathValue, mode) {
			return pythonExpr{}, false
		}
		return pythonExpr{kind: pythonBindingHandle}, true
	case pythonBindingBuiltin:
		if expression.member != "print" && expression.member != "compile" {
			return pythonExpr{}, false
		}
		return pythonExpr{kind: pythonBindingUnknown}, true
	default:
		return pythonExpr{}, false
	}
}

func (parser *pythonIntentParser) callMember(receiver pythonExpr, member string, args pythonCallArgs) (pythonExpr, bool) {
	switch receiver.kind {
	case pythonBindingPathlib:
		if member != "Path" {
			return pythonExpr{}, false
		}
		return parser.call(pythonExpr{kind: pythonBindingPathConstructor}, args)
	case pythonBindingPath:
		switch member {
		case "read_text", "read_bytes":
			if !parser.appendIntent(receiver.value, contextapi.ResourceRead) {
				return pythonExpr{}, false
			}
			return pythonExpr{kind: pythonBindingStringUnknown}, true
		case "write_text", "write_bytes":
			if !parser.appendIntent(receiver.value, contextapi.ResourceWrite) {
				return pythonExpr{}, false
			}
			return pythonExpr{kind: pythonBindingUnknown}, true
		case "open":
			mode, ok := parser.pathOpenMode(args)
			if !ok || !parser.appendModeIntents(receiver.value, mode) {
				return pythonExpr{}, false
			}
			return pythonExpr{kind: pythonBindingHandle}, true
		case "joinpath":
			return parser.joinPath(receiver.value, args)
		default:
			return pythonExpr{}, false
		}
	case pythonBindingHandle:
		switch member {
		case "read", "readline", "readlines":
			return pythonExpr{kind: pythonBindingStringUnknown}, true
		case "write", "writelines", "close", "flush", "seek", "tell":
			return pythonExpr{kind: pythonBindingUnknown}, true
		default:
			return pythonExpr{}, false
		}
	case pythonBindingString, pythonBindingStringUnknown:
		if !isPythonHarmlessStringMethod(member, args) {
			return pythonExpr{}, false
		}
		return pythonExpr{kind: pythonBindingStringUnknown}, true
	default:
		return pythonExpr{}, false
	}
}

func (parser *pythonIntentParser) combineExpressions(left, right pythonExpr, operator pythonTokenKind) (pythonExpr, bool) {
	switch operator {
	case pythonTokenPlus:
		if left.kind == pythonBindingString && right.kind == pythonBindingString {
			if !pythonConcatFits(len(left.value), len(right.value)) {
				return pythonExpr{}, false
			}
			return pythonExpr{kind: pythonBindingString, value: left.value + right.value}, true
		}
		if left.kind == pythonBindingPath || right.kind == pythonBindingPath {
			return pythonExpr{}, false
		}
		if left.kind == pythonBindingStringUnknown || right.kind == pythonBindingStringUnknown || left.kind == pythonBindingUnknown || right.kind == pythonBindingUnknown {
			return pythonExpr{kind: pythonBindingStringUnknown}, true
		}
		return pythonExpr{}, false
	case pythonTokenSlash:
		if left.kind != pythonBindingPath || right.kind != pythonBindingString {
			return pythonExpr{}, false
		}
		joined, ok := pythonJoinPath(left.value, right.value)
		if !ok {
			return pythonExpr{}, false
		}
		return pythonExpr{kind: pythonBindingPath, value: joined}, true
	default:
		return pythonExpr{}, false
	}
}

func (parser *pythonIntentParser) openArguments(args pythonCallArgs, withPath bool) (string, string, bool) {
	if !withPath || len(args.positional) < 1 || len(args.positional) > 2 || !isPythonPathValue(args.positional[0]) {
		return "", "", false
	}
	mode := "r"
	if len(args.positional) == 2 {
		if args.positional[1].kind != pythonBindingString {
			return "", "", false
		}
		mode = args.positional[1].value
	}
	if len(args.keywords) > 1 {
		return "", "", false
	}
	if keywordMode, exists := args.keywords["mode"]; exists {
		if len(args.positional) == 2 || keywordMode.kind != pythonBindingString {
			return "", "", false
		}
		mode = keywordMode.value
	}
	for keyword := range args.keywords {
		if keyword != "mode" {
			return "", "", false
		}
	}
	return args.positional[0].value, mode, validPythonMode(mode)
}

func (parser *pythonIntentParser) pathOpenMode(args pythonCallArgs) (string, bool) {
	if len(args.positional) > 1 || len(args.keywords) > 1 {
		return "", false
	}
	mode := "r"
	if len(args.positional) == 1 {
		if args.positional[0].kind != pythonBindingString {
			return "", false
		}
		mode = args.positional[0].value
	}
	if keywordMode, exists := args.keywords["mode"]; exists {
		if len(args.positional) == 1 || keywordMode.kind != pythonBindingString {
			return "", false
		}
		mode = keywordMode.value
	}
	for keyword := range args.keywords {
		if keyword != "mode" {
			return "", false
		}
	}
	return mode, validPythonMode(mode)
}

func validPythonMode(mode string) bool {
	switch mode {
	case "r", "rb", "w", "wb", "a", "ab", "x", "xb", "r+", "rb+", "r+b", "w+", "wb+", "w+b", "a+", "ab+", "a+b", "x+", "xb+", "x+b":
		return true
	default:
		return false
	}
}

func (parser *pythonIntentParser) appendModeIntents(pathValue, mode string) bool {
	read, write := pythonModeOperations(mode)
	if !read && !write {
		return false
	}
	if read && !parser.appendIntent(pathValue, contextapi.ResourceRead) {
		return false
	}
	if write && !parser.appendIntent(pathValue, contextapi.ResourceWrite) {
		return false
	}
	return true
}

func pythonModeOperations(mode string) (bool, bool) {
	if strings.Contains(mode, "+") {
		return true, true
	}
	switch mode {
	case "r", "rb":
		return true, false
	case "w", "wb", "a", "ab", "x", "xb":
		return false, true
	default:
		return false, false
	}
}

func (parser *pythonIntentParser) joinPath(base string, args pythonCallArgs) (pythonExpr, bool) {
	if len(args.keywords) != 0 {
		return pythonExpr{}, false
	}
	joined := base
	for _, argument := range args.positional {
		if argument.kind != pythonBindingString {
			return pythonExpr{}, false
		}
		var ok bool
		joined, ok = pythonJoinPath(joined, argument.value)
		if !ok {
			return pythonExpr{}, false
		}
	}
	return pythonExpr{kind: pythonBindingPath, value: joined}, true
}

func pythonJoinPath(base string, part string) (string, bool) {
	if len(base) > pythonIntentMaxValueBytes || len(part) > pythonIntentMaxValueBytes {
		return "", false
	}
	if strings.HasPrefix(part, "/") {
		return part, true
	}
	if !pythonJoinFits(len(base), len(part)) {
		return "", false
	}
	joined := path.Join(base, part)
	return joined, len(joined) <= pythonIntentMaxValueBytes
}

func pythonConcatFits(left, right int) bool {
	if left < 0 || right < 0 || left > pythonIntentMaxValueBytes || right > pythonIntentMaxValueBytes {
		return false
	}
	return left+right <= pythonIntentMaxValueBytes
}

func pythonJoinFits(left, right int) bool {
	if left < 0 || right < 0 || left > pythonIntentMaxValueBytes || right > pythonIntentMaxValueBytes {
		return false
	}
	if left == 0 || right == 0 {
		return true
	}
	return left+right+1 <= pythonIntentMaxValueBytes
}

func (parser *pythonIntentParser) appendIntent(pathValue string, operation contextapi.ResourceOperation) bool {
	if len(parser.intents) >= pythonIntentMaxIntents {
		return false
	}
	parser.intents = append(parser.intents, pythonFileIntent{path: pathValue, operation: operation})
	return true
}

func (parser *pythonIntentParser) bind(name string, binding pythonBinding) bool {
	if _, exists := parser.bindings[name]; !exists && len(parser.bindings) >= pythonIntentMaxBindings {
		return false
	}
	parser.bindings[name] = binding
	return true
}

func (parser *pythonIntentParser) assign(name string, expression pythonExpr) bool {
	if binding, exists := parser.bindings[name]; exists {
		switch binding.kind {
		case pythonBindingOpen, pythonBindingPathConstructor, pythonBindingPathlib, pythonBindingBuiltin:
			parser.bindings[name] = pythonBinding{kind: pythonBindingUnknown}
			return true
		}
	}
	return parser.bind(name, pythonBinding{kind: expression.kind, value: expression.value, name: expression.member})
}

func isPythonPathValue(expression pythonExpr) bool {
	return expression.kind == pythonBindingString || expression.kind == pythonBindingPath
}

func isPythonHarmlessStringMethod(name string, args pythonCallArgs) bool {
	if len(args.keywords) != 0 {
		return false
	}
	switch name {
	case "lower", "upper", "casefold":
		return len(args.positional) == 0
	case "strip", "lstrip", "rstrip":
		return len(args.positional) <= 1 && (len(args.positional) == 0 || args.positional[0].kind == pythonBindingString)
	case "removeprefix", "removesuffix":
		return len(args.positional) == 1 && args.positional[0].kind == pythonBindingString
	case "replace":
		if len(args.positional) != 2 && len(args.positional) != 3 {
			return false
		}
		if args.positional[0].kind != pythonBindingString || args.positional[1].kind != pythonBindingString {
			return false
		}
		return len(args.positional) == 2 || args.positional[2].kind == pythonBindingNumber
	default:
		return false
	}
}

func isPythonUnsupportedExpressionName(name string) bool {
	switch name {
	case "if", "for", "while", "def", "class", "try", "with", "match", "async", "else", "elif", "except", "finally", "return", "yield", "raise", "assert", "global", "nonlocal", "del", "pass", "break", "continue", "lambda", "exec", "eval":
		return true
	default:
		return false
	}
}

func isPythonReservedName(name string) bool {
	switch name {
	case "False", "None", "True", "and", "as", "assert", "async", "await", "break", "case", "class", "continue", "def", "del", "elif", "else", "except", "finally", "for", "from", "global", "if", "import", "in", "is", "lambda", "match", "nonlocal", "not", "or", "pass", "raise", "return", "try", "while", "with", "yield":
		return true
	default:
		return false
	}
}

func (parser *pythonIntentParser) looksLikeAssignment() bool {
	return parser.at(pythonTokenName) && parser.peek(1).kind == pythonTokenEqual
}

func (parser *pythonIntentParser) atExpressionEnd() bool {
	return parser.at(pythonTokenSemicolon) || parser.at(pythonTokenNewline) || parser.at(pythonTokenEOF)
}

func (parser *pythonIntentParser) skipNewlines() {
	for parser.at(pythonTokenNewline) {
		parser.position++
	}
}

func (parser *pythonIntentParser) at(kind pythonTokenKind) bool {
	return parser.current().kind == kind
}

func (parser *pythonIntentParser) atName(name string) bool {
	return parser.at(pythonTokenName) && parser.current().value == name
}

func (parser *pythonIntentParser) current() pythonToken {
	if parser.position >= len(parser.tokens) {
		return pythonToken{kind: pythonTokenEOF}
	}
	return parser.tokens[parser.position]
}

func (parser *pythonIntentParser) peek(offset int) pythonToken {
	position := parser.position + offset
	if position >= len(parser.tokens) {
		return pythonToken{kind: pythonTokenEOF}
	}
	return parser.tokens[position]
}

func (parser *pythonIntentParser) require(kind pythonTokenKind, value string) bool {
	if !parser.at(kind) || value != "" && parser.current().value != value {
		return false
	}
	parser.position++
	return true
}
