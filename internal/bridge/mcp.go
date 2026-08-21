package bridge

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

const protocolVersion = "2025-03-26"

type MCPClient struct {
	endpoint string
	http     *http.Client

	mu        sync.Mutex
	sessionID string
	nextID    int64
}

func NewMCPClient(endpoint string, client *http.Client) *MCPClient {
	if client == nil {
		client = &http.Client{Timeout: 20 * time.Second}
	}
	return &MCPClient{endpoint: endpoint, http: client}
}

func (c *MCPClient) FetchDevices(ctx context.Context) ([]Device, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	result, err := c.callLocked(ctx, "getBasicInformation", map[string]any{"extraParams": map[string]any{}})
	if err != nil {
		return nil, err
	}
	return decodeDevices(result)
}

// ControlSwitch intentionally performs one MCP call only. A retry after an
// ambiguous transport failure could execute the physical action twice.
func (c *MCPClient) ControlSwitch(ctx context.Context, device Device, state string) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	result, err := c.callLocked(ctx, "controlSingleDevice", map[string]any{
		"deviceid":    device.ID,
		"name":        device.Name,
		"params":      map[string]any{"switch": state},
		"extraParams": map[string]any{},
	})
	if err != nil {
		return err
	}
	return controlSucceeded(result)
}

func (c *MCPClient) callLocked(ctx context.Context, tool string, arguments map[string]any) (json.RawMessage, error) {
	if c.sessionID == "" {
		if err := c.initializeLocked(ctx); err != nil {
			return nil, err
		}
	}
	return c.requestLocked(ctx, "tools/call", map[string]any{"name": tool, "arguments": arguments})
}

func (c *MCPClient) initializeLocked(ctx context.Context) error {
	result, sessionID, err := c.request(ctx, "", "initialize", map[string]any{
		"protocolVersion": protocolVersion,
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]string{"name": "ewelink-api", "version": "0.1.0"},
	}, false)
	if err != nil {
		return err
	}
	var initResult struct {
		ProtocolVersion string `json:"protocolVersion"`
	}
	if err := json.Unmarshal(result, &initResult); err != nil {
		return fmt.Errorf("decode MCP initialize result: %w", err)
	}
	if sessionID == "" {
		return fmt.Errorf("MCP server did not return Mcp-Session-Id")
	}
	c.sessionID = sessionID
	// The MCP protocol requires this notification after successful initialization.
	if _, _, err := c.request(ctx, c.sessionID, "notifications/initialized", nil, true); err != nil {
		c.sessionID = ""
		return fmt.Errorf("send MCP initialized notification: %w", err)
	}
	return nil
}

func (c *MCPClient) requestLocked(ctx context.Context, method string, params any) (json.RawMessage, error) {
	result, sessionID, err := c.request(ctx, c.sessionID, method, params, false)
	if sessionID != "" {
		c.sessionID = sessionID
	}
	return result, err
}

func (c *MCPClient) request(ctx context.Context, sessionID, method string, params any, notification bool) (json.RawMessage, string, error) {
	payload := map[string]any{"jsonrpc": "2.0", "method": method}
	if !notification {
		c.nextID++
		payload["id"] = c.nextID
	}
	if params != nil {
		payload["params"] = params
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, "", err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("MCP-Protocol-Version", protocolVersion)
	if sessionID != "" {
		req.Header.Set("Mcp-Session-Id", sessionID)
	}

	response, err := c.http.Do(req)
	if err != nil {
		return nil, "", fmt.Errorf("MCP %s request: %w", method, err)
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		message, _ := io.ReadAll(io.LimitReader(response.Body, 4096))
		return nil, response.Header.Get("Mcp-Session-Id"), fmt.Errorf("MCP %s returned HTTP %d: %s", method, response.StatusCode, strings.TrimSpace(string(message)))
	}
	if method == "notifications/initialized" {
		return nil, response.Header.Get("Mcp-Session-Id"), nil
	}

	result, err := decodeMCPResponse(response.Body)
	if err != nil {
		return nil, response.Header.Get("Mcp-Session-Id"), fmt.Errorf("decode MCP %s response: %w", method, err)
	}
	return result, response.Header.Get("Mcp-Session-Id"), nil
}

func decodeMCPResponse(body io.Reader) (json.RawMessage, error) {
	reader := bufio.NewScanner(body)
	reader.Buffer(make([]byte, 4096), 4<<20)
	for reader.Scan() {
		line := reader.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		var envelope struct {
			Result json.RawMessage `json:"result"`
			Error  *struct {
				Code    int    `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
		}
		if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &envelope); err != nil {
			return nil, err
		}
		if envelope.Error != nil {
			return nil, fmt.Errorf("RPC error %d: %s", envelope.Error.Code, envelope.Error.Message)
		}
		if envelope.Result != nil {
			return envelope.Result, nil
		}
	}
	if err := reader.Err(); err != nil {
		return nil, err
	}
	return nil, fmt.Errorf("response did not contain an MCP result")
}

func decodeDevices(result json.RawMessage) ([]Device, error) {
	text, err := toolText(result)
	if err != nil {
		return nil, err
	}
	var payload struct {
		Devices []struct {
			ID       string          `json:"deviceid"`
			Name     string          `json:"name"`
			Online   bool            `json:"online"`
			UIID     json.RawMessage `json:"uiid"`
			Params   json.RawMessage `json:"params"`
			Family   any             `json:"family"`
			Channels any             `json:"ck_channel_name"`
		} `json:"devices"`
	}
	if err := json.Unmarshal([]byte(text), &payload); err != nil {
		return nil, fmt.Errorf("tool did not return a device JSON document: %w", err)
	}

	devices := make([]Device, 0, len(payload.Devices))
	for _, source := range payload.Devices {
		params, err := decodeJSONObject(source.Params)
		if err != nil {
			return nil, fmt.Errorf("decode params for device %q: %w", source.ID, err)
		}
		var uiid string
		if len(source.UIID) > 0 && string(source.UIID) != "null" {
			if err := json.Unmarshal(source.UIID, &uiid); err != nil {
				uiid = strings.Trim(string(source.UIID), "\"")
			}
		}
		devices = append(devices, Device{ID: source.ID, Name: source.Name, Online: source.Online, UIID: uiid, Params: params, Family: source.Family, Channels: source.Channels})
	}
	return devices, nil
}

func toolText(result json.RawMessage) (string, error) {
	var response struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.Unmarshal(result, &response); err != nil {
		return "", err
	}
	for _, item := range response.Content {
		if item.Type == "text" && item.Text != "" {
			return item.Text, nil
		}
	}
	return "", fmt.Errorf("tool returned no text content")
}

func decodeJSONObject(raw json.RawMessage) (map[string]any, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return map[string]any{}, nil
	}
	var object map[string]any
	if err := json.Unmarshal(raw, &object); err == nil {
		return object, nil
	}
	var encoded string
	if err := json.Unmarshal(raw, &encoded); err != nil {
		return nil, err
	}
	if err := json.Unmarshal([]byte(encoded), &object); err != nil {
		return nil, err
	}
	return object, nil
}

func controlSucceeded(result json.RawMessage) error {
	text, err := toolText(result)
	if err != nil {
		return err
	}
	var response struct {
		Error int    `json:"error"`
		Msg   string `json:"msg"`
	}
	if err := json.Unmarshal([]byte(text), &response); err != nil {
		return fmt.Errorf("tool did not return a control JSON document: %w", err)
	}
	if response.Error != 0 {
		if response.Msg == "" {
			response.Msg = "unknown control error"
		}
		return fmt.Errorf("eWeLink rejected control: %s (code %d)", response.Msg, response.Error)
	}
	return nil
}
