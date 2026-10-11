package recon

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/svnbjrn/spoon/internal/secretscan"
)

const (
	maxSARIFItems     = 25_000
	maxSARIFGzipBytes = 10 * 1024 * 1024
)

var errSARIFOutput = errors.New("unable to write SARIF output")

// SARIFOptions carries run-level metadata for deterministic local SARIF.
type SARIFOptions struct {
	ToolVersion string
	Complete    bool
	Reasons     map[string]int
}

// WriteSARIFFile writes a redacted SARIF 2.1.0 report atomically. It does not
// expose scanner input, patches, or captured values in its errors.
func WriteSARIFFile(path string, rules []secretscan.RuleInfo, findings []secretscan.Finding, opts SARIFOptions) error {
	if path == "" || len(rules) > maxSARIFItems || len(findings) > maxSARIFItems {
		return errSARIFOutput
	}
	document := buildSARIF(rules, findings, opts)
	plain, err := json.Marshal(document)
	if err != nil {
		return errSARIFOutput
	}
	if _, err := deterministicGzip(plain); err != nil {
		return errSARIFOutput
	}
	output := plain
	if strings.HasSuffix(path, ".gz") {
		output, err = deterministicGzip(plain)
		if err != nil {
			return errSARIFOutput
		}
	}
	return atomicWriteSARIF(path, output)
}

type sarifDocument struct {
	Schema  string     `json:"$schema"`
	Version string     `json:"version"`
	Runs    []sarifRun `json:"runs"`
}

type sarifRun struct {
	Tool       sarifTool       `json:"tool"`
	Results    []sarifResult   `json:"results"`
	Properties sarifProperties `json:"properties"`
}

type sarifTool struct {
	Driver sarifDriver `json:"driver"`
}

type sarifDriver struct {
	Name           string      `json:"name"`
	InformationURI string      `json:"informationUri"`
	Version        string      `json:"version"`
	Rules          []sarifRule `json:"rules"`
}

type sarifRule struct {
	ID                   string                    `json:"id"`
	Name                 string                    `json:"name"`
	DefaultConfiguration sarifDefaultConfiguration `json:"defaultConfiguration"`
}

type sarifDefaultConfiguration struct {
	Level string `json:"level"`
}

type sarifResult struct {
	RuleID              string            `json:"ruleId"`
	Level               string            `json:"level"`
	Message             sarifMessage      `json:"message"`
	Locations           []sarifLocation   `json:"locations"`
	PartialFingerprints map[string]string `json:"partialFingerprints"`
}

type sarifMessage struct {
	Text string `json:"text"`
}

type sarifLocation struct {
	PhysicalLocation sarifPhysicalLocation `json:"physicalLocation"`
}

type sarifPhysicalLocation struct {
	ArtifactLocation sarifArtifactLocation `json:"artifactLocation"`
	Region           sarifRegion           `json:"region"`
}

type sarifArtifactLocation struct {
	URI string `json:"uri"`
}

type sarifRegion struct {
	StartLine   int `json:"startLine"`
	StartColumn int `json:"startColumn"`
	EndLine     int `json:"endLine"`
	EndColumn   int `json:"endColumn"`
}

type sarifProperties struct {
	Complete bool           `json:"spoonReconComplete"`
	Reasons  map[string]int `json:"spoonReconPartialReasons"`
}

func buildSARIF(rules []secretscan.RuleInfo, findings []secretscan.Finding, opts SARIFOptions) sarifDocument {
	sortedRules := append([]secretscan.RuleInfo(nil), rules...)
	sort.Slice(sortedRules, func(left, right int) bool { return sortedRules[left].ID < sortedRules[right].ID })
	sarifRules := make([]sarifRule, 0, len(sortedRules))
	for _, rule := range sortedRules {
		sarifRules = append(sarifRules, sarifRule{
			ID:   rule.ID,
			Name: rule.Name,
			DefaultConfiguration: sarifDefaultConfiguration{
				Level: rule.Severity,
			},
		})
	}

	sortedFindings := append([]secretscan.Finding(nil), findings...)
	sort.Slice(sortedFindings, func(left, right int) bool {
		return compareFinding(sortedFindings[left], sortedFindings[right]) < 0
	})
	results := make([]sarifResult, 0, len(sortedFindings))
	for _, finding := range sortedFindings {
		results = append(results, sarifResult{
			RuleID: finding.RuleID,
			Level:  finding.Severity,
			Message: sarifMessage{
				Text: "rule " + finding.RuleID + " matched at " + finding.Repository + ":" + finding.Path + ":" + strconv.Itoa(finding.StartLine),
			},
			Locations: []sarifLocation{{
				PhysicalLocation: sarifPhysicalLocation{
					ArtifactLocation: sarifArtifactLocation{URI: sarifArtifactURI(finding)},
					Region: sarifRegion{
						StartLine:   finding.StartLine,
						StartColumn: finding.StartColumn,
						EndLine:     finding.EndLine,
						EndColumn:   finding.EndColumn,
					},
				},
			}},
			PartialFingerprints: map[string]string{"spoon/recon/v1": finding.Fingerprint},
		})
	}
	reasons := make(map[string]int, len(opts.Reasons))
	for reason, count := range opts.Reasons {
		reasons[reason] = count
	}
	return sarifDocument{
		Schema:  "https://json.schemastore.org/sarif-2.1.0.json",
		Version: "2.1.0",
		Runs: []sarifRun{{
			Tool: sarifTool{Driver: sarifDriver{
				Name:           "spoon",
				InformationURI: "https://github.com/Raudbjorn/spoon",
				Version:        opts.ToolVersion,
				Rules:          sarifRules,
			}},
			Results: results,
			Properties: sarifProperties{
				Complete: opts.Complete,
				Reasons:  reasons,
			},
		}},
	}
}

func compareFinding(left, right secretscan.Finding) int {
	if left.Repository != right.Repository {
		if left.Repository < right.Repository {
			return -1
		}
		return 1
	}
	if left.Path != right.Path {
		if left.Path < right.Path {
			return -1
		}
		return 1
	}
	if left.StartLine != right.StartLine {
		if left.StartLine < right.StartLine {
			return -1
		}
		return 1
	}
	if left.StartColumn != right.StartColumn {
		if left.StartColumn < right.StartColumn {
			return -1
		}
		return 1
	}
	if left.RuleID != right.RuleID {
		if left.RuleID < right.RuleID {
			return -1
		}
		return 1
	}
	if left.Fingerprint < right.Fingerprint {
		return -1
	}
	if left.Fingerprint > right.Fingerprint {
		return 1
	}
	return 0
}

func sarifArtifactURI(finding secretscan.Finding) string {
	artifact := url.URL{
		Scheme: "spoon",
		Host:   "repository",
		Path:   "/" + finding.Repository + "/" + finding.Path,
	}
	query := url.Values{}
	query.Set("fork", finding.Fork)
	query.Set("snapshot", finding.SnapshotID)
	artifact.RawQuery = query.Encode()
	return artifact.String()
}

func deterministicGzip(data []byte) ([]byte, error) {
	var output boundedBuffer
	output.limit = maxSARIFGzipBytes
	writer := gzip.NewWriter(&output)
	writer.ModTime = time.Unix(0, 0).UTC()
	writer.OS = 255
	if _, err := writer.Write(data); err != nil {
		_ = writer.Close()
		return nil, err
	}
	if err := writer.Close(); err != nil || output.exceeded {
		return nil, errSARIFOutput
	}
	return output.Buffer.Bytes(), nil
}

type boundedBuffer struct {
	bytes.Buffer
	limit    int
	exceeded bool
}

func (buffer *boundedBuffer) Write(value []byte) (int, error) {
	remaining := buffer.limit - buffer.Len()
	if remaining <= 0 {
		buffer.exceeded = true
		return 0, errSARIFOutput
	}
	if len(value) > remaining {
		_, _ = buffer.Buffer.Write(value[:remaining])
		buffer.exceeded = true
		return remaining, errSARIFOutput
	}
	return buffer.Buffer.Write(value)
}

func atomicWriteSARIF(path string, data []byte) error {
	if info, err := os.Lstat(path); err == nil {
		if info.Mode()&os.ModeSymlink != 0 {
			return errSARIFOutput
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return errSARIFOutput
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return errSARIFOutput
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return errSARIFOutput
	}
	if count, err := temporary.Write(data); err != nil || count != len(data) {
		_ = temporary.Close()
		return errSARIFOutput
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return errSARIFOutput
	}
	if err := temporary.Close(); err != nil {
		return errSARIFOutput
	}
	if info, err := os.Lstat(path); err == nil {
		if info.Mode()&os.ModeSymlink != 0 {
			return errSARIFOutput
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return errSARIFOutput
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return errSARIFOutput
	}
	return nil
}
