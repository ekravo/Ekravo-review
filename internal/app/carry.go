package app

import (
	"context"
	"encoding/json"
	"sort"
	"strconv"
	"strings"

	"github.com/ekravo/Ekravo-review/v2/internal/diff"
	"github.com/ekravo/Ekravo-review/v2/internal/review"
	"github.com/ekravo/Ekravo-review/v2/internal/store"
)

// carryForward keeps an incremental re-review honest. The incremental diff
// only shows new commits, so a blocking finding the author never fixed would
// otherwise vanish from the verdict and the PR would approve itself green.
// A prior finding is resolved when the new commits edit or remove its anchor
// line; findings whose file was not touched at all are carried forward.
func carryForward(prior []review.Finding, touched []*diff.File, blockOn string) (carried, resolved []review.Finding) {
	byPath := make(map[string]*diff.File, len(touched))
	for _, f := range touched {
		byPath[f.Path] = f
	}
	for _, p := range prior {
		// Only blocking findings gate the verdict; optional notes are not
		// carried, so the author is never nagged into fixing them.
		if !review.Blocking(p, blockOn) {
			continue
		}
		f := byPath[p.File]
		if f == nil || !lineTouched(f, p.Line) {
			p.Carried = true
			carried = append(carried, p)
			continue
		}
		resolved = append(resolved, p)
	}
	return carried, resolved
}

// lineTouched reports whether the incremental diff edited or removed the
// anchor line. Prior findings are anchored in the previous head, which is the
// OLD side of the incremental diff, so hunk old-side spans are the authority.
func lineTouched(f *diff.File, line int) bool {
	for _, h := range f.Hunks {
		if h.OldCount > 0 && line >= h.OldStart && line < h.OldStart+h.OldCount {
			return true
		}
	}
	return false
}

// priorFindings decodes the stored findings of the previous review round.
func priorFindings(prev *store.Review) []review.Finding {
	if prev == nil || strings.TrimSpace(prev.FindingsJSON) == "" {
		return nil
	}
	var out []review.Finding
	if err := json.Unmarshal([]byte(prev.FindingsJSON), &out); err != nil {
		return nil
	}
	return out
}

// mergeCarried combines this round's findings with carried-forward ones,
// dropping any carried finding the model re-reported on the same line.
func mergeCarried(fresh, carried []review.Finding) []review.Finding {
	seen := make(map[string]bool, len(fresh))
	for _, f := range fresh {
		seen[f.File+":"+strconv.Itoa(f.Line)] = true
	}
	out := append([]review.Finding(nil), fresh...)
	for _, c := range carried {
		if seen[c.File+":"+strconv.Itoa(c.Line)] {
			continue
		}
		out = append(out, c)
	}
	return out
}

// resolvedIDs lists previous-round findings this round no longer reports.
// An edited anchor only counts as resolved when the model did not re-report
// a defect on the same file:line; otherwise the fix failed and the finding
// is still open under its new wording. The touched list is the incremental
// diff, whose old side matches the previous head the findings anchor to.
func resolvedIDs(prior, current []review.Finding, touched []*diff.File, touchedResolved []review.Finding) []string {
	byPath := make(map[string]*diff.File, len(touched))
	for _, f := range touched {
		byPath[f.Path] = f
	}
	nowID := make(map[string]bool, len(current))
	for _, f := range current {
		nowID[f.ID()] = true
	}
	reopened := make(map[string]bool, len(touchedResolved))
	for _, p := range touchedResolved {
		for _, f := range current {
			if f.File != p.File {
				continue
			}
			if f.Line == p.Line || lineTouchedNew(byPath[f.File], f.Line) {
				reopened[p.ID()] = true
				break
			}
		}
	}
	var out []string
	for _, p := range prior {
		if !nowID[p.ID()] && !reopened[p.ID()] {
			out = append(out, p.ID())
		}
	}
	sort.Strings(out)
	return out
}

// lineTouchedNew reports whether a new-side line sits inside an edited hunk.
func lineTouchedNew(f *diff.File, line int) bool {
	if f == nil {
		return false
	}
	for _, h := range f.Hunks {
		if h.NewCount > 0 && line >= h.NewStart && line < h.NewStart+h.NewCount {
			return true
		}
	}
	return false
}

// severityOrder ranks severities for display and the inline-comment budget.
func severityOrder(s review.Severity) int {
	switch s {
	case review.Critical:
		return 4
	case review.High:
		return 3
	case review.Medium:
		return 2
	case review.Low:
		return 1
	default:
		return 0
	}
}

// loadPrior fetches the previous review record once per run; callers pass it
// to diffFor and the carry-forward step so the store is not queried twice.
func (a *App) loadPrior(ctx context.Context, repo string, number int) *store.Review {
	prev, ok, err := a.st.LatestReview(ctx, repo, number)
	if err != nil || !ok {
		return nil
	}
	return prev
}
