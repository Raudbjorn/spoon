package secretscan

import (
	"regexp"
	"strconv"
	"strings"
)

const maxPatchBytes = 64 * 1024

var unifiedHunkHeader = regexp.MustCompile(`^@@ -([0-9]+)(?:,([0-9]+))? \+([0-9]+)(?:,([0-9]+))? @@(?:.*)$`)

type addedLine struct {
	line int
	text string
}

type parsedPatch struct {
	additions []addedLine
	reason    string
}

type hunkState struct {
	oldRemaining int
	newRemaining int
	newLine      int
}

func parsePatch(unit ScanUnit) parsedPatch {
	if len(unit.Patch) == 0 {
		if unit.Additions > 0 {
			return parsedPatch{reason: "empty_patch"}
		}
		return parsedPatch{}
	}
	if len(unit.Patch) > maxPatchBytes {
		return parsedPatch{reason: "patch_too_large"}
	}

	lines := strings.Split(unit.Patch, "\n")
	var (
		state    hunkState
		seenHunk bool
		result   parsedPatch
	)
	for index, raw := range lines {
		if index == len(lines)-1 && raw == "" {
			continue
		}
		line := strings.TrimSuffix(raw, "\r")
		if strings.HasPrefix(line, "@@") {
			if seenHunk && (state.oldRemaining != 0 || state.newRemaining != 0) {
				return parsedPatch{reason: "malformed_patch"}
			}
			parsed, ok := parseHunkHeader(line)
			if !ok {
				return parsedPatch{reason: "malformed_patch"}
			}
			state = parsed
			seenHunk = true
			continue
		}
		if !seenHunk {
			if (strings.HasPrefix(line, "+") && !strings.HasPrefix(line, "+++")) ||
				(strings.HasPrefix(line, "-") && !strings.HasPrefix(line, "---")) ||
				strings.HasPrefix(line, " ") || strings.HasPrefix(line, "\\") {
				return parsedPatch{reason: "malformed_patch"}
			}
			continue
		}

		if line == `\ No newline at end of file` {
			continue
		}
		if line == "" {
			return parsedPatch{reason: "malformed_patch"}
		}
		switch line[0] {
		case '+':
			state.newRemaining--
			if state.newRemaining < 0 || state.newLine < 1 {
				return parsedPatch{reason: "malformed_patch"}
			}
			result.additions = append(result.additions, addedLine{line: state.newLine, text: line[1:]})
			state.newLine++
		case '-':
			state.oldRemaining--
			if state.oldRemaining < 0 {
				return parsedPatch{reason: "malformed_patch"}
			}
		case ' ':
			state.oldRemaining--
			state.newRemaining--
			if state.oldRemaining < 0 || state.newRemaining < 0 || state.newLine < 1 {
				return parsedPatch{reason: "malformed_patch"}
			}
			state.newLine++
		default:
			return parsedPatch{reason: "malformed_patch"}
		}
	}
	if seenHunk && (state.oldRemaining != 0 || state.newRemaining != 0) {
		return parsedPatch{reason: "malformed_patch"}
	}
	if !seenHunk {
		return parsedPatch{reason: "malformed_patch"}
	}
	if unit.Additions > 0 && len(result.additions) == 0 {
		return parsedPatch{reason: "empty_patch"}
	}
	return result
}

func parseHunkHeader(line string) (hunkState, bool) {
	matches := unifiedHunkHeader.FindStringSubmatch(line)
	if matches == nil {
		return hunkState{}, false
	}
	oldStart, ok := parseHunkNumber(matches[1])
	if !ok {
		return hunkState{}, false
	}
	oldCount, ok := parseHunkCount(matches[2])
	if !ok {
		return hunkState{}, false
	}
	newStart, ok := parseHunkNumber(matches[3])
	if !ok {
		return hunkState{}, false
	}
	newCount, ok := parseHunkCount(matches[4])
	if !ok || (oldCount > 0 && oldStart == 0) || (newCount > 0 && newStart == 0) {
		return hunkState{}, false
	}
	return hunkState{oldRemaining: oldCount, newRemaining: newCount, newLine: newStart}, true
}

func parseHunkNumber(value string) (int, bool) {
	number, err := strconv.Atoi(value)
	return number, err == nil && number >= 0
}

func parseHunkCount(value string) (int, bool) {
	if value == "" {
		return 1, true
	}
	return parseHunkNumber(value)
}
