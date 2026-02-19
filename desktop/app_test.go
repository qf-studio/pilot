package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestGetServerStatus_DaemonRunning(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "healthy"})
	})
	mux.HandleFunc("/api/v1/status", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"version": "1.40.1",
			"running": true,
		})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	app := &App{
		httpClient: &http.Client{Timeout: 2 * time.Second},
		gatewayURL: srv.URL,
	}

	status := app.GetServerStatus()
	if !status.Running {
		t.Fatal("expected Running=true when daemon is healthy")
	}
	if status.Version != "1.40.1" {
		t.Fatalf("expected version 1.40.1, got %q", status.Version)
	}
	if status.GatewayURL != srv.URL {
		t.Fatalf("expected GatewayURL=%q, got %q", srv.URL, status.GatewayURL)
	}
}

func TestGetServerStatus_DaemonNotRunning(t *testing.T) {
	app := &App{
		httpClient: &http.Client{Timeout: 1 * time.Second},
		gatewayURL: "http://127.0.0.1:1", // nothing listening
	}

	status := app.GetServerStatus()
	if status.Running {
		t.Fatal("expected Running=false when daemon is unreachable")
	}
}

func TestGetServerStatus_EmptyGatewayURL(t *testing.T) {
	app := &App{
		httpClient: &http.Client{Timeout: 1 * time.Second},
		gatewayURL: "",
	}

	status := app.GetServerStatus()
	if status.Running {
		t.Fatal("expected Running=false when gatewayURL is empty")
	}
}

func TestGetServerStatus_HealthOK_StatusUnauthorized(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "healthy"})
	})
	mux.HandleFunc("/api/v1/status", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	app := &App{
		httpClient: &http.Client{Timeout: 2 * time.Second},
		gatewayURL: srv.URL,
	}

	status := app.GetServerStatus()
	if !status.Running {
		t.Fatal("expected Running=true even when /api/v1/status returns 401")
	}
	if status.Version != "" {
		t.Fatalf("expected empty version when status is unauthorized, got %q", status.Version)
	}
}
