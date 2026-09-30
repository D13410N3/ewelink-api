package bridge

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"
)

type fakeRemote struct {
	devices      []Device
	fetchErr     error
	controlErr   error
	controlledID string
	controlledTo string
	fetches      int
}

func (f *fakeRemote) FetchDevices(context.Context) ([]Device, error) {
	f.fetches++
	if f.fetchErr != nil {
		return nil, f.fetchErr
	}
	return f.devices, nil
}

func (f *fakeRemote) ControlSwitch(_ context.Context, device Device, state string) error {
	f.controlledID = device.ID
	f.controlledTo = state
	return f.controlErr
}

func newTestService(remote Remote) *Service {
	return NewService(remote, slog.New(slog.NewTextHandler(io.Discard, nil)))
}

func TestRefreshIfStaleUsesSharedFreshSnapshot(t *testing.T) {
	cache := NewMemoryCache()
	if err := cache.Replace(context.Background(), []Device{{ID: "existing"}}, time.Now().UTC()); err != nil {
		t.Fatalf("cache.Replace() error = %v", err)
	}
	remote := &fakeRemote{devices: []Device{{ID: "from-remote"}}}
	service := NewServiceWithCache(remote, cache, noopMetrics{}, slog.New(slog.NewTextHandler(io.Discard, nil)))

	if err := service.RefreshIfStale(context.Background(), time.Minute); err != nil {
		t.Fatalf("RefreshIfStale() error = %v", err)
	}
	device, found, err := service.Device(context.Background(), "existing")
	if err != nil || !found || device.ID != "existing" {
		t.Fatalf("fresh shared snapshot changed unexpectedly: device=%#v found=%t error=%v", device, found, err)
	}
	if remote.fetches != 0 {
		t.Fatalf("remote fetches = %d, want 0 for a fresh shared snapshot", remote.fetches)
	}
}

func TestRefreshIfStaleRefreshesSharedStaleSnapshotOnce(t *testing.T) {
	cache := NewMemoryCache()
	if err := cache.Replace(context.Background(), []Device{{ID: "stale"}}, time.Now().Add(-time.Minute)); err != nil {
		t.Fatalf("cache.Replace() error = %v", err)
	}
	remote := &fakeRemote{devices: []Device{{ID: "fresh"}}}
	first := NewServiceWithCache(remote, cache, noopMetrics{}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	second := NewServiceWithCache(remote, cache, noopMetrics{}, slog.New(slog.NewTextHandler(io.Discard, nil)))

	if err := first.RefreshIfStale(context.Background(), time.Second); err != nil {
		t.Fatalf("first RefreshIfStale() error = %v", err)
	}
	if err := second.RefreshIfStale(context.Background(), time.Second); err != nil {
		t.Fatalf("second RefreshIfStale() error = %v", err)
	}
	if _, found, _ := second.Device(context.Background(), "fresh"); !found {
		t.Fatal("shared stale snapshot was not refreshed")
	}
	if _, found, _ := second.Device(context.Background(), "stale"); found {
		t.Fatal("stale shared snapshot was not replaced")
	}
	if remote.fetches != 1 {
		t.Fatalf("remote fetches = %d, want 1 for shared stale snapshot", remote.fetches)
	}
}

func TestRefreshPreservesTimestampUntilObservedStateChange(t *testing.T) {
	cache := NewMemoryCache()
	service := NewServiceWithCache(&fakeRemote{}, cache, noopMetrics{}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	initialAt := time.Date(2026, time.August, 22, 12, 0, 0, 0, time.UTC)
	if err := cache.Replace(context.Background(), []Device{{ID: "one", Params: map[string]any{"switch": "off"}}}, initialAt); err != nil {
		t.Fatalf("initial Replace() error = %v", err)
	}

	unchangedRefreshAt := initialAt.Add(time.Minute)
	if err := cache.Replace(context.Background(), []Device{{ID: "one", Params: map[string]any{"switch": "off"}}}, unchangedRefreshAt); err != nil {
		t.Fatalf("unchanged Replace() error = %v", err)
	}
	device, found, err := service.Device(context.Background(), "one")
	if err != nil || !found || device.LastStateChangeAt == nil || !device.LastStateChangeAt.Equal(initialAt) {
		t.Fatalf("unchanged state timestamp = %v, found=%t, error=%v; want %v", device.LastStateChangeAt, found, err, initialAt)
	}

	changedRefreshAt := unchangedRefreshAt.Add(time.Minute)
	if err := cache.Replace(context.Background(), []Device{{ID: "one", Params: map[string]any{"switch": "on"}}}, changedRefreshAt); err != nil {
		t.Fatalf("changed Replace() error = %v", err)
	}
	device, found, err = service.Device(context.Background(), "one")
	if err != nil || !found || device.LastStateChangeAt == nil || !device.LastStateChangeAt.Equal(changedRefreshAt) {
		t.Fatalf("changed state timestamp = %v, found=%t, error=%v; want %v", device.LastStateChangeAt, found, err, changedRefreshAt)
	}
}

func TestSwitchUpdatesCacheUntilAuthoritativeRefresh(t *testing.T) {
	remote := &fakeRemote{devices: []Device{{ID: "one", Name: "Lamp", Params: map[string]any{"switch": "off"}}}}
	service := newTestService(remote)
	if err := service.Refresh(context.Background()); err != nil {
		t.Fatalf("Refresh() error = %v", err)
	}

	device, err := service.Switch(context.Background(), "one", "")
	if err != nil {
		t.Fatalf("Switch() error = %v", err)
	}
	if remote.controlledID != "one" || remote.controlledTo != "on" {
		t.Fatalf("control = (%q, %q), want (one, on)", remote.controlledID, remote.controlledTo)
	}
	if got := device.Params["switch"]; got != "on" {
		t.Fatalf("local switch state = %v, want on", got)
	}
	if device.LastStateChangeAt == nil {
		t.Fatal("local switch did not set lastStateChangeAt")
	}
	localChangeAt := *device.LastStateChangeAt

	remote.devices[0].Params["switch"] = "on"
	if err := service.Refresh(context.Background()); err != nil {
		t.Fatalf("Refresh() error = %v", err)
	}
	device, ok, err := service.Device(context.Background(), "one")
	if err != nil || !ok || device.LastStateChangeAt == nil || !device.LastStateChangeAt.Equal(localChangeAt) {
		t.Fatalf("unchanged refresh did not retain local timestamp: %#v, found=%t, error=%v", device, ok, err)
	}

	remote.devices[0].Params["switch"] = "off"
	if err := service.Refresh(context.Background()); err != nil {
		t.Fatalf("Refresh() error = %v", err)
	}
	device, ok, err = service.Device(context.Background(), "one")
	if err != nil {
		t.Fatalf("Device() error = %v", err)
	}
	if !ok || device.Params["switch"] != "off" {
		t.Fatalf("authoritative refresh did not replace local state: %#v", device)
	}
	if device.LastStateChangeAt == nil || !device.LastStateChangeAt.After(localChangeAt) {
		t.Fatalf("changed refresh did not set a newer timestamp: %v, previous=%v", device.LastStateChangeAt, localChangeAt)
	}
}

func TestSwitchDoesNotChangeCacheWhenControlFails(t *testing.T) {
	remote := &fakeRemote{
		devices:    []Device{{ID: "one", Params: map[string]any{"switch": "off"}}},
		controlErr: errors.New("MCP unavailable"),
	}
	service := newTestService(remote)
	if err := service.Refresh(context.Background()); err != nil {
		t.Fatalf("Refresh() error = %v", err)
	}

	if _, err := service.Switch(context.Background(), "one", "on"); err == nil {
		t.Fatal("Switch() error = nil, want error")
	}
	device, _, err := service.Device(context.Background(), "one")
	if err != nil {
		t.Fatalf("Device() error = %v", err)
	}
	if got := device.Params["switch"]; got != "off" {
		t.Fatalf("switch state = %v, want off after failed control", got)
	}
}

func TestSwitchRejectsUnsupportedDeviceState(t *testing.T) {
	remote := &fakeRemote{devices: []Device{{ID: "multi", Params: map[string]any{"switches": []any{}}}}}
	service := newTestService(remote)
	if err := service.Refresh(context.Background()); err != nil {
		t.Fatalf("Refresh() error = %v", err)
	}

	if _, err := service.Switch(context.Background(), "multi", "on"); err == nil {
		t.Fatal("Switch() error = nil, want unsupported state error")
	}
	if remote.controlledID != "" {
		t.Fatalf("control called for unsupported state: %q", remote.controlledID)
	}
}
