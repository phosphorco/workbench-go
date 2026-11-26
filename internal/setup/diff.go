package setup

import (
	"fmt"
	"strings"
)

// unifiedDiff renders a unified diff (3 lines of context) from the file on
// disk to the projection.
func unifiedDiff(name string, current, want []byte) string {
	before := splitLines(string(current))
	after := splitLines(string(want))
	// A last line without a newline is a different line, rendered with the
	// conventional marker after it.
	if len(current) != 0 && current[len(current)-1] != '\n' {
		before[len(before)-1] += noNewlineSentinel
	}
	if len(want) != 0 && want[len(want)-1] != '\n' {
		after[len(after)-1] += noNewlineSentinel
	}
	operations := diffLines(before, after)
	var output strings.Builder
	fmt.Fprintf(&output, "--- a/%s\n+++ b/%s\n", name, name)
	const context = 3
	for start := 0; start < len(operations); {
		if operations[start].kind == ' ' {
			start++
			continue
		}
		hunkStart := start - context
		if hunkStart < 0 {
			hunkStart = 0
		}
		end := start
		for end < len(operations) {
			if operations[end].kind != ' ' {
				end++
				continue
			}
			run := end
			for run < len(operations) && operations[run].kind == ' ' {
				run++
			}
			if run-end > 2*context || run == len(operations) {
				end += min(context, run-end)
				break
			}
			end = run
		}
		oldStart, newStart, oldCount, newCount := 1, 1, 0, 0
		for _, operation := range operations[:hunkStart] {
			if operation.kind != '+' {
				oldStart++
			}
			if operation.kind != '-' {
				newStart++
			}
		}
		for _, operation := range operations[hunkStart:end] {
			if operation.kind != '+' {
				oldCount++
			}
			if operation.kind != '-' {
				newCount++
			}
		}
		fmt.Fprintf(&output, "@@ -%d,%d +%d,%d @@\n", oldStart, oldCount, newStart, newCount)
		for _, operation := range operations[hunkStart:end] {
			text, noNewline := strings.CutSuffix(operation.text, noNewlineSentinel)
			output.WriteByte(operation.kind)
			// A carriage return is otherwise invisible in a terminal.
			output.WriteString(strings.ReplaceAll(text, "\r", "␍"))
			output.WriteByte('\n')
			if noNewline {
				output.WriteString("\\ No newline at end of file\n")
			}
		}
		start = end
	}
	return output.String()
}

// noNewlineSentinel marks a final line without a newline; it cannot occur in
// a line of text.
const noNewlineSentinel = "\x00no-newline"

type lineOperation struct {
	kind byte
	text string
}

func splitLines(text string) []string {
	if text == "" {
		return nil
	}
	return strings.Split(strings.TrimSuffix(text, "\n"), "\n")
}

// diffLines is a longest-common-subsequence diff; owned files are small.
func diffLines(before, after []string) []lineOperation {
	prefix := 0
	for prefix < len(before) && prefix < len(after) && before[prefix] == after[prefix] {
		prefix++
	}
	suffix := 0
	for suffix < len(before)-prefix && suffix < len(after)-prefix && before[len(before)-1-suffix] == after[len(after)-1-suffix] {
		suffix++
	}
	middleBefore := before[prefix : len(before)-suffix]
	middleAfter := after[prefix : len(after)-suffix]
	rows, columns := len(middleBefore), len(middleAfter)
	table := make([][]int32, rows+1)
	for row := range table {
		table[row] = make([]int32, columns+1)
	}
	for row := rows - 1; row >= 0; row-- {
		for column := columns - 1; column >= 0; column-- {
			if middleBefore[row] == middleAfter[column] {
				table[row][column] = table[row+1][column+1] + 1
			} else if table[row+1][column] >= table[row][column+1] {
				table[row][column] = table[row+1][column]
			} else {
				table[row][column] = table[row][column+1]
			}
		}
	}
	operations := make([]lineOperation, 0, len(before)+len(after))
	for _, line := range before[:prefix] {
		operations = append(operations, lineOperation{' ', line})
	}
	row, column := 0, 0
	for row < rows || column < columns {
		switch {
		case row < rows && column < columns && middleBefore[row] == middleAfter[column]:
			operations = append(operations, lineOperation{' ', middleBefore[row]})
			row++
			column++
		case row < rows && (column == columns || table[row+1][column] >= table[row][column+1]):
			operations = append(operations, lineOperation{'-', middleBefore[row]})
			row++
		default:
			operations = append(operations, lineOperation{'+', middleAfter[column]})
			column++
		}
	}
	for _, line := range before[len(before)-suffix:] {
		operations = append(operations, lineOperation{' ', line})
	}
	return operations
}
