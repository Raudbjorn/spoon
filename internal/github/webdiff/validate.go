package webdiff

import (
	"fmt"
	"strconv"
	"strings"
)

type hunkRange struct {
	start int
	count int
}

// ValidatePatch proves that every declared hunk contains all of its old and
// new rows, including context. Matching REST addition/deletion totals alone
// cannot detect lost context rows. Validate only after joining all pages for
// a file: a page boundary may split a hunk.
func ValidatePatch(patch string) error {
	var oldLeft, newLeft, oldEnd, newEnd int
	var haveHunk, changed, canMarkNewline bool
	for line := range strings.Lines(patch) {
		line = strings.TrimSuffix(line, "\n")
		if strings.HasPrefix(line, "@@") {
			if haveHunk && (oldLeft != 0 || newLeft != 0 || !changed) {
				return fmt.Errorf("GitHub web diff contains an incomplete or unchanged hunk")
			}
			old, new, err := parseHunkHeader(line)
			if err != nil {
				return err
			}
			if haveHunk && (old.start < oldEnd || new.start < newEnd) {
				return fmt.Errorf("GitHub web diff contains overlapping or reordered hunks")
			}
			oldEnd, newEnd = old.start+old.count, new.start+new.count
			oldLeft, newLeft = old.count, new.count
			haveHunk, changed, canMarkNewline = true, false, false
			continue
		}
		if !haveHunk || line == "" {
			return fmt.Errorf("GitHub web diff contains rows without a hunk header")
		}
		if line == `\ No newline at end of file` && canMarkNewline {
			canMarkNewline = false
			continue
		}
		switch line[0] {
		case '+':
			newLeft--
			changed = true
		case '-':
			oldLeft--
			changed = true
		case ' ':
			oldLeft--
			newLeft--
		default:
			return fmt.Errorf("GitHub web diff contains an unrecognized hunk row")
		}
		if oldLeft < 0 || newLeft < 0 {
			return fmt.Errorf("GitHub web diff contains more rows than its hunk declares")
		}
		canMarkNewline = true
	}
	if !haveHunk || !changed || oldLeft != 0 || newLeft != 0 {
		return fmt.Errorf("GitHub web diff lacks complete hunk rows")
	}
	return nil
}

func parseHunkHeader(line string) (hunkRange, hunkRange, error) {
	fields := strings.Fields(line)
	if len(fields) < 4 || fields[0] != "@@" || fields[3] != "@@" {
		return hunkRange{}, hunkRange{}, fmt.Errorf("GitHub web diff has an invalid hunk header")
	}
	old, oldOK := parseHunkRange(fields[1], '-')
	new, newOK := parseHunkRange(fields[2], '+')
	if !oldOK || !newOK {
		return hunkRange{}, hunkRange{}, fmt.Errorf("GitHub web diff has invalid hunk ranges")
	}
	return old, new, nil
}

func parseHunkRange(field string, sign byte) (hunkRange, bool) {
	if len(field) < 2 || field[0] != sign {
		return hunkRange{}, false
	}
	startText, countText, hasCount := strings.Cut(field[1:], ",")
	if !hasCount {
		countText = "1"
	}
	decimal := func(s string) (int, bool) {
		for _, r := range s {
			if r < '0' || r > '9' {
				return 0, false
			}
		}
		n, err := strconv.Atoi(s)
		return n, err == nil
	}
	start, startOK := decimal(startText)
	count, countOK := decimal(countText)
	maxInt := int(^uint(0) >> 1)
	if !startOK || !countOK || count > maxInt-start || (start == 0 && count > 0) {
		return hunkRange{}, false
	}
	return hunkRange{start: start, count: count}, true
}
