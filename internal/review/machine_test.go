package review

import (
	"encoding/json"
	"strings"
	"testing"
)

// extractMachine parses the hidden contract exactly the way an agent would:
// locate the stable markers and JSON-decode what is between them.
func extractMachine(t *testing.T, body string) MachineReview {
	t.Helper()
	start := strings.Index(body, machineMarker)
	if start < 0 {
		t.Fatalf("no machine marker in body:\n%s", body)
	}
	rest := body[start+len(machineMarker):]
	end := strings.Index(rest, "-->")
	if end < 0 {
		t.Fatalf("machine marker not closed:\n%s", body)
	}
	var m MachineReview
	if err := json.Unmarshal([]byte(strings.TrimSpace(rest[:end])), &m); err != nil {
		t.Fatalf("machine block is not valid JSON: %v\n%s", err, rest[:end])
	}
	return m
}

func TestMachineBlockOnCleanReview(t *testing.T) {
	in := SummaryInput{
		Meta:    Meta{Number: 42, Repo: "o/r", Title: "Clean"},
		Verdict: Decide(nil, "high"),
		Commit:  "0123456789abcdef0123456789abcdef01234567",
	}
	out := Summary(in)
	m := extractMachine(t, out)
	if m.Status != StatusPass {
		t.Errorf("status = %s, want pass", m.Status)
	}
	if m.PR != 42 || m.HeadSHA != in.Commit {
		t.Errorf("pr/sha = %d/%s", m.PR, m.HeadSHA)
	}
	if !m.Coverage.Complete {
		t.Errorf("clean review should be complete: %+v", m.Coverage)
	}
	if len(m.Findings) != 0 {
		t.Errorf("findings = %+v", m.Findings)
	}
	// The marker must lead the body so truncation cannot cut it.
	if !strings.HasPrefix(out, machineMarker) {
		t.Errorf("machine block is not first:\n%.120s", out)
	}
}

func TestMachineBlockActionRequired(t *testing.T) {
	f := Finding{File: "a.go", Line: 2, Severity: High, Category: "bug", Title: "Nil deref", Fix: "Check for nil.", Confidence: 0.9}
	low := Finding{File: "a.go", Line: 3, Severity: Low, Category: "quality", Title: "Naming", Fix: "Rename.", Confidence: 0.8}
	in := SummaryInput{
		Meta:     Meta{Number: 7, Repo: "o/r"},
		Findings: []Finding{f, low},
		Verdict:  Decide([]Finding{f, low}, "high"),
		Commit:   "abc",
	}
	m := extractMachine(t, Summary(in))
	if m.Status != StatusActionRequired || m.Blocking != 1 {
		t.Fatalf("status/blocking = %s/%d", m.Status, m.Blocking)
	}
	if len(m.Findings) != 2 {
		t.Fatalf("findings = %d", len(m.Findings))
	}
	byID := map[string]MachineFinding{}
	for _, mf := range m.Findings {
		if mf.ID == "" {
			t.Errorf("finding without ID: %+v", mf)
		}
		byID[mf.ID] = mf
	}
	if !byID[f.ID()].Blocking {
		t.Errorf("high finding should be blocking")
	}
	if byID[low.ID()].Blocking {
		t.Errorf("low finding under block_on=high must be non-blocking")
	}
}

func TestMachineStatusNeverPassesOnPartial(t *testing.T) {
	clean := Decide(nil, "high")
	if got := StatusOf(clean, true, 0); got != StatusIncomplete {
		t.Errorf("truncated clean = %s, want incomplete", got)
	}
	if got := StatusOf(clean, false, 1); got != StatusIncomplete {
		t.Errorf("failed-section clean = %s, want incomplete", got)
	}
	sugg := Decide([]Finding{{Severity: Low}}, "high")
	if got := StatusOf(sugg, false, 0); got != StatusPassSuggestions {
		t.Errorf("suggestions = %s", got)
	}
	if got := StatusOf(Verdict{Decision: "weird"}, false, 0); got != StatusIncomplete {
		t.Errorf("unknown decision = %s, want incomplete", got)
	}
}

func TestPartialApprovalCalloutIsNotGreen(t *testing.T) {
	out := Summary(SummaryInput{
		Meta:      Meta{Number: 1, Repo: "o/r"},
		Verdict:   Decide(nil, "high"),
		Truncated: true,
	})
	if strings.Contains(out, "Safe to merge") {
		t.Errorf("partial review must not claim safe to merge:\n%s", out)
	}
	if !strings.Contains(out, "Not all green yet") {
		t.Errorf("partial review must warn:\n%s", out)
	}
	m := extractMachine(t, out)
	if m.Status != StatusIncomplete || m.Coverage.Complete {
		t.Errorf("machine status = %s complete=%v", m.Status, m.Coverage.Complete)
	}
}

func TestSuggestionsCalloutStopsFixLoops(t *testing.T) {
	out := Summary(SummaryInput{
		Meta:     Meta{Number: 1, Repo: "o/r"},
		Findings: []Finding{{File: "a.go", Line: 2, Severity: Low, Category: "quality", Title: "nit", Fix: "x"}},
		Verdict:  Decide([]Finding{{Severity: Low}}, "high"),
	})
	if !strings.Contains(out, "do not push further commits") {
		t.Errorf("suggestions callout must tell agents to stop:\n%s", out)
	}
	m := extractMachine(t, out)
	if m.Status != StatusPassSuggestions {
		t.Errorf("status = %s", m.Status)
	}
}

func TestFindingIDStableAcrossRounds(t *testing.T) {
	a := Finding{File: "a.go", Line: 2, Category: "bug", Title: "Nil deref"}
	b := a
	b.Detail = "reworded detail"
	b.Fix = "reworded fix"
	if a.ID() != b.ID() {
		t.Errorf("ID changed with prose: %s vs %s", a.ID(), b.ID())
	}
	b.Line = 3
	if a.ID() == b.ID() {
		t.Errorf("ID must change when the anchor moves")
	}
}

func TestMachineBlockSurvivesCommentTerminator(t *testing.T) {
	f := Finding{File: "a.go", Line: 2, Severity: High, Category: "bug",
		Title: "injects --> into html", Fix: "escape --> first", Confidence: 0.9}
	out := Summary(SummaryInput{
		Meta:     Meta{Number: 1, Repo: "o/r"},
		Findings: []Finding{f},
		Verdict:  Decide([]Finding{f}, "high"),
	})
	m := extractMachine(t, out) // fails if the marker closed early
	if m.Status != StatusActionRequired {
		t.Errorf("status = %s", m.Status)
	}
}

func TestResolvedIDsRendered(t *testing.T) {
	out := Summary(SummaryInput{
		Meta:        Meta{Number: 1, Repo: "o/r"},
		Verdict:     Decide(nil, "high"),
		Incremental: true,
		SinceSHA:    "prevsha",
		ResolvedIDs: []string{"f-abc123"},
	})
	if !strings.Contains(out, "1 finding(s) from the previous round are resolved") {
		t.Errorf("resolved note missing:\n%s", out)
	}
	m := extractMachine(t, out)
	if len(m.ResolvedIDs) != 1 || m.ResolvedIDs[0] != "f-abc123" {
		t.Errorf("resolved = %v", m.ResolvedIDs)
	}
	if !m.Coverage.Incremental || m.Coverage.SinceSHA != "prevsha" {
		t.Errorf("coverage = %+v", m.Coverage)
	}
}

func TestFailureBlockParses(t *testing.T) {
	body := FailureBlock(9, "o/r", "all sections failed")
	start := strings.Index(body, machineMarker)
	if start < 0 {
		t.Fatalf("no marker: %s", body)
	}
	rest := body[start+len(machineMarker):]
	end := strings.Index(rest, "-->")
	var m struct {
		Status string `json:"status"`
		Error  string `json:"error"`
		PR     int    `json:"pr"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(rest[:end])), &m); err != nil {
		t.Fatalf("failure block invalid: %v", err)
	}
	if m.Status != "error" || m.PR != 9 || m.Error == "" {
		t.Fatalf("failure block = %+v", m)
	}
}
