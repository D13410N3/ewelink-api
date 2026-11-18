package bridge

import (
	"strconv"

	"github.com/prometheus/client_golang/prometheus"
)

type deviceMetrics interface {
	RecordRefresh([]Device)
	RecordSwitch(Device, string)
	RecordChannelSwitch(Device, int, string)
}

type Metrics struct {
	deviceState     *prometheus.GaugeVec
	switches        *prometheus.CounterVec
	channelState    *prometheus.GaugeVec
	channelSwitches *prometheus.CounterVec
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
		channelState: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "ewelink_device_channel_state",
			Help: "Current outlet state for multi-channel eWeLink devices; on is 1 and off is 0.",
		}, []string{"id", "name", "online", "uiid", "channel"}),
		channelSwitches: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "ewelink_device_channel_switches_total",
			Help: "Successful eWeLink outlet control operations.",
		}, []string{"id", "name", "online", "uiid", "channel"}),
	}
	for _, collector := range []prometheus.Collector{metrics.deviceState, metrics.switches, metrics.channelState, metrics.channelSwitches} {
		if err := registerer.Register(collector); err != nil {
			return nil, err
		}
	}
	return metrics, nil
}

func (m *Metrics) RecordRefresh(devices []Device) {
	m.deviceState.Reset()
	m.channelState.Reset()
	for _, device := range devices {
		if state, ok := switchState(device); ok {
			m.deviceState.With(deviceLabels(device)).Set(state)
		}
		switches, ok := deviceSwitches(device)
		if !ok {
			continue
		}
		for _, item := range switches {
			m.channelState.With(channelLabels(device, item.Outlet)).Set(switchStateValue(item.State))
		}
	}
}

func (m *Metrics) RecordSwitch(device Device, state string) {
	labels := deviceLabels(device)
	m.switches.With(labels).Inc()
	m.deviceState.With(labels).Set(switchStateValue(state))
}

func (m *Metrics) RecordChannelSwitch(device Device, channel int, state string) {
	labels := channelLabels(device, channel)
	m.channelSwitches.With(labels).Inc()
	m.channelState.With(labels).Set(switchStateValue(state))
}

type noopMetrics struct{}

func (noopMetrics) RecordRefresh([]Device)                  {}
func (noopMetrics) RecordSwitch(Device, string)             {}
func (noopMetrics) RecordChannelSwitch(Device, int, string) {}

func deviceLabels(device Device) prometheus.Labels {
	return prometheus.Labels{
		"id":     device.ID,
		"name":   device.Name,
		"online": strconv.FormatBool(device.Online),
		"uiid":   device.UIID,
	}
}

func channelLabels(device Device, channel int) prometheus.Labels {
	labels := deviceLabels(device)
	labels["channel"] = strconv.Itoa(channel)
	return labels
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
