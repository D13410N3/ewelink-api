package bridge

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// channelNames reads the ck_channel_name payload eWeLink returns for
// multi-channel devices. Firmware sends it as a map from outlet index to
// label; a plain array of labels is accepted as well.
func channelNames(raw json.RawMessage) map[int]string {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}

	var byOutlet map[string]json.RawMessage
	if err := json.Unmarshal(raw, &byOutlet); err == nil {
		names := make(map[int]string, len(byOutlet))
		for key, value := range byOutlet {
			outlet, convErr := strconv.Atoi(strings.TrimSpace(key))
			if convErr != nil || outlet < 0 {
				continue
			}
			if name := channelNameValue(value); name != "" {
				names[outlet] = name
			}
		}
		return names
	}

	var list []string
	if err := json.Unmarshal(raw, &list); err == nil {
		names := make(map[int]string, len(list))
		for outlet, name := range list {
			if name = strings.TrimSpace(name); name != "" {
				names[outlet] = name
			}
		}
		return names
	}
	return nil
}

// channelNameValue accepts both a bare label and an object carrying a name.
func channelNameValue(raw json.RawMessage) string {
	var name string
	if err := json.Unmarshal(raw, &name); err == nil {
		return strings.TrimSpace(name)
	}
	var object struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(raw, &object); err == nil {
		return strings.TrimSpace(object.Name)
	}
	return ""
}

// applyChannelNames sets the per-outlet labels of a multi-channel device.
// eWeLink's own label wins; when it sends nothing usable the bridge falls back
// to "Канал N" so consumers never have to invent a channel name themselves.
func applyChannelNames(device *Device, raw json.RawMessage) {
	switches, ok := deviceSwitches(*device)
	if !ok {
		device.Channels = nil
		return
	}

	names := channelNames(raw)
	channels := make(map[string]string, len(switches))
	for _, item := range switches {
		name := names[item.Outlet]
		if name == "" {
			name = fmt.Sprintf("Канал %d", item.Outlet+1)
		}
		channels[strconv.Itoa(item.Outlet)] = name
	}
	device.Channels = channels
}
