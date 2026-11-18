package bridge

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
)

// Device is the device data the bridge needs to list and control a device.
type Device struct {
	ID                string            `json:"id"`
	Name              string            `json:"name"`
	Online            bool              `json:"online"`
	UIID              string            `json:"uiid,omitempty"`
	Params            map[string]any    `json:"params"`
	Family            any               `json:"family,omitempty"`
	Channels          map[string]string `json:"channels,omitempty"`
	LastStateChangeAt *time.Time        `json:"lastStateChangeAt,omitempty"`
}

type Snapshot struct {
	Devices      []Device  `json:"devices"`
	RefreshedAt  time.Time `json:"refreshedAt"`
	RefreshError string    `json:"refreshError,omitempty"`
}

// Cache persists the authoritative device snapshot and the temporary local
// switch state applied after a successful control operation.
type Cache interface {
	Replace(context.Context, []Device, time.Time) error
	SetRefreshError(context.Context, error) error
	Snapshot(context.Context) (Snapshot, error)
	Get(context.Context, string) (Device, bool, error)
	SetSwitch(context.Context, string, string, time.Time) error
	SetChannelSwitch(context.Context, string, int, string, time.Time) error
	TryAcquireRefreshLease(context.Context, time.Duration) (bool, error)
	Close() error
}

type memoryCache struct {
	mu                sync.RWMutex
	snapshot          Snapshot
	refreshLeaseUntil time.Time
}

func NewMemoryCache() Cache {
	return &memoryCache{snapshot: Snapshot{Devices: []Device{}}}
}

// Replace installs an authoritative MCP snapshot. It preserves a device's
// state-change timestamp when its scalar switch state has not changed.
func (c *memoryCache) Replace(_ context.Context, devices []Device, refreshedAt time.Time) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.snapshot = Snapshot{
		Devices:     mergeDevices(c.snapshot.Devices, devices, refreshedAt),
		RefreshedAt: refreshedAt,
	}
	return nil
}

func (c *memoryCache) SetRefreshError(_ context.Context, err error) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.snapshot.RefreshError = err.Error()
	return nil
}

func (c *memoryCache) Snapshot(_ context.Context) (Snapshot, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return cloneSnapshot(c.snapshot), nil
}

func (c *memoryCache) Get(_ context.Context, id string) (Device, bool, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	for _, device := range c.snapshot.Devices {
		if device.ID == id {
			return cloneDevice(device), true, nil
		}
	}
	return Device{}, false, nil
}

// SetSwitch applies a successful control result locally. The next Replace
// removes it in favor of the remote server's authoritative status.
func (c *memoryCache) SetSwitch(_ context.Context, id, state string, changedAt time.Time) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	for i := range c.snapshot.Devices {
		if c.snapshot.Devices[i].ID != id {
			continue
		}
		if c.snapshot.Devices[i].Params == nil {
			c.snapshot.Devices[i].Params = make(map[string]any)
		}
		c.snapshot.Devices[i].Params["switch"] = state
		c.snapshot.Devices[i].LastStateChangeAt = timePointer(changedAt)
		return nil
	}
	return fmt.Errorf("device %q is not in the cache", id)
}

// SetChannelSwitch applies a successful multi-channel control result locally.
// The next Replace removes it in favor of the remote server's status.
func (c *memoryCache) SetChannelSwitch(_ context.Context, id string, channel int, state string, changedAt time.Time) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	for i := range c.snapshot.Devices {
		if c.snapshot.Devices[i].ID != id {
			continue
		}
		if err := setChannelState(&c.snapshot.Devices[i], channel, state); err != nil {
			return err
		}
		c.snapshot.Devices[i].LastStateChangeAt = timePointer(changedAt)
		return nil
	}
	return fmt.Errorf("device %q is not in the cache", id)
}

func (c *memoryCache) TryAcquireRefreshLease(_ context.Context, duration time.Duration) (bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := time.Now()
	if now.Before(c.refreshLeaseUntil) {
		return false, nil
	}
	c.refreshLeaseUntil = now.Add(duration)
	return true, nil
}

func (c *memoryCache) Close() error { return nil }

type RedisOptions struct {
	Host string
	Port int
	DB   int
	Auth string
}

type redisCache struct {
	client *redis.Client
	key    string
}

const (
	redisSnapshotKey     = "ewelink-api:device-snapshot"
	redisRefreshLeaseKey = "ewelink-api:device-refresh-lease"
)

func NewRedisCache(ctx context.Context, options RedisOptions) (Cache, error) {
	client := redis.NewClient(&redis.Options{
		Addr:     fmt.Sprintf("%s:%d", options.Host, options.Port),
		DB:       options.DB,
		Password: options.Auth,
	})
	if err := client.Ping(ctx).Err(); err != nil {
		_ = client.Close()
		return nil, fmt.Errorf("connect to Redis: %w", err)
	}
	return &redisCache{client: client, key: redisSnapshotKey}, nil
}

func (c *redisCache) Replace(ctx context.Context, devices []Device, refreshedAt time.Time) error {
	return c.mutate(ctx, func(snapshot *Snapshot) error {
		snapshot.Devices = mergeDevices(snapshot.Devices, devices, refreshedAt)
		snapshot.RefreshedAt = refreshedAt
		snapshot.RefreshError = ""
		return nil
	})
}

func (c *redisCache) SetRefreshError(ctx context.Context, refreshErr error) error {
	return c.mutate(ctx, func(snapshot *Snapshot) error {
		snapshot.RefreshError = refreshErr.Error()
		return nil
	})
}

func (c *redisCache) Snapshot(ctx context.Context) (Snapshot, error) {
	return c.load(ctx, c.client)
}

func (c *redisCache) Get(ctx context.Context, id string) (Device, bool, error) {
	snapshot, err := c.Snapshot(ctx)
	if err != nil {
		return Device{}, false, err
	}
	for _, device := range snapshot.Devices {
		if device.ID == id {
			return cloneDevice(device), true, nil
		}
	}
	return Device{}, false, nil
}

func (c *redisCache) SetSwitch(ctx context.Context, id, state string, changedAt time.Time) error {
	return c.mutate(ctx, func(snapshot *Snapshot) error {
		for i := range snapshot.Devices {
			if snapshot.Devices[i].ID != id {
				continue
			}
			if snapshot.Devices[i].Params == nil {
				snapshot.Devices[i].Params = make(map[string]any)
			}
			snapshot.Devices[i].Params["switch"] = state
			snapshot.Devices[i].LastStateChangeAt = timePointer(changedAt)
			return nil
		}
		return fmt.Errorf("device %q is not in the cache", id)
	})
}

func (c *redisCache) SetChannelSwitch(ctx context.Context, id string, channel int, state string, changedAt time.Time) error {
	return c.mutate(ctx, func(snapshot *Snapshot) error {
		for i := range snapshot.Devices {
			if snapshot.Devices[i].ID != id {
				continue
			}
			if err := setChannelState(&snapshot.Devices[i], channel, state); err != nil {
				return err
			}
			snapshot.Devices[i].LastStateChangeAt = timePointer(changedAt)
			return nil
		}
		return fmt.Errorf("device %q is not in the cache", id)
	})
}

func (c *redisCache) TryAcquireRefreshLease(ctx context.Context, duration time.Duration) (bool, error) {
	acquired, err := c.client.SetNX(ctx, redisRefreshLeaseKey, "1", duration).Result()
	if err != nil {
		return false, fmt.Errorf("acquire Redis refresh lease: %w", err)
	}
	return acquired, nil
}

func (c *redisCache) Close() error { return c.client.Close() }

func (c *redisCache) mutate(ctx context.Context, update func(*Snapshot) error) error {
	for range 3 {
		err := c.client.Watch(ctx, func(tx *redis.Tx) error {
			snapshot, err := c.load(ctx, tx)
			if err != nil {
				return err
			}
			if err := update(&snapshot); err != nil {
				return err
			}
			encoded, err := json.Marshal(snapshot)
			if err != nil {
				return fmt.Errorf("encode Redis cache snapshot: %w", err)
			}
			_, err = tx.TxPipelined(ctx, func(pipe redis.Pipeliner) error {
				pipe.Set(ctx, c.key, encoded, 0)
				return nil
			})
			return err
		}, c.key)
		if err != redis.TxFailedErr {
			return err
		}
	}
	return fmt.Errorf("update Redis cache: concurrent update conflict")
}

func (c *redisCache) load(ctx context.Context, client redis.Cmdable) (Snapshot, error) {
	encoded, err := client.Get(ctx, c.key).Bytes()
	if err == redis.Nil {
		return Snapshot{Devices: []Device{}}, nil
	}
	if err != nil {
		return Snapshot{}, fmt.Errorf("read Redis cache snapshot: %w", err)
	}
	var snapshot Snapshot
	if err := json.Unmarshal(encoded, &snapshot); err != nil {
		return Snapshot{}, fmt.Errorf("decode Redis cache snapshot: %w", err)
	}
	if snapshot.Devices == nil {
		snapshot.Devices = []Device{}
	}
	return cloneSnapshot(snapshot), nil
}

func (c *redisCache) save(ctx context.Context, snapshot Snapshot) error {
	encoded, err := json.Marshal(snapshot)
	if err != nil {
		return fmt.Errorf("encode Redis cache snapshot: %w", err)
	}
	if err := c.client.Set(ctx, c.key, encoded, 0).Err(); err != nil {
		return fmt.Errorf("write Redis cache snapshot: %w", err)
	}
	return nil
}

func mergeDevices(previous, incoming []Device, refreshedAt time.Time) []Device {
	previousByID := make(map[string]Device, len(previous))
	for _, device := range previous {
		previousByID[device.ID] = device
	}

	merged := cloneDevices(incoming)
	for i := range merged {
		previousDevice, found := previousByID[merged[i].ID]
		signature, hasState := switchSignature(merged[i])
		if !hasState {
			if found && previousDevice.LastStateChangeAt != nil {
				merged[i].LastStateChangeAt = timePointer(*previousDevice.LastStateChangeAt)
			}
			continue
		}

		previousSignature, previousHasState := switchSignature(previousDevice)
		if found && previousHasState && previousSignature == signature && previousDevice.LastStateChangeAt != nil {
			merged[i].LastStateChangeAt = timePointer(*previousDevice.LastStateChangeAt)
			continue
		}
		merged[i].LastStateChangeAt = timePointer(refreshedAt)
	}
	return merged
}

func scalarSwitchState(device Device) (string, bool) {
	state, ok := device.Params["switch"].(string)
	return state, ok && (state == "on" || state == "off")
}

// switchSignature renders the controllable state of a device as one string so
// mergeDevices can keep the previous change timestamp when nothing changed.
func switchSignature(device Device) (string, bool) {
	if state, ok := scalarSwitchState(device); ok {
		return "switch=" + state, true
	}
	switches, ok := deviceSwitches(device)
	if !ok {
		return "", false
	}
	parts := make([]string, 0, len(switches))
	for _, item := range switches {
		parts = append(parts, fmt.Sprintf("%d=%s", item.Outlet, item.State))
	}
	return "switches=" + strings.Join(parts, ","), true
}

type channelSwitch struct {
	Outlet int
	State  string
}

// deviceSwitches reads the multi-channel params.switches array in the shape
// eWeLink returns it: [{"outlet":0,"switch":"on"}, ...].
func deviceSwitches(device Device) ([]channelSwitch, bool) {
	raw, ok := device.Params["switches"]
	if !ok {
		return nil, false
	}
	switch values := raw.(type) {
	case []any:
		result := make([]channelSwitch, 0, len(values))
		for _, value := range values {
			entry, ok := value.(map[string]any)
			if !ok {
				continue
			}
			outlet, ok := intValue(entry["outlet"])
			if !ok {
				continue
			}
			state, ok := entry["switch"].(string)
			if !ok || (state != "on" && state != "off") {
				continue
			}
			result = append(result, channelSwitch{Outlet: outlet, State: state})
		}
		if len(result) == 0 {
			return nil, false
		}
		sort.Slice(result, func(i, j int) bool { return result[i].Outlet < result[j].Outlet })
		return result, true
	case []map[string]any:
		result := make([]channelSwitch, 0, len(values))
		for _, entry := range values {
			outlet, ok := intValue(entry["outlet"])
			if !ok {
				continue
			}
			state, ok := entry["switch"].(string)
			if !ok || (state != "on" && state != "off") {
				continue
			}
			result = append(result, channelSwitch{Outlet: outlet, State: state})
		}
		if len(result) == 0 {
			return nil, false
		}
		sort.Slice(result, func(i, j int) bool { return result[i].Outlet < result[j].Outlet })
		return result, true
	default:
		return nil, false
	}
}

func intValue(value any) (int, bool) {
	switch typed := value.(type) {
	case int:
		return typed, true
	case int64:
		return int(typed), true
	case float64:
		return int(typed), true
	case json.Number:
		number, err := typed.Int64()
		return int(number), err == nil
	default:
		return 0, false
	}
}

// setChannelState updates one outlet in params.switches, preserving the
// device's other channels and their order.
func setChannelState(device *Device, channel int, state string) error {
	if device.Params == nil {
		device.Params = make(map[string]any)
	}
	switches, ok := deviceSwitches(*device)
	if !ok {
		return fmt.Errorf("device %q does not expose params.switches", device.ID)
	}
	found := false
	encoded := make([]any, 0, len(switches))
	for _, item := range switches {
		next := item.State
		if item.Outlet == channel {
			next = state
			found = true
		}
		encoded = append(encoded, map[string]any{"outlet": item.Outlet, "switch": next})
	}
	if !found {
		return fmt.Errorf("device %q has no channel %d", device.ID, channel)
	}
	device.Params["switches"] = encoded
	return nil
}

func timePointer(value time.Time) *time.Time {
	value = value.UTC()
	return &value
}

func cloneSnapshot(snapshot Snapshot) Snapshot {
	clone := snapshot
	clone.Devices = cloneDevices(snapshot.Devices)
	return clone
}

func cloneDevices(devices []Device) []Device {
	clone := make([]Device, len(devices))
	for i, device := range devices {
		clone[i] = cloneDevice(device)
	}
	return clone
}

func cloneDevice(device Device) Device {
	clone := device
	if device.LastStateChangeAt != nil {
		clone.LastStateChangeAt = timePointer(*device.LastStateChangeAt)
	}
	if device.Params != nil {
		clone.Params = make(map[string]any, len(device.Params))
		for key, value := range device.Params {
			clone.Params[key] = value
		}
	}
	if device.Channels != nil {
		clone.Channels = make(map[string]string, len(device.Channels))
		for key, value := range device.Channels {
			clone.Channels[key] = value
		}
	}
	return clone
}
