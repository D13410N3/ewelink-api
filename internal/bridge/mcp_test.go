package bridge

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestDecodeDevicesWithEncodedParams(t *testing.T) {
	result := []byte(`{"content":[{"type":"text","text":"{\"devices\":[{\"deviceid\":\"1000abc\",\"name\":\"Lamp\",\"online\":true,\"uiid\":1,\"params\":\"{\\\"switch\\\":\\\"on\\\"}\"}]}"}]}`)

	devices, err := decodeDevices(result)
	if err != nil {
		t.Fatalf("decodeDevices() error = %v", err)
	}
	if len(devices) != 1 {
		t.Fatalf("device count = %d, want 1", len(devices))
	}
	if devices[0].ID != "1000abc" || devices[0].Params["switch"] != "on" {
		t.Fatalf("decoded device = %#v", devices[0])
	}
}

func TestDecodeMCPResponse(t *testing.T) {
	result, err := decodeMCPResponse(strings.NewReader("event: message\ndata: {\"jsonrpc\":\"2.0\",\"id\":1,\"result\":{\"ok\":true}}\n\n"))
	if err != nil {
		t.Fatalf("decodeMCPResponse() error = %v", err)
	}
	if string(result) != `{"ok":true}` {
		t.Fatalf("result = %s", result)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

func TestMCPInitializationSendsNotificationWithoutID(t *testing.T) {
	var notification map[string]any
	transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		var payload map[string]any
		if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
			t.Fatalf("decode request: %v", err)
		}

		status := http.StatusOK
		headers := make(http.Header)
		var body string
		switch payload["method"] {
		case "initialize":
			headers.Set("Mcp-Session-Id", "session")
			body = "data: {\"jsonrpc\":\"2.0\",\"id\":1,\"result\":{\"protocolVersion\":\"2025-03-26\"}}\n\n"
		case "notifications/initialized":
			notification = payload
			status = http.StatusAccepted
		case "tools/call":
			body = "data: {\"jsonrpc\":\"2.0\",\"id\":2,\"result\":{\"content\":[{\"type\":\"text\",\"text\":\"{\\\"devices\\\":[]}\"}]}}\n\n"
		default:
			t.Fatalf("unexpected method %q", payload["method"])
		}
		return &http.Response{StatusCode: status, Header: headers, Body: io.NopCloser(strings.NewReader(body)), Request: request}, nil
	})

	client := NewMCPClient("https://mcp.invalid", &http.Client{Transport: transport})
	if _, err := client.FetchDevices(context.Background()); err != nil {
		t.Fatalf("FetchDevices() error = %v", err)
	}
	if _, hasID := notification["id"]; hasID {
		t.Fatalf("initialized notification unexpectedly has id: %#v", notification)
	}
}

func TestControlSucceeded(t *testing.T) {
	if err := controlSucceeded([]byte(`{"content":[{"type":"text","text":"{\"error\":0}"}]}`)); err != nil {
		t.Fatalf("controlSucceeded() error = %v", err)
	}
	if err := controlSucceeded([]byte(`{"content":[{"type":"text","text":"{\"error\":1,\"msg\":\"offline\"}"}]}`)); err == nil {
		t.Fatal("controlSucceeded() error = nil, want rejection")
	}
}
