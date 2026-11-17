package poller

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ekravo/Ekravo-review/v2/internal/config"
	"github.com/ekravo/Ekravo-review/v2/internal/githubx"
)

func TestWatchedReposFollowsWatchList(t *testing.T) {
	cfg := &config.Config{GitHub: config.GitHub{
		Repos:       []string{"o/configured"},
		IgnoreRepos: map[string]struct{}{},
	}}
	p := New(cfg, nil, slog.New(slog.NewTextHandler(io.Discard, nil)), nil, nil)
	ctx := context.Background()

	got := p.watchedRepos(ctx)
	if len(got) != 1 || got[0] != "o/configured" {
		t.Fatalf("without a watch list = %v", got)
	}

	p.SetWatch(func(context.Context) (bool, []string, error) {
		return false, []string{"o/one", "o/two"}, nil
	})
	if got = p.watchedRepos(ctx); len(got) != 2 || got[1] != "o/two" {
		t.Fatalf("selected watch list = %v", got)
	}

	p.SetWatch(func(context.Context) (bool, []string, error) { return true, nil, nil })
	if got = p.watchedRepos(ctx); len(got) != 1 || got[0] != "o/configured" {
		t.Fatalf("watch-all should fall back to configured repos = %v", got)
	}

	p.SetWatch(func(context.Context) (bool, []string, error) { return false, nil, errors.New("db down") })
	if got = p.watchedRepos(ctx); len(got) != 1 || got[0] != "o/configured" {
		t.Fatalf("read errors must fail open = %v", got)
	}

	p.SetWatch(func(context.Context) (bool, []string, error) { return false, nil, nil })
	if got = p.watchedRepos(ctx); len(got) != 0 {
		t.Fatalf("empty selection should poll nothing = %v", got)
	}
}

func TestScanCommentsSkipsOwnIdentityOnly(t *testing.T) {
	now := time.Now().UTC()
	comments := []githubx.IssueComment{}
	add := func(login string) {
		c := githubx.IssueComment{
			Body:      "@ekravo-review review",
			IssueURL:  "https://api.github.com/repos/o/r/pull/7",
			CreatedAt: now,
		}
		c.User.Login = login
		comments = append(comments, c)
	}
	add("ekravo-review")      // our PAT identity: skip
	add("ekravo-review[bot]") // our App identity: skip
	add("Ekravo-Review[Bot]") // case-insensitive: skip
	add("other-agent[bot]")   // a different bot may command us: accept
	add("some-human")         // humans: accept

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(r.URL.Path, "/issues/comments") {
			json.NewEncoder(w).Encode(comments)
			return
		}
		json.NewEncoder(w).Encode([]githubx.PullRequest{})
	}))
	defer srv.Close()

	gh, err := githubx.New(config.GitHub{BaseURL: srv.URL, Token: "test-token"})
	if err != nil {
		t.Fatalf("client: %v", err)
	}
	cfg := &config.Config{GitHub: config.GitHub{
		Repos:       []string{"o/r"},
		IgnoreRepos: map[string]struct{}{},
	}}
	cfg.Bot.Handle = "ekravo-review"

	var cmds []string
	p := New(cfg, gh, slog.New(slog.NewTextHandler(io.Discard, nil)), nil,
		func(_ context.Context, _ string, _ int, command string) { cmds = append(cmds, command) })

	ctx := context.Background()
	p.cycle(ctx) // first pass only sets the watermark
	p.cycle(ctx)

	if len(cmds) != 2 || cmds[0] != "review" || cmds[1] != "review" {
		t.Fatalf("commands = %v, want two reviews from other-agent[bot] and some-human only", cmds)
	}
}
