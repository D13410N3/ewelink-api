package bridge

import "testing"

func TestDecodeDevicesFillsChannelNames(t *testing.T) {
	result := []byte(`{"content":[{"type":"text","text":"{\"devices\":[{\"deviceid\":\"1000abc\",\"name\":\"Switch\",\"online\":true,\"uiid\":\"8\",\"params\":{\"switches\":[{\"outlet\":0,\"switch\":\"on\"},{\"outlet\":1,\"switch\":\"off\"},{\"outlet\":2,\"switch\":\"off\"}]},\"ck_channel_name\":{\"0\":\"Ванная\",\"1\":\"Туалет\",\"2\":\"Кухня\"}}]}"}]}`)

	devices, err := decodeDevices(result)
	if err != nil {
		t.Fatalf("decodeDevices() error = %v", err)
	}
	if len(devices) != 1 {
		t.Fatalf("device count = %d, want 1", len(devices))
	}
	want := map[string]string{"0": "Ванная", "1": "Туалет", "2": "Кухня"}
	if !equalChannels(devices[0].Channels, want) {
		t.Fatalf("channels = %#v, want %#v", devices[0].Channels, want)
	}
}

func TestDecodeDevicesFallsBackToChannelNumber(t *testing.T) {
	result := []byte(`{"content":[{"type":"text","text":"{\"devices\":[{\"deviceid\":\"1000abc\",\"name\":\"Switch\",\"online\":true,\"uiid\":\"4\",\"params\":{\"switches\":[{\"outlet\":0,\"switch\":\"on\"},{\"outlet\":1,\"switch\":\"on\"},{\"outlet\":2,\"switch\":\"on\"},{\"outlet\":3,\"switch\":\"on\"}]}}]}"}]}`)

	devices, err := decodeDevices(result)
	if err != nil {
		t.Fatalf("decodeDevices() error = %v", err)
	}
	want := map[string]string{"0": "Канал 1", "1": "Канал 2", "2": "Канал 3", "3": "Канал 4"}
	if !equalChannels(devices[0].Channels, want) {
		t.Fatalf("channels = %#v, want %#v", devices[0].Channels, want)
	}
}

func TestDecodeDevicesFillsMissingChannelNameOnly(t *testing.T) {
	result := []byte(`{"content":[{"type":"text","text":"{\"devices\":[{\"deviceid\":\"1000abc\",\"name\":\"Switch\",\"online\":true,\"uiid\":\"8\",\"params\":{\"switches\":[{\"outlet\":0,\"switch\":\"off\"},{\"outlet\":1,\"switch\":\"off\"},{\"outlet\":2,\"switch\":\"off\"}]},\"ck_channel_name\":{\"0\":\"Ванная\",\"2\":\"  \"}}]}"}]}`)

	devices, err := decodeDevices(result)
	if err != nil {
		t.Fatalf("decodeDevices() error = %v", err)
	}
	want := map[string]string{"0": "Ванная", "1": "Канал 2", "2": "Канал 3"}
	if !equalChannels(devices[0].Channels, want) {
		t.Fatalf("channels = %#v, want %#v", devices[0].Channels, want)
	}
}

func TestDecodeDevicesAcceptsChannelNameArray(t *testing.T) {
	result := []byte(`{"content":[{"type":"text","text":"{\"devices\":[{\"deviceid\":\"1000abc\",\"name\":\"Switch\",\"online\":true,\"uiid\":\"8\",\"params\":{\"switches\":[{\"outlet\":0,\"switch\":\"off\"},{\"outlet\":1,\"switch\":\"off\"}]},\"ck_channel_name\":[\"Ванная\",\"Туалет\"]}]}"}]}`)

	devices, err := decodeDevices(result)
	if err != nil {
		t.Fatalf("decodeDevices() error = %v", err)
	}
	want := map[string]string{"0": "Ванная", "1": "Туалет"}
	if !equalChannels(devices[0].Channels, want) {
		t.Fatalf("channels = %#v, want %#v", devices[0].Channels, want)
	}
}

func TestDecodeDevicesAcceptsChannelNameObjects(t *testing.T) {
	result := []byte(`{"content":[{"type":"text","text":"{\"devices\":[{\"deviceid\":\"1000abc\",\"name\":\"Switch\",\"online\":true,\"uiid\":\"8\",\"params\":{\"switches\":[{\"outlet\":0,\"switch\":\"off\"},{\"outlet\":1,\"switch\":\"off\"}]},\"ck_channel_name\":{\"0\":{\"name\":\"Ванная\"},\"1\":{\"name\":\"Туалет\"}}}]}"}]}`)

	devices, err := decodeDevices(result)
	if err != nil {
		t.Fatalf("decodeDevices() error = %v", err)
	}
	want := map[string]string{"0": "Ванная", "1": "Туалет"}
	if !equalChannels(devices[0].Channels, want) {
		t.Fatalf("channels = %#v, want %#v", devices[0].Channels, want)
	}
}

func TestDecodeDevicesOmitsChannelsForScalarSwitch(t *testing.T) {
	result := []byte(`{"content":[{"type":"text","text":"{\"devices\":[{\"deviceid\":\"1000abc\",\"name\":\"Lamp\",\"online\":true,\"uiid\":\"1\",\"params\":{\"switch\":\"on\"},\"ck_channel_name\":{\"0\":\"Ванная\"}}]}"}]}`)

	devices, err := decodeDevices(result)
	if err != nil {
		t.Fatalf("decodeDevices() error = %v", err)
	}
	if len(devices[0].Channels) != 0 {
		t.Fatalf("channels = %#v, want none", devices[0].Channels)
	}
}

func equalChannels(got, want map[string]string) bool {
	if len(got) != len(want) {
		return false
	}
	for key, value := range want {
		if got[key] != value {
			return false
		}
	}
	return true
}
