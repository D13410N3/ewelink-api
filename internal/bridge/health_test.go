package bridge

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
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

var errHealthRefresh = healthRefreshError{}

type healthRefreshError struct{}

func (healthRefreshError) Error() string { return "refresh failed" }
