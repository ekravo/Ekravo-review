package app

import (
	"testing"

	"github.com/ekravo/Ekravo-review/v2/internal/diff"
	"github.com/ekravo/Ekravo-review/v2/internal/review"
	"github.com/ekravo/Ekravo-review/v2/internal/store"
)

func touchedFile(t *testing.T, path, patch string) *diff.File {
	t.Helper()
	f, err := diff.ParseFilePatch(path, "modified", patch)
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func TestCarryForwardKeepsUntouchedBlockers(t *testing.T) {
	blocker := review.Finding{File: "a.go", Line: 10, Severity: review.High, Category: "bug", Title: "unfixed", Fix: "fix it"}
	note := review.Finding{File: "a.go", Line: 20, Severity: review.Low, Category: "quality", Title: "nit", Fix: "opt"}
	// The new commits touch an unrelated file only.
	touched := []*diff.File{touchedFile(t, "b.go", "@@ -1,2 +1,3 @@\n ctx\n+new line\n ctx2\n")}

	carried, resolved := carryForward([]review.Finding{blocker, note}, touched, "high")
	if len(carried) != 1 || carried[0].Title != "unfixed" || !carried[0].Carried {
		t.Fatalf("carried = %+v, want the untouched blocker only", carried)
	}
	if len(resolved) != 0 {
		t.Fatalf("resolved = %+v, want none", resolved)
	}
	// The carried blocker must keep the verdict at changes_requested.
	v := review.Decide(mergeCarried(nil, carried), "high")
	if v.Decision != "changes" {
		t.Fatalf("verdict with carried blocker = %s, want changes", v.Decision)
	}
}

func TestCarryForwardResolvesEditedAnchor(t *testing.T) {
	blocker := review.Finding{File: "a.go", Line: 10, Severity: review.High, Category: "bug", Title: "was here", Fix: "f"}
	// Hunk old side covers line 10: the author edited the defective line.
	touched := []*diff.File{touchedFile(t, "a.go", "@@ -9,3 +9,3 @@\n ctx\n-old line ten\n+fixed line ten\n")}

	carried, resolved := carryForward([]review.Finding{blocker}, touched, "high")
	if len(carried) != 0 {
		t.Fatalf("carried = %+v, want none", carried)
	}
	if len(resolved) != 1 || resolved[0].Title != "was here" {
		t.Fatalf("resolved = %+v", resolved)
	}
}

func TestMergeCarriedPrefersFreshOnSameLine(t *testing.T) {
	fresh := review.Finding{File: "a.go", Line: 10, Severity: review.High, Category: "bug", Title: "reworded", Fix: "new"}
	carried := review.Finding{File: "a.go", Line: 10, Severity: review.High, Category: "bug", Title: "old wording", Fix: "old", Carried: true}
	out := mergeCarried([]review.Finding{fresh}, []review.Finding{carried})
	if len(out) != 1 || out[0].Title != "reworded" {
		t.Fatalf("merged = %+v, want fresh only", out)
	}
}

func TestResolvedIDsSuppressFailedFix(t *testing.T) {
	prior := review.Finding{File: "a.go", Line: 10, Severity: review.High, Category: "bug", Title: "old wording", Fix: "f"}
	// The author edited line 10 but the model re-reported a defect there
	// with new wording: the fix failed, so the old ID is NOT resolved.
	fresh := review.Finding{File: "a.go", Line: 10, Severity: review.High, Category: "bug", Title: "still broken", Fix: "f2"}
	touched := []*diff.File{touchedFile(t, "a.go", "@@ -9,3 +9,3 @@\n ctx\n-old ten\n+bad fix ten\n")}
	_, touchedResolved := carryForward([]review.Finding{prior}, touched, "high")

	ids := resolvedIDs([]review.Finding{prior}, []review.Finding{fresh}, touched, touchedResolved)
	if len(ids) != 0 {
		t.Fatalf("resolved = %v, want none (fix failed on the same line)", ids)
	}

	// A real fix elsewhere in the file with no re-report resolves the ID.
	clean := []*diff.File{touchedFile(t, "a.go", "@@ -9,3 +9,3 @@\n ctx\n-old ten\n+good ten\n")}
	_, tr := carryForward([]review.Finding{prior}, clean, "high")
	ids = resolvedIDs([]review.Finding{prior}, nil, clean, tr)
	if len(ids) != 1 || ids[0] != prior.ID() {
		t.Fatalf("resolved = %v, want [%s]", ids, prior.ID())
	}
}

func TestPriorFindingsDecodesStoredJSON(t *testing.T) {
	prev := &store.Review{FindingsJSON: `[{"file":"a.go","line":2,"severity":"high","category":"bug","title":"t","fix":"f"}]`}
	got := priorFindings(prev)
	if len(got) != 1 || got[0].File != "a.go" {
		t.Fatalf("prior = %+v", got)
	}
	if priorFindings(nil) != nil || priorFindings(&store.Review{FindingsJSON: "not json"}) != nil {
		t.Fatalf("bad input should yield nil")
	}
}
