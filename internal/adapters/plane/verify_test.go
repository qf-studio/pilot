package plane

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/qf-studio/pilot/internal/testutil"
)

const (
	verifyTestSlug     = "acme"
	verifyTestProject1 = "proj-first-1111"
	verifyTestProject2 = "proj-second-2222"
)

func verifyTestConfig(baseURL, apiKey string, projectIDs ...string) *Config {
	return &Config{
		Enabled:       true,
		BaseURL:       baseURL,
		APIKey:        apiKey,
		WorkspaceSlug: verifyTestSlug,
		ProjectIDs:    projectIDs,
	}
}

func TestVerifier_OK(t *testing.T) {
	var gotPath, gotKey string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotKey = r.Header.Get("X-API-Key")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"results":[{"id":"lbl-1","name":"pilot","color":"#000000"}]}`))
	}))
	defer srv.Close()

	v := NewVerifier(verifyTestConfig(srv.URL, testutil.FakePlaneAPIKey, verifyTestProject1, verifyTestProject2))
	if got := v.Name(); got != "plane" {
		t.Errorf("Name() = %q, want %q", got, "plane")
	}
	if err := v.Verify(context.Background()); err != nil {
		t.Fatalf("Verify() = %v, want nil", err)
	}

	wantPath := "/api/v1/workspaces/" + verifyTestSlug + "/projects/" + verifyTestProject1 + "/labels/"
	if gotPath != wantPath {
		t.Errorf("request path = %q, want %q", gotPath, wantPath)
	}
	if gotKey != testutil.FakePlaneAPIKey {
		t.Errorf("X-API-Key = %q, want %q", gotKey, testutil.FakePlaneAPIKey)
	}
}

func TestVerifier_Unauthorized(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"detail":"Invalid API key"}`))
	}))
	defer srv.Close()

	err := NewVerifier(verifyTestConfig(srv.URL, testutil.FakePlaneAPIKey, verifyTestProject1)).Verify(context.Background())
	if err == nil {
		t.Fatal("Verify() = nil, want an error")
	}
	msg := err.Error()
	if !strings.HasPrefix(msg, "plane check failed:") {
		t.Errorf("error %q does not start with %q", msg, "plane check failed:")
	}
	for _, want := range []string{verifyTestSlug, verifyTestProject1} {
		if !strings.Contains(msg, want) {
			t.Errorf("error %q does not name %q", msg, want)
		}
	}
}

func TestVerifier_NoProjects(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		_, _ = w.Write([]byte(`{"results":[]}`))
	}))
	defer srv.Close()

	err := NewVerifier(verifyTestConfig(srv.URL, testutil.FakePlaneAPIKey)).Verify(context.Background())
	if err == nil {
		t.Fatal("Verify() = nil, want an error")
	}
	msg := err.Error()
	if !strings.HasPrefix(msg, "plane check failed:") || !strings.Contains(msg, "no project is set") {
		t.Errorf("error %q does not say that no project is set", msg)
	}
	if n := calls.Load(); n != 0 {
		t.Errorf("server got %d requests, want 0", n)
	}
}

// errorTexts returns every rendering of err a caller can get: the text, the
// text of each step of errors.Unwrap (single and multi), and %v, %+v and %#v.
func errorTexts(err error) []string {
	var texts []string
	queue := []error{err}
	for len(queue) > 0 {
		e := queue[0]
		queue = queue[1:]
		if e == nil {
			continue
		}
		texts = append(texts, e.Error(), fmt.Sprintf("%v", e), fmt.Sprintf("%+v", e), fmt.Sprintf("%#v", e))
		switch u := e.(type) {
		case interface{ Unwrap() error }:
			queue = append(queue, u.Unwrap())
		case interface{ Unwrap() []error }:
			queue = append(queue, u.Unwrap()...)
		}
	}
	return texts
}

func TestVerifier_ErrorHasNoKey(t *testing.T) {
	ctx := context.Background()
	var texts []string

	// The 401 answer echoes the request, X-API-Key header included.
	srv := testutil.EchoServer(t, http.StatusUnauthorized)
	err := NewVerifier(verifyTestConfig(srv.URL, testutil.CanaryToken, verifyTestProject1)).Verify(ctx)
	_ = testutil.ErrText(t, err)
	if errors.Unwrap(err) == nil {
		t.Error("401 error wraps nothing, want the SDK error")
	}
	texts = append(texts, errorTexts(err)...)

	// The transport failure prints the request with all its headers.
	err = NewVerifier(
		verifyTestConfig("https://plane.invalid", testutil.CanaryToken, verifyTestProject1),
		WithVerifierHTTPClient(&http.Client{Transport: testutil.FailingTransport()}),
	).Verify(ctx)
	_ = testutil.ErrText(t, err)
	texts = append(texts, errorTexts(err)...)

	testutil.AssertNoCanaryLeak(t, testutil.CanaryToken, texts...)
}
