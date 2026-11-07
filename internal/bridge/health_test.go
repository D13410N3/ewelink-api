package bridge

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestHealthCheckUsesLastRefreshResult(t *testing.T) {
	remote := &fakeRemote{devices: []Device{{ID: "one", Params: map[string]any{"switch": "off"}}}}
	service := newTestService(remote)
	handler := NewHTTPHandler(service)

	beforeRefresh := httptest.NewRecorder()
	handler.ServeHTTP(beforeRefresh, httptest.NewRequest(http.MethodGet, "/health-check", nil))
	if beforeRefresh.Code != http.StatusServiceUnavailable {
		t.Fatalf("status before refresh = %d, want %d", beforeRefresh.Code, http.StatusServiceUnavailable)
	}

	if err := service.Refresh(context.Background()); err != nil {
		t.Fatalf("Refresh() error = %v", err)
	}
	healthy := httptest.NewRecorder()
	handler.ServeHTTP(healthy, httptest.NewRequest(http.MethodGet, "/health-check", nil))
	if healthy.Code != http.StatusOK {
		t.Fatalf("status after successful refresh = %d, want %d", healthy.Code, http.StatusOK)
	}

	remote.fetchErr = errHealthRefresh
	if err := service.Refresh(context.Background()); err == nil {
		t.Fatal("Refresh() error = nil, want error")
	}
	unhealthy := httptest.NewRecorder()
	handler.ServeHTTP(unhealthy, httptest.NewRequest(http.MethodGet, "/health-check", nil))
	if unhealthy.Code != http.StatusServiceUnavailable {
		t.Fatalf("status after failed refresh = %d, want %d", unhealthy.Code, http.StatusServiceUnavailable)
	}
}

func TestLivezDoesNotDependOnSnapshot(t *testing.T) {
	service := newTestService(&fakeRemote{})
	handler := NewHTTPHandler(service)

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/livez", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("livez status = %d, want %d", response.Code, http.StatusOK)
	}
}

func TestHealthCheckToleratesRecentFailedRefresh(t *testing.T) {
	remote := &fakeRemote{devices: []Device{{ID: "one", Params: map[string]any{"switch": "off"}}}}
	service := newTestService(remote)
	if err := service.Refresh(context.Background()); err != nil {
		t.Fatalf("Refresh() error = %v", err)
	}
	handler := NewHTTPHandlerWithOptions(service, HTTPOptions{MaxSnapshotAge: 2 * time.Minute})

	remote.fetchErr = errHealthRefresh
	if err := service.Refresh(context.Background()); err == nil {
		t.Fatal("Refresh() error = nil, want error")
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/health-check", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("status with a recent snapshot = %d, want %d: %s", response.Code, http.StatusOK, response.Body.String())
	}
}

func TestHealthCheckFailsWhenSnapshotIsTooOld(t *testing.T) {
	cache := NewMemoryCache()
	if err := cache.Replace(context.Background(), []Device{{ID: "one"}}, time.Now().Add(-10*time.Minute)); err != nil {
		t.Fatalf("cache.Replace() error = %v", err)
	}
	service := NewServiceWithCache(&fakeRemote{}, cache, noopMetrics{}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	handler := NewHTTPHandlerWithOptions(service, HTTPOptions{MaxSnapshotAge: time.Minute})

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/health-check", nil))
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("status with an old snapshot = %d, want %d", response.Code, http.StatusServiceUnavailable)
	}
}

var errHealthRefresh = healthRefreshError{}

type healthRefreshError struct{}

func (healthRefreshError) Error() string { return "refresh failed" }
