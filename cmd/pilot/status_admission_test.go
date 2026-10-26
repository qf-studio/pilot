package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestAdmissionPauseLine(t *testing.T) {
	at := time.Date(2026, 10, 26, 14, 5, 0, 0, time.UTC)
	tests := []struct {
		name string
		st   admissionStatusBody
		want string // substring; "" means no line
	}{
		{"not paused", admissionStatusBody{}, ""},
		{"paused by another owner", admissionStatusBody{AdmissionPaused: true, AdmissionPauseOwners: []string{"platform-breaker"}}, ""},
		{"usage limit", admissionStatusBody{AdmissionPaused: true, AdmissionPauseOwners: []string{"usage-limit"}, AdmissionResumesAt: &at}, "admission paused: usage limit, resumes at "},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := admissionPauseLine(tt.st)
			if tt.want == "" && got != "" || !strings.HasPrefix(got, tt.want) {
				t.Fatalf("line = %q, want prefix %q", got, tt.want)
			}
		})
	}
}

func TestFetchAdmissionPauseLine(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"admission_paused":true,"admission_pause_owners":["usage-limit"],"admission_resumes_at":"2026-10-26T14:05:00Z"}`))
	}))
	defer srv.Close()

	if got := fetchAdmissionPauseLine(srv.URL); !strings.HasPrefix(got, "admission paused: usage limit, resumes at ") {
		t.Fatalf("line = %q", got)
	}
	if got := fetchAdmissionPauseLine("http://127.0.0.1:1"); got != "" {
		t.Fatalf("unreachable daemon should print nothing, got %q", got)
	}
}
