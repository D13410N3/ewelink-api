package bridge

import (
	"context"
	"io"
	"log/slog"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
)

func TestMetricsRecordDeviceStateAndSwitch(t *testing.T) {
	registry := prometheus.NewRegistry()
	metrics, err := NewMetrics(registry)
	if err != nil {
		t.Fatalf("NewMetrics() error = %v", err)
	}
	remote := &fakeRemote{devices: []Device{{
		ID: "one", Name: "Lamp", Online: true, UIID: "1", Params: map[string]any{"switch": "off"},
	}}}
	service := NewServiceWithCache(remote, NewMemoryCache(), metrics, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err := service.Refresh(context.Background()); err != nil {
		t.Fatalf("Refresh() error = %v", err)
	}
	if _, err := service.Switch(context.Background(), "one", "on"); err != nil {
		t.Fatalf("Switch() error = %v", err)
	}

	families, err := registry.Gather()
	if err != nil {
		t.Fatalf("Gather() error = %v", err)
	}
	values := map[string]float64{}
	for _, family := range families {
		for _, metric := range family.Metric {
			if family.GetName() == "ewelink_device_state" {
				values[family.GetName()] = metric.GetGauge().GetValue()
			}
			if family.GetName() == "ewelink_device_switches_total" {
				values[family.GetName()] = metric.GetCounter().GetValue()
			}
		}
	}
	if values["ewelink_device_state"] != 1 {
		t.Fatalf("ewelink_device_state = %v, want 1", values["ewelink_device_state"])
	}
	if values["ewelink_device_switches_total"] != 1 {
		t.Fatalf("ewelink_device_switches_total = %v, want 1", values["ewelink_device_switches_total"])
	}
}
