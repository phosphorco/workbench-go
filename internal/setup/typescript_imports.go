package setup

// observedTypeScriptImport is the source-level evidence needed by closure
// diagnostics. The scanner deliberately recognizes only module-loading syntax;
// quoted prose, comments, and unrelated string literals are not imports.
type observedTypeScriptImport struct {
	specifier string
	line      int
	kind      string
}

type typeScriptTokenKind uint8

const (
	typeScriptIdentifier typeScriptTokenKind = iota
	typeScriptString
	typeScriptPunctuation
)

type typeScriptToken struct {
	kind typeScriptTokenKind
	text string
	line int
}

func observeTypeScriptImports(source []byte) []observedTypeScriptImport {
	tokens := lexTypeScript(source)
	imports := make([]observedTypeScriptImport, 0)
	for index, token := range tokens {
		if token.kind != typeScriptIdentifier {
			continue
		}
		switch token.text {
		case "import":
			if specifier, exists := immediateImportSpecifier(tokens, index+1); exists {
				if specifier.kind == "" {
					specifier.kind = "import-statement"
				}
				imports = append(imports, specifier)
				continue
			}
			if specifier, exists := importEqualsSpecifier(tokens, index+1); exists {
				specifier.kind = "require-call"
				imports = append(imports, specifier)
				continue
			}
			if specifier, exists := fromSpecifier(tokens, index+1); exists {
				specifier.kind = "import-statement"
				imports = append(imports, specifier)
			}
		case "export":
			if specifier, exists := fromSpecifier(tokens, index+1); exists {
				specifier.kind = "import-statement"
				imports = append(imports, specifier)
			}
		}
	}
	return imports
}

func immediateImportSpecifier(tokens []typeScriptToken, index int) (observedTypeScriptImport, bool) {
	if index >= len(tokens) {
		return observedTypeScriptImport{}, false
	}
	if tokens[index].kind == typeScriptString {
		return importFromToken(tokens[index])
	}
	if tokens[index].kind == typeScriptPunctuation && tokens[index].text == "." {
		return observedTypeScriptImport{}, false
	}
	if tokens[index].kind != typeScriptPunctuation || tokens[index].text != "(" || index+1 >= len(tokens) || tokens[index+1].kind != typeScriptString {
		return observedTypeScriptImport{}, false
	}
	result, exists := importFromToken(tokens[index+1])
	result.kind = "dynamic-import"
	return result, exists
}

func importEqualsSpecifier(tokens []typeScriptToken, index int) (observedTypeScriptImport, bool) {
	if index < len(tokens) && tokens[index].kind == typeScriptIdentifier && tokens[index].text == "type" {
		index++
	}
	if index+4 >= len(tokens) || tokens[index].kind != typeScriptIdentifier {
		return observedTypeScriptImport{}, false
	}
	if tokens[index+1].kind != typeScriptPunctuation || tokens[index+1].text != "=" || tokens[index+2].kind != typeScriptIdentifier || tokens[index+2].text != "require" || tokens[index+3].kind != typeScriptPunctuation || tokens[index+3].text != "(" || tokens[index+4].kind != typeScriptString {
		return observedTypeScriptImport{}, false
	}
	return importFromToken(tokens[index+4])
}

func fromSpecifier(tokens []typeScriptToken, index int) (observedTypeScriptImport, bool) {
	depth := 0
	for ; index < len(tokens); index++ {
		token := tokens[index]
		if token.kind == typeScriptPunctuation {
			switch token.text {
			case "(", "[", "{":
				depth++
			case ")", "]", "}":
				if depth > 0 {
					depth--
				}
			case ";":
				if depth == 0 {
					return observedTypeScriptImport{}, false
				}
			}
			continue
		}
		if depth == 0 && token.kind == typeScriptIdentifier && token.text == "from" && index+1 < len(tokens) && tokens[index+1].kind == typeScriptString {
			return importFromToken(tokens[index+1])
		}
	}
	return observedTypeScriptImport{}, false
}

func importFromToken(token typeScriptToken) (observedTypeScriptImport, bool) {
	if token.text == "" {
		return observedTypeScriptImport{}, false
	}
	return observedTypeScriptImport{specifier: token.text, line: token.line}, true
}

func lexTypeScript(source []byte) []typeScriptToken {
	tokens := make([]typeScriptToken, 0)
	line := 1
	templateDepths := make([]int, 0)
	for index := 0; index < len(source); {
		if len(templateDepths) != 0 && templateDepths[len(templateDepths)-1] == 0 {
			// Template text is inert, but ${...} resumes ordinary TypeScript
			// lexing. A nested dynamic import is therefore observed without
			// treating the surrounding prose as source evidence.
			switch source[index] {
			case '\\':
				if index+1 < len(source) {
					if source[index+1] == '\n' {
						line++
					}
					index += 2
				} else {
					index++
				}
			case '\n':
				line++
				index++
			case '`':
				templateDepths = templateDepths[:len(templateDepths)-1]
				index++
			case '$':
				if index+1 < len(source) && source[index+1] == '{' {
					templateDepths[len(templateDepths)-1] = 1
					index += 2
				} else {
					index++
				}
			default:
				index++
			}
			continue
		}
		switch source[index] {
		case ' ', '\t', '\r', '\f':
			index++
		case '\n':
			line++
			index++
		case '/':
			if index+1 < len(source) && source[index+1] == '/' {
				index += 2
				for index < len(source) && source[index] != '\n' {
					index++
				}
				continue
			}
			if index+1 < len(source) && source[index+1] == '*' {
				index += 2
				for index < len(source) {
					if source[index] == '\n' {
						line++
					}
					if index+1 < len(source) && source[index] == '*' && source[index+1] == '/' {
						index += 2
						break
					}
					index++
				}
				continue
			}
			if canStartTypeScriptRegex(tokens) {
				index++
				inCharacterClass := false
				for index < len(source) {
					if source[index] == '\\' && index+1 < len(source) {
						index += 2
						continue
					}
					if source[index] == '\n' || source[index] == '\r' {
						break
					}
					if source[index] == '[' {
						inCharacterClass = true
					} else if source[index] == ']' {
						inCharacterClass = false
					} else if source[index] == '/' && !inCharacterClass {
						index++
						for index < len(source) && isTypeScriptIdentifierContinue(source[index]) {
							index++
						}
						break
					}
					index++
				}
				continue
			}
			tokens = append(tokens, typeScriptToken{kind: typeScriptPunctuation, text: "/", line: line})
			index++
		case '\'', '"':
			quote := source[index]
			startLine := line
			index++
			value := make([]byte, 0)
			for index < len(source) {
				if source[index] == '\\' && index+1 < len(source) {
					index++
					value = append(value, source[index])
					index++
					continue
				}
				if source[index] == quote {
					index++
					break
				}
				if source[index] == '\n' {
					line++
				}
				value = append(value, source[index])
				index++
			}
			tokens = append(tokens, typeScriptToken{kind: typeScriptString, text: string(value), line: startLine})
		case '`':
			templateDepths = append(templateDepths, 0)
			index++
		default:
			if isTypeScriptIdentifierStart(source[index]) {
				start := index
				index++
				for index < len(source) && isTypeScriptIdentifierContinue(source[index]) {
					index++
				}
				tokens = append(tokens, typeScriptToken{kind: typeScriptIdentifier, text: string(source[start:index]), line: line})
				continue
			}
			if len(templateDepths) != 0 {
				switch source[index] {
				case '{':
					templateDepths[len(templateDepths)-1]++
				case '}':
					templateDepths[len(templateDepths)-1]--
					if templateDepths[len(templateDepths)-1] == 0 {
						index++
						continue
					}
				}
			}
			tokens = append(tokens, typeScriptToken{kind: typeScriptPunctuation, text: string(source[index]), line: line})
			index++
		}
	}
	return tokens
}

func isTypeScriptIdentifierStart(value byte) bool {
	return value == '_' || value == '$' || value >= 'a' && value <= 'z' || value >= 'A' && value <= 'Z'
}

func isTypeScriptIdentifierContinue(value byte) bool {
	return isTypeScriptIdentifierStart(value) || value >= '0' && value <= '9'
}

func canStartTypeScriptRegex(tokens []typeScriptToken) bool {
	if len(tokens) == 0 {
		return true
	}
	previous := tokens[len(tokens)-1]
	if previous.kind == typeScriptPunctuation {
		switch previous.text {
		case "(", "[", "{", ",", ";", ":", "=", "!", "?", "&", "|", "+", "-", "*", "%", "~", "^", "<", ">":
			return true
		}
		return false
	}
	if previous.kind == typeScriptIdentifier {
		switch previous.text {
		case "await", "case", "delete", "do", "else", "in", "instanceof", "new", "of", "return", "throw", "typeof", "void", "yield":
			return true
		}
	}
	return false
}
