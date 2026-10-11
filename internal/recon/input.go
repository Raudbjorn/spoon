package recon

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"time"

	"github.com/svnbjrn/spoon/internal/secretscan"
)

const maxNDJSONLineBytes = 64 * 1024 * 1024

// inputError identifies malformed or unusable NDJSON input. Its message is
// deliberately content-free because input lines can contain scanned material.
type inputError struct{}

func (*inputError) Error() string { return "invalid recon scan input" }

// IsBadInput reports whether ScanForkNDJSON rejected input rather than failing
// to read or emit it.
func IsBadInput(err error) bool {
	var target *inputError
	return errors.As(err, &target)
}

type inputReadError struct{}

func (*inputReadError) Error() string { return "unable to read recon scan input" }

type outputError struct{}

func (*outputError) Error() string { return "unable to emit recon finding" }

type forkWire struct {
	ID            string          `json:"id"`
	Owner         string          `json:"owner"`
	Name          string          `json:"name"`
	PushedAt      string          `json:"pushed_at"`
	Upstream      string          `json:"upstream"`
	Lineage       *lineageWire    `json:"lineage"`
	T2            *t2Wire         `json:"t2"`
	BudgetSkipped bool            `json:"budget_skipped"`
	Error         json.RawMessage `json:"error"`
}

type lineageWire struct {
	NetworkRoot string `json:"networkRoot"`
}

type t2Wire struct {
	Files                    *[]fileWire   `json:"files"`
	FilesUnfetched           bool          `json:"files_unfetched"`
	FilesTruncated           bool          `json:"files_truncated"`
	PatchSkippedReason       string        `json:"patch_skipped_reason"`
	Commits                  *[]commitWire `json:"commits"`
	CommitFilesComplete      *bool         `json:"commit_files_complete"`
	CommitFilesSkippedReason string        `json:"commit_files_skipped_reason"`
}

type commitWire struct {
	SHA   string      `json:"sha"`
	Files *[]fileWire `json:"files"`
}

type fileWire struct {
	Path        string  `json:"path"`
	Patch       *string `json:"patch"`
	PatchSource string  `json:"patchSource"`
	Additions   int     `json:"additions"`
}

// ScanForkNDJSON transforms detailed spn fork NDJSON into bounded scanner
// units. It never returns scanned content in an error.
func ScanForkNDJSON(ctx context.Context, r io.Reader, session *secretscan.Session, emit func(secretscan.Finding) error) (Summary, error) {
	summary := NewSummary("scan")
	if r == nil || session == nil || emit == nil {
		return summary, errors.New("invalid recon scan dependencies")
	}
	before := session.Stats()
	ready := 0
	err := forEachNDJSONLine(r, func(line []byte) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if len(bytes.TrimSpace(line)) == 0 {
			return nil
		}
		summary.Records++
		var record forkWire
		if err := json.Unmarshal(line, &record); err != nil {
			return &inputError{}
		}
		if isForkErrorRecord(record.Error) {
			summary.InputErrors++
			markPartial(&summary, "input_error", false)
			return nil
		}
		recordReady, err := scanRecord(ctx, record, session, emit, &summary)
		if err != nil {
			return err
		}
		ready += recordReady
		return nil
	})
	stats := session.Stats()
	summary.DedupeEvictions = stats.DedupeEvictions - before.DedupeEvictions
	if err != nil {
		if IsBadInput(err) {
			return summary, err
		}
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return summary, err
		}
		return summary, err
	}
	if ready == 0 {
		return summary, &inputError{}
	}
	return summary, nil
}

func isForkErrorRecord(raw json.RawMessage) bool {
	return len(raw) != 0 && string(raw) != "null"
}

func scanRecord(ctx context.Context, record forkWire, session *secretscan.Session, emit func(secretscan.Finding) error, summary *Summary) (int, error) {
	if record.BudgetSkipped {
		markPartial(summary, "budget_skipped", false)
	}
	if record.T2 == nil {
		markPartial(summary, "missing_t2", true)
		return 0, nil
	}
	t2 := record.T2
	if t2.FilesUnfetched {
		markPartial(summary, "files_unfetched", true)
	}
	if t2.FilesTruncated {
		markPartial(summary, "files_truncated", false)
	}
	if t2.PatchSkippedReason != "" || t2.CommitFilesSkippedReason != "" {
		markPartial(summary, "patch_skipped", false)
	}
	if t2.CommitFilesComplete == nil {
		markPartial(summary, "missing_detail", true)
	} else if !*t2.CommitFilesComplete {
		markPartial(summary, "commit_files_incomplete", false)
	}

	repository, networkRoot, identityOK := recordIdentity(record)
	ready := 0
	if t2.Files == nil {
		markPartial(summary, "missing_files", true)
	} else if !identityOK {
		markPartial(summary, "invalid_provenance", true)
	} else if snapshot, ok := aggregateSnapshot(record.PushedAt); !ok {
		markPartial(summary, "invalid_provenance", true)
	} else {
		for _, file := range *t2.Files {
			if !validFilePath(file.Path) {
				markPartial(summary, "invalid_provenance", true)
				continue
			}
			unit, ok := aggregateUnit(repository, networkRoot, snapshot, file)
			if !ok {
				markPartial(summary, "missing_patch", true)
				continue
			}
			ready++
			if err := scanUnit(ctx, session, unit, emit, summary); err != nil {
				return ready, err
			}
		}
	}

	if t2.Commits == nil {
		markPartial(summary, "missing_detail", true)
		return ready, nil
	}
	if !identityOK {
		for range *t2.Commits {
			markPartial(summary, "invalid_provenance", true)
		}
		return ready, nil
	}
	for _, commit := range *t2.Commits {
		if !validCommitSHA(commit.SHA) {
			markPartial(summary, "invalid_provenance", true)
			continue
		}
		if commit.Files == nil {
			markPartial(summary, "missing_files", true)
			continue
		}
		for _, file := range *commit.Files {
			if !validFilePath(file.Path) {
				markPartial(summary, "invalid_provenance", true)
				continue
			}
			unit, ok := commitUnit(repository, networkRoot, commit.SHA, file)
			if !ok {
				markPartial(summary, "missing_patch", true)
				continue
			}
			ready++
			if err := scanUnit(ctx, session, unit, emit, summary); err != nil {
				return ready, err
			}
		}
	}
	return ready, nil
}

func recordIdentity(record forkWire) (repository, networkRoot string, ok bool) {
	repository = record.ID
	if repository == "" {
		if validIdentity(record.Owner) && validIdentity(record.Name) {
			repository = record.Owner + "/" + record.Name
		} else {
			return "", "", false
		}
	}
	networkRoot = record.Upstream
	if networkRoot == "" && record.Lineage != nil {
		networkRoot = record.Lineage.NetworkRoot
	}
	if !validIdentity(repository) || strings.IndexByte(networkRoot, 0) >= 0 {
		return "", "", false
	}
	return repository, networkRoot, true
}

func validIdentity(value string) bool {
	return strings.TrimSpace(value) != "" && strings.IndexByte(value, 0) < 0
}

func validFilePath(value string) bool {
	return value != "" && strings.IndexByte(value, 0) < 0
}

func aggregateSnapshot(value string) (string, bool) {
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil || parsed.IsZero() {
		return "", false
	}
	return parsed.UTC().Format(time.RFC3339Nano), true
}

func validCommitSHA(value string) bool {
	if len(value) < 40 || len(value) > 64 {
		return false
	}
	for _, char := range value {
		if char < '0' || char > '9' {
			if char < 'a' || char > 'f' {
				return false
			}
		}
	}
	return true
}

func aggregateUnit(repository, networkRoot, snapshot string, file fileWire) (secretscan.ScanUnit, bool) {
	return wireUnit(secretscan.SourceCurrentCompare, repository, networkRoot, repository, snapshot, "", file)
}

func commitUnit(repository, networkRoot, sha string, file fileWire) (secretscan.ScanUnit, bool) {
	return wireUnit(secretscan.SourceCurrentCommit, repository, networkRoot, repository, sha, sha, file)
}

func wireUnit(source secretscan.Source, repository, networkRoot, fork, snapshot, sha string, file fileWire) (secretscan.ScanUnit, bool) {
	if file.Path == "" || strings.IndexByte(file.Path, 0) >= 0 {
		return secretscan.ScanUnit{}, false
	}
	if file.Additions > 0 && (file.Patch == nil || *file.Patch == "") {
		return secretscan.ScanUnit{}, false
	}
	patch := ""
	if file.Patch != nil {
		patch = *file.Patch
	}
	return secretscan.ScanUnit{
		Source:      source,
		Repository:  repository,
		NetworkRoot: networkRoot,
		Fork:        fork,
		SnapshotID:  snapshot,
		CommitSHA:   sha,
		Path:        file.Path,
		Patch:       patch,
		PatchSource: file.PatchSource,
		Additions:   file.Additions,
		Complete:    true,
	}, true
}

func scanUnit(ctx context.Context, session *secretscan.Session, unit secretscan.ScanUnit, emit func(secretscan.Finding) error, summary *Summary) error {
	summary.ScanUnits++
	result := session.Scan(ctx, unit)
	if !result.Complete {
		markPartial(summary, result.IncompleteReason, true)
	}
	for _, finding := range result.Findings {
		if err := emit(finding); err != nil {
			return &outputError{}
		}
		summary.Findings++
	}
	return nil
}

func markPartial(summary *Summary, reason string, countUnit bool) {
	summary.Complete = false
	if reason != "" {
		summary.Reasons[reason]++
	}
	if countUnit {
		summary.PartialUnits++
	}
}

func forEachNDJSONLine(r io.Reader, fn func([]byte) error) error {
	reader := bufio.NewReaderSize(r, 64*1024)
	var line []byte
	for {
		fragment, err := reader.ReadSlice('\n')
		if len(line)+len(fragment) > maxNDJSONLineBytes+2 {
			return &inputError{}
		}
		line = append(line, fragment...)
		switch {
		case err == nil:
			if err := consumeNDJSONLine(line, fn); err != nil {
				return err
			}
			line = nil
		case errors.Is(err, bufio.ErrBufferFull):
			continue
		case errors.Is(err, io.EOF):
			if len(line) != 0 {
				return consumeNDJSONLine(line, fn)
			}
			return nil
		default:
			return &inputReadError{}
		}
	}
}

func consumeNDJSONLine(line []byte, fn func([]byte) error) error {
	line = trimLineEnding(line)
	if len(line) > maxNDJSONLineBytes {
		return &inputError{}
	}
	return fn(line)
}

func trimLineEnding(line []byte) []byte {
	if len(line) > 0 && line[len(line)-1] == '\n' {
		line = line[:len(line)-1]
	}
	if len(line) > 0 && line[len(line)-1] == '\r' {
		line = line[:len(line)-1]
	}
	return line
}
