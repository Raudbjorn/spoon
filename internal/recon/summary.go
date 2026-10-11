// Package recon contains bounded local reconnaissance transforms and reports.
package recon

// Summary is the compact completion report emitted after a recon operation.
type Summary struct {
	Type             string         `json:"type"`
	Mode             string         `json:"mode"`
	Records          int            `json:"records,omitempty"`
	ScanUnits        int            `json:"scanUnits,omitempty"`
	PartialUnits     int            `json:"partialUnits,omitempty"`
	InputErrors      int            `json:"inputErrors,omitempty"`
	Findings         int            `json:"findings,omitempty"`
	DedupeEvictions  int            `json:"dedupeEvictions,omitempty"`
	Candidates       int            `json:"candidates,omitempty"`
	Rewrites         int            `json:"rewrites,omitempty"`
	Unknown          int            `json:"unknown,omitempty"`
	GraphQLQueries   int            `json:"graphqlQueries,omitempty"`
	GraphQLCost      int            `json:"graphqlCost,omitempty"`
	LogicalRESTCalls int            `json:"logicalRestCalls,omitempty"`
	Complete         bool           `json:"complete"`
	Reasons          map[string]int `json:"reasons,omitempty"`
}

// NewSummary creates a complete report for one supported recon mode.
func NewSummary(mode string) Summary {
	if mode != "scan" && mode != "rewrites" {
		panic("invalid recon summary mode")
	}
	return Summary{
		Type:     "recon_summary",
		Mode:     mode,
		Complete: true,
		Reasons:  make(map[string]int),
	}
}

// Merge combines summaries of the same operation mode.
func (s *Summary) Merge(other Summary) {
	if s.Mode != other.Mode {
		panic("cannot merge different recon summary modes")
	}
	if s.Reasons == nil {
		s.Reasons = make(map[string]int, len(other.Reasons))
	}
	s.Records += other.Records
	s.ScanUnits += other.ScanUnits
	s.PartialUnits += other.PartialUnits
	s.InputErrors += other.InputErrors
	s.Findings += other.Findings
	s.DedupeEvictions += other.DedupeEvictions
	s.Candidates += other.Candidates
	s.Rewrites += other.Rewrites
	s.Unknown += other.Unknown
	s.GraphQLQueries += other.GraphQLQueries
	s.GraphQLCost += other.GraphQLCost
	s.LogicalRESTCalls += other.LogicalRESTCalls
	s.Complete = s.Complete && other.Complete
	for reason, count := range other.Reasons {
		s.Reasons[reason] += count
	}
}
