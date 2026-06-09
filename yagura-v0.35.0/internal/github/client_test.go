package github

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func testServer(t *testing.T, handler http.HandlerFunc) (*httptest.Server, *Client) {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	c := NewClient(Config{Token: "ghp_test_token", BaseURL: srv.URL})
	return srv, c
}

func TestGetRepository_Success(t *testing.T) {
	_, c := testServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/shizukutanaka/mihari" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer ghp_test_token" {
			t.Errorf("auth missing")
		}
		w.Header().Set("X-RateLimit-Limit", "5000")
		w.Header().Set("X-RateLimit-Remaining", "4999")
		w.Header().Set("X-RateLimit-Reset", fmt.Sprint(time.Now().Add(1*time.Hour).Unix()))
		fmt.Fprintln(w, `{
			"full_name": "shizukutanaka/mihari",
			"default_branch": "main",
			"pushed_at": "2026-05-13T10:00:00Z",
			"stargazers_count": 42,
			"language": "Go"
		}`)
	})

	r, err := c.GetRepository(context.Background(), "shizukutanaka", "mihari")
	if err != nil {
		t.Fatal(err)
	}
	if r.FullName != "shizukutanaka/mihari" {
		t.Errorf("got %q", r.FullName)
	}
	if r.StargazersCount != 42 {
		t.Errorf("got %d", r.StargazersCount)
	}
	if r.Owner != "shizukutanaka" || r.Name != "mihari" {
		t.Errorf("Owner/Name not set: %q/%q", r.Owner, r.Name)
	}
	rl := c.LastRateLimit()
	if rl.Limit != 5000 || rl.Remaining != 4999 {
		t.Errorf("RateLimit not updated: %+v", rl)
	}
}

func TestGetRepository_NotFound(t *testing.T) {
	_, c := testServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})
	_, err := c.GetRepository(context.Background(), "x", "ghost")
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("expected ErrNotFound, got %v", err)
	}
}

func TestGetRepository_Unauthorized(t *testing.T) {
	_, c := testServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	})
	_, err := c.GetRepository(context.Background(), "x", "r")
	if !errors.Is(err, ErrUnauthorized) {
		t.Errorf("expected ErrUnauthorized, got %v", err)
	}
}

func TestGetRepository_RateLimited(t *testing.T) {
	_, c := testServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("X-RateLimit-Remaining", "0")
		w.WriteHeader(http.StatusForbidden)
	})
	_, err := c.GetRepository(context.Background(), "x", "r")
	if !errors.Is(err, ErrRateLimited) {
		t.Errorf("expected ErrRateLimited, got %v", err)
	}
}

func TestGetRepository_Forbidden_NonRateLimit(t *testing.T) {
	_, c := testServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("X-RateLimit-Remaining", "4000")
		w.WriteHeader(http.StatusForbidden)
	})
	_, err := c.GetRepository(context.Background(), "x", "r")
	if !errors.Is(err, ErrForbidden) {
		t.Errorf("expected ErrForbidden, got %v", err)
	}
	if errors.Is(err, ErrRateLimited) {
		t.Error("should NOT be RateLimited")
	}
}

func TestCountOpenItems(t *testing.T) {
	_, c := testServer(t, func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query().Get("q")
		switch {
		case strings.Contains(q, "is:pr"):
			fmt.Fprintln(w, `{"total_count": 7}`)
		case strings.Contains(q, "is:issue"):
			fmt.Fprintln(w, `{"total_count": 12}`)
		default:
			t.Errorf("unknown query: %s", q)
		}
	})
	prs, issues, err := c.CountOpenItems(context.Background(), "x", "r")
	if err != nil {
		t.Fatal(err)
	}
	if prs != 7 || issues != 12 {
		t.Errorf("got prs=%d issues=%d, want 7 and 12", prs, issues)
	}
}

func TestLatestRelease_Found(t *testing.T) {
	_, c := testServer(t, func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintln(w, `{"tag_name": "v0.11.0"}`)
	})
	tag, err := c.LatestRelease(context.Background(), "x", "r")
	if err != nil {
		t.Fatal(err)
	}
	if tag != "v0.11.0" {
		t.Errorf("got %q", tag)
	}
}

func TestLatestRelease_NoReleaseReturnsEmpty(t *testing.T) {
	_, c := testServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})
	tag, err := c.LatestRelease(context.Background(), "x", "r")
	if err != nil {
		t.Fatalf("404 should be tolerated, got %v", err)
	}
	if tag != "" {
		t.Errorf("expected empty, got %q", tag)
	}
}

func TestLatestCIStatus_Success(t *testing.T) {
	_, c := testServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("branch") != "main" {
			t.Errorf("branch param missing")
		}
		fmt.Fprintln(w, `{"workflow_runs": [{"conclusion": "success"}]}`)
	})
	status, err := c.LatestCIStatus(context.Background(), "x", "r", "main")
	if err != nil {
		t.Fatal(err)
	}
	if status != "success" {
		t.Errorf("got %q", status)
	}
}

func TestLatestCIStatus_NoRuns(t *testing.T) {
	_, c := testServer(t, func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintln(w, `{"workflow_runs": []}`)
	})
	status, err := c.LatestCIStatus(context.Background(), "x", "r", "main")
	if err != nil {
		t.Fatal(err)
	}
	if status != "" {
		t.Errorf("got %q, want empty", status)
	}
}

func TestLatestCIStatus_NoActionsEnabled(t *testing.T) {
	_, c := testServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})
	status, err := c.LatestCIStatus(context.Background(), "x", "r", "main")
	if err != nil {
		t.Fatalf("404 should be tolerated, got %v", err)
	}
	if status != "" {
		t.Errorf("got %q, want empty", status)
	}
}

func TestRequest_UserAgent(t *testing.T) {
	_, c := testServer(t, func(w http.ResponseWriter, r *http.Request) {
		ua := r.Header.Get("User-Agent")
		if !strings.Contains(ua, "yagura") {
			t.Errorf("UA wrong: %q", ua)
		}
		fmt.Fprintln(w, `{}`)
	})
	_, _ = c.GetRepository(context.Background(), "x", "r")
}

func TestRequest_ContextCancellation(t *testing.T) {
	_, c := testServer(t, func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(1 * time.Second)
	})
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_, err := c.GetRepository(ctx, "x", "r")
	if err == nil {
		t.Error("expected timeout error")
	}
}

// ─── GetCommit ───────────────────────────────────────────────

func TestGetCommit_Success(t *testing.T) {
	_, c := testServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/org/repo/commits/abc123" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		fmt.Fprintln(w, `{"sha":"abc123","commit":{"committer":{"date":"2026-06-09T12:00:00Z"}}}`)
	})
	info, err := c.GetCommit(context.Background(), "org", "repo", "abc123")
	if err != nil {
		t.Fatalf("GetCommit: %v", err)
	}
	if info.SHA != "abc123" {
		t.Errorf("SHA = %q, want abc123", info.SHA)
	}
}

func TestGetCommit_NotFound(t *testing.T) {
	_, c := testServer(t, func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"message":"Not Found"}`, http.StatusNotFound)
	})
	_, err := c.GetCommit(context.Background(), "org", "repo", "deadbeef")
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("expected ErrNotFound, got %v", err)
	}
}

// ─── GetTagSHA ───────────────────────────────────────────────

func TestGetTagSHA_LightweightTag(t *testing.T) {
	_, c := testServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/repos/org/repo/git/ref/tags/v1.0" {
			fmt.Fprintln(w, `{"object":{"sha":"sha-of-commit","type":"commit"}}`)
			return
		}
		http.Error(w, "unexpected path", http.StatusInternalServerError)
	})
	sha, err := c.GetTagSHA(context.Background(), "org", "repo", "v1.0")
	if err != nil {
		t.Fatalf("GetTagSHA: %v", err)
	}
	if sha != "sha-of-commit" {
		t.Errorf("SHA = %q, want sha-of-commit", sha)
	}
}

func TestGetTagSHA_AnnotatedTag(t *testing.T) {
	_, c := testServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/org/repo/git/ref/tags/v2.0":
			// annotated tag points at tag object, not commit
			fmt.Fprintln(w, `{"object":{"sha":"tag-obj-sha","type":"tag"}}`)
		case "/repos/org/repo/git/tags/tag-obj-sha":
			// dereference tag object → commit SHA
			fmt.Fprintln(w, `{"object":{"sha":"commit-sha"}}`)
		default:
			http.Error(w, "unexpected: "+r.URL.Path, http.StatusInternalServerError)
		}
	})
	sha, err := c.GetTagSHA(context.Background(), "org", "repo", "v2.0")
	if err != nil {
		t.Fatalf("GetTagSHA annotated: %v", err)
	}
	if sha != "commit-sha" {
		t.Errorf("SHA = %q, want commit-sha", sha)
	}
}

func TestGetTagSHA_NotFound(t *testing.T) {
	_, c := testServer(t, func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"message":"Not Found"}`, http.StatusNotFound)
	})
	_, err := c.GetTagSHA(context.Background(), "org", "repo", "vX.Y")
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("expected ErrNotFound, got %v", err)
	}
}
