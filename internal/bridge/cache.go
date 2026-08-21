package bridge

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
)

// Device is the device data the bridge needs to list and control a device.
type Device struct {
	ID       string         `json:"id"`
	Name     string         `json:"name"`
	Online   bool           `json:"online"`
	UIID     string         `json:"uiid,omitempty"`
	Params   map[string]any `json:"params"`
	Family   any            `json:"family,omitempty"`
	Channels any            `json:"channels,omitempty"`
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
	SetSwitch(context.Context, string, string) error
	Close() error
}

type memoryCache struct {
	mu       sync.RWMutex
	snapshot Snapshot
}

func NewMemoryCache() Cache {
	return &memoryCache{snapshot: Snapshot{Devices: []Device{}}}
}

// Replace installs an authoritative MCP snapshot. It deliberately discards all
// locally optimistic values created by controls between refreshes.
func (c *memoryCache) Replace(_ context.Context, devices []Device, refreshedAt time.Time) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.snapshot = Snapshot{Devices: cloneDevices(devices), RefreshedAt: refreshedAt}
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
func (c *memoryCache) SetSwitch(_ context.Context, id, state string) error {
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
		return nil
	}
	return fmt.Errorf("device %q is not in the cache", id)
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

const redisSnapshotKey = "ewelink-api:device-snapshot"

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
	return c.save(ctx, Snapshot{Devices: cloneDevices(devices), RefreshedAt: refreshedAt})
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

func (c *redisCache) SetSwitch(ctx context.Context, id, state string) error {
	return c.mutate(ctx, func(snapshot *Snapshot) error {
		for i := range snapshot.Devices {
			if snapshot.Devices[i].ID != id {
				continue
			}
			if snapshot.Devices[i].Params == nil {
				snapshot.Devices[i].Params = make(map[string]any)
			}
			snapshot.Devices[i].Params["switch"] = state
			return nil
		}
		return fmt.Errorf("device %q is not in the cache", id)
	})
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
	if device.Params != nil {
		clone.Params = make(map[string]any, len(device.Params))
		for key, value := range device.Params {
			clone.Params[key] = value
		}
	}
	return clone
}
