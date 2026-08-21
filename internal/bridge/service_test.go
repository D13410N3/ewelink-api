package bridge

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
)

type fakeRemote struct {
	devices      []Device
	fetchErr     error
	controlErr   error
	controlledID string
	controlledTo string
}

func (f *fakeRemote) FetchDevices(context.Context) ([]Device, error) {
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

	remote.devices[0].Params["switch"] = "off"
	if err := service.Refresh(context.Background()); err != nil {
		t.Fatalf("Refresh() error = %v", err)
	}
	device, ok, err := service.Device(context.Background(), "one")
	if err != nil {
		t.Fatalf("Device() error = %v", err)
	}
	if !ok || device.Params["switch"] != "off" {
		t.Fatalf("authoritative refresh did not replace local state: %#v", device)
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
