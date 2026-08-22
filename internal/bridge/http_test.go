package bridge

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestForcedRefreshEndpointsUpdateSnapshot(t *testing.T) {
	remote := &fakeRemote{devices: []Device{{ID: "one", Params: map[string]any{"switch": "off"}}}}
	service := newTestService(remote)
	if err := service.Refresh(context.Background()); err != nil {
		t.Fatalf("initial Refresh() error = %v", err)
	}
	remote.devices = []Device{{ID: "two", Params: map[string]any{"switch": "on"}}}

	handler := NewHTTPHandler(service)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/v1/update", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("update status = %d, want %d: %s", response.Code, http.StatusOK, response.Body.String())
	}
	if _, found, err := service.Device(context.Background(), "two"); err != nil || !found {
		t.Fatalf("updated cache does not contain new device: found=%t, error=%v", found, err)
	}

	remote.devices = []Device{{ID: "three", Params: map[string]any{"switch": "off"}}}
	forceResponse := httptest.NewRecorder()
	handler.ServeHTTP(forceResponse, httptest.NewRequest(http.MethodGet, "/v1/devices/force", nil))
	if forceResponse.Code != http.StatusOK {
		t.Fatalf("force status = %d, want %d: %s", forceResponse.Code, http.StatusOK, forceResponse.Body.String())
	}
	if _, found, err := service.Device(context.Background(), "three"); err != nil || !found {
		t.Fatalf("force endpoint did not update cache: found=%t, error=%v", found, err)
	}

	remote.fetchErr = errHealthRefresh
	failedResponse := httptest.NewRecorder()
	handler.ServeHTTP(failedResponse, httptest.NewRequest(http.MethodGet, "/v1/devices/force", nil))
	if failedResponse.Code != http.StatusBadGateway {
		t.Fatalf("failed update status = %d, want %d", failedResponse.Code, http.StatusBadGateway)
	}
	if _, found, err := service.Device(context.Background(), "three"); err != nil || !found {
		t.Fatalf("failed update did not retain cache: found=%t, error=%v", found, err)
	}
}

func TestSwitchEndpointUsesOptionalFormState(t *testing.T) {
	remote := &fakeRemote{devices: []Device{{ID: "one", Params: map[string]any{"switch": "off"}}}}
	service := newTestService(remote)
	if err := service.Refresh(context.Background()); err != nil {
		t.Fatalf("Refresh() error = %v", err)
	}
	handler := NewHTTPHandler(service)

	explicit := httptest.NewRequest(http.MethodPost, "/v1/devices/one/switch", strings.NewReader("state=on"))
	explicit.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	explicitResponse := httptest.NewRecorder()
	handler.ServeHTTP(explicitResponse, explicit)
	if explicitResponse.Code != http.StatusOK {
		t.Fatalf("explicit state status = %d, want %d: %s", explicitResponse.Code, http.StatusOK, explicitResponse.Body.String())
	}
	if remote.controlledTo != "on" {
		t.Fatalf("explicit control state = %q, want on", remote.controlledTo)
	}

	toggle := httptest.NewRequest(http.MethodPost, "/v1/devices/one/switch", nil)
	toggleResponse := httptest.NewRecorder()
	handler.ServeHTTP(toggleResponse, toggle)
	if toggleResponse.Code != http.StatusOK {
		t.Fatalf("omitted state status = %d, want %d: %s", toggleResponse.Code, http.StatusOK, toggleResponse.Body.String())
	}
	if remote.controlledTo != "off" {
		t.Fatalf("omitted state control = %q, want off", remote.controlledTo)
	}

	legacy := httptest.NewRequest(http.MethodPost, "/v1/devices/one/switch/on", nil)
	legacyResponse := httptest.NewRecorder()
	handler.ServeHTTP(legacyResponse, legacy)
	if legacyResponse.Code != http.StatusNotFound {
		t.Fatalf("legacy route status = %d, want %d", legacyResponse.Code, http.StatusNotFound)
	}
}
