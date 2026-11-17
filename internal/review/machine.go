package review

import (
	"encoding/json"
	"fmt"
	"strings"
)

// MachineBlockVersion is the schema version of the hidden JSON contract.
// Bump it only for breaking changes; agents pin on the marker prefix.
const MachineBlockVersion = 1

// machineMarker opens the hidden HTML comment every review body carries.
// It is a stable, versioned delimiter an agent can locate without parsing
// markdown, and it never renders in the GitHub UI.
const machineMarker = "<!-- ekravo-review:v1"

// MachineStatus is the closed enum an automated reader gates on. It answers
// "what should I do next?" without interpreting prose:
//   - action_required: fix the blocking findings and push again
//   - pass: nothing to fix; the reviewed head is clean
//   - pass_with_suggestions: mergeable; notes are optional — do not push more commits for them
//   - incomplete: the review did not cover the whole diff; do not treat as a pass
//   - error: the review failed; retry
type MachineStatus string

const (
	StatusActionRequired  MachineStatus = "action_required"
	StatusPass            MachineStatus = "pass"
	StatusPassSuggestions MachineStatus = "pass_with_suggestions"
	StatusIncomplete      MachineStatus = "incomplete"
	StatusError           MachineStatus = "error"
)

// MachineFinding is one finding in the machine block.
type MachineFinding struct {
	ID         string   `json:"id"`
	File       string   `json:"file"`
	Line       int      `json:"line"`
	Severity   Severity `json:"severity"`
	Category   string   `json:"category"`
	Blocking   bool     `json:"blocking"`
	Confidence float64  `json:"confidence"`
	Title      string   `json:"title"`
	Detail     string   `json:"detail,omitempty"`
	Fix        string   `json:"fix"`
	Carried    bool     `json:"carried,omitempty"`
}

// MachineCoverage tells the reader how much of the PR this review saw.
type MachineCoverage struct {
	Complete       bool   `json:"complete"`
	Truncated      bool   `json:"truncated"`
	SectionsTotal  int    `json:"sections_total"`
	SectionsFailed int    `json:"sections_failed"`
	Incremental    bool   `json:"incremental"`
	SinceSHA       string `json:"since_sha,omitempty"`
	Dropped        int    `json:"dropped"`
	InlinePosted   int    `json:"inline_posted"`
}

// MachineReview is the JSON payload inside the hidden marker. Everything an
// agent needs — verdict, worklist, done-condition, coverage — in one parse.
type MachineReview struct {
	Schema      int              `json:"schema"`
	PR          int              `json:"pr"`
	Repo        string           `json:"repo,omitempty"`
	HeadSHA     string           `json:"head_sha"`
	Status      MachineStatus    `json:"status"`
	Verdict     string           `json:"verdict"`
	Blocking    int              `json:"blocking"`
	Counts      map[Severity]int `json:"counts"`
	Coverage    MachineCoverage  `json:"coverage"`
	Findings    []MachineFinding `json:"findings"`
	ResolvedIDs []string         `json:"resolved_since_last_round,omitempty"`
}

// StatusOf maps a verdict and coverage flags to the machine status enum.
// A partial review can never report a pass: green on part of a diff is not
// green, and an agent must keep the PR open until a complete pass exists.
func StatusOf(v Verdict, truncated bool, failed int) MachineStatus {
	if truncated || failed > 0 {
		return StatusIncomplete
	}
	switch v.Decision {
	case "changes":
		return StatusActionRequired
	case "suggestions":
		return StatusPassSuggestions
	case "approve":
		return StatusPass
	default:
		return StatusIncomplete
	}
}

// MachineBlock renders the hidden JSON comment for a summary. It is placed
// at the very top of the body so a byte-budget truncation can never cut it.
func MachineBlock(in SummaryInput, inlinePosted int, resolved []string) string {
	status := StatusOf(in.Verdict, in.Truncated, in.Failed)
	m := MachineReview{
		Schema:   MachineBlockVersion,
		PR:       in.Meta.Number,
		Repo:     in.Meta.Repo,
		HeadSHA:  in.Commit,
		Status:   status,
		Verdict:  in.Verdict.Decision,
		Blocking: in.Verdict.Blocking,
		Counts:   in.Verdict.Counts,
		Coverage: MachineCoverage{
			Complete:       status == StatusPass || status == StatusPassSuggestions || status == StatusActionRequired,
			Truncated:      in.Truncated,
			SectionsTotal:  in.Sections,
			SectionsFailed: in.Failed,
			Incremental:    in.Incremental,
			SinceSHA:       in.SinceSHA,
			Dropped:        in.Dropped,
			InlinePosted:   inlinePosted,
		},
		Findings:    make([]MachineFinding, 0, len(in.Findings)),
		ResolvedIDs: resolved,
	}
	for _, f := range in.Findings {
		m.Findings = append(m.Findings, MachineFinding{
			ID:         f.ID(),
			File:       f.File,
			Line:       f.Line,
			Severity:   f.Severity,
			Category:   f.Category,
			Blocking:   Blocking(f, in.Verdict.BlockOn),
			Confidence: f.Confidence,
			Title:      f.Title,
			Detail:     f.Detail,
			Fix:        f.Fix,
			Carried:    f.Carried,
		})
	}
	data, err := json.Marshal(m)
	if err != nil {
		// Marshal of plain structs cannot fail in practice; fall back to a
		// minimal error block rather than emitting broken JSON.
		return machineMarker + "\n{\"schema\":1,\"status\":\"error\"}\n-->\n"
	}
	// A finding could contain the comment terminator inside model text;
	// neutralize it so the marker always closes exactly once.
	data = []byte(strings.ReplaceAll(string(data), "-->", "-- >"))
	return machineMarker + "\n" + string(data) + "\n-->\n"
}

// FailureBlock renders the machine contract for a failed review, so an agent
// can distinguish "review errored, retry" from a verdict without prose.
func FailureBlock(pr int, repo, reason string) string {
	data, _ := json.Marshal(struct {
		Schema int           `json:"schema"`
		PR     int           `json:"pr"`
		Repo   string        `json:"repo,omitempty"`
		Status MachineStatus `json:"status"`
		Error  string        `json:"error"`
	}{MachineBlockVersion, pr, repo, StatusError, reason})
	return fmt.Sprintf("%s\n%s\n-->\n", machineMarker, data)
}
