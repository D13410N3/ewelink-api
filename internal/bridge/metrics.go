package bridge

import (
	"strconv"

	"github.com/prometheus/client_golang/prometheus"
)

type deviceMetrics interface {
	RecordRefresh([]Device)
	RecordSwitch(Device, string)
}

type Metrics struct {
	deviceState *prometheus.GaugeVec
	switches    *prometheus.CounterVec
}

func NewMetrics(registerer prometheus.Registerer) (*Metrics, error) {
	if registerer == nil {
		registerer = prometheus.DefaultRegisterer
	}
	metrics := &Metrics{
		deviceState: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "ewelink_device_state",
			Help: "Current primary switch state for eWeLink devices; on is 1 and off is 0.",
		}, []string{"id", "name", "online", "uiid"}),
		switches: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "ewelink_device_switches_total",
			Help: "Successful eWeLink primary switch control operations.",
		}, []string{"id", "name", "online", "uiid"}),
	}
	if err := registerer.Register(metrics.deviceState); err != nil {
		return nil, err
	}
	if err := registerer.Register(metrics.switches); err != nil {
		return nil, err
	}
	return metrics, nil
}

func (m *Metrics) RecordRefresh(devices []Device) {
	m.deviceState.Reset()
	for _, device := range devices {
		state, ok := switchState(device)
		if !ok {
			continue
		}
		m.deviceState.With(deviceLabels(device)).Set(state)
	}
}

func (m *Metrics) RecordSwitch(device Device, state string) {
	labels := deviceLabels(device)
	m.switches.With(labels).Inc()
	m.deviceState.With(labels).Set(switchStateValue(state))
}

type noopMetrics struct{}

func (noopMetrics) RecordRefresh([]Device)      {}
func (noopMetrics) RecordSwitch(Device, string) {}

func deviceLabels(device Device) prometheus.Labels {
	return prometheus.Labels{
		"id":     device.ID,
		"name":   device.Name,
		"online": strconv.FormatBool(device.Online),
		"uiid":   device.UIID,
	}
}

func switchState(device Device) (float64, bool) {
	state, ok := device.Params["switch"].(string)
	if !ok || (state != "on" && state != "off") {
		return 0, false
	}
	return switchStateValue(state), true
}

func switchStateValue(state string) float64 {
	if state == "on" {
		return 1
	}
	return 0
}
