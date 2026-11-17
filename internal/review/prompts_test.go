package review

import (
	"strings"
	"testing"
)

func TestSystemPromptAddressesAgentReader(t *testing.T) {
	p := SystemPrompt(nil)
	for _, want := range []string{"AI coding agent", "applies fixes"} {
		if !strings.Contains(p, want) {
			t.Errorf("system prompt missing %q", want)
		}
	}
	// The schema example must use concrete valid enum values, not the
	// pipe-separated alternatives weak models copy verbatim.
	if strings.Contains(p, `"critical|high|medium|low"`) {
		t.Errorf("schema still shows pipe alternatives as a literal value")
	}
}

func TestReReviewPromptShowsPriorFindings(t *testing.T) {
	prior := []Finding{{File: "a.go", Line: 4, Severity: High, Title: "unfixed nil deref"}}
	p := UserPromptWithPrior(Meta{Number: 1}, "", "", "+ 5 | x", 0, 1, false, true, prior)
	if !strings.Contains(p, "Findings from the previous round") {
		t.Fatalf("prior findings block missing:\n%s", p)
	}
	if !strings.Contains(p, "a.go:4 [high] unfixed nil deref") {
		t.Fatalf("prior finding not listed:\n%s", p)
	}
	// Without prior findings the impossible instruction must not appear.
	p = UserPromptWithPrior(Meta{Number: 1}, "", "", "+ 5 | x", 0, 1, false, true, nil)
	if strings.Contains(p, "Findings from the previous round") {
		t.Fatalf("empty prior list should not render a block")
	}
}
