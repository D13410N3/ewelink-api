package bridge

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"
)

type Remote interface {
	FetchDevices(context.Context) ([]Device, error)
	ControlSwitch(context.Context, Device, string) error
}

type Service struct {
	remote  Remote
	cache   Cache
	metrics deviceMetrics
	logger  *slog.Logger
}

func NewService(remote Remote, logger *slog.Logger) *Service {
	return NewServiceWithCache(remote, NewMemoryCache(), noopMetrics{}, logger)
}

func NewServiceWithCache(remote Remote, cache Cache, metrics deviceMetrics, logger *slog.Logger) *Service {
	if metrics == nil {
		metrics = noopMetrics{}
	}
	return &Service{remote: remote, cache: cache, metrics: metrics, logger: logger}
}

func (s *Service) Refresh(ctx context.Context) error {
	devices, err := s.remote.FetchDevices(ctx)
	if err != nil {
		if cacheErr := s.cache.SetRefreshError(ctx, err); cacheErr != nil {
			return fmt.Errorf("fetch devices: %w; record refresh error: %v", err, cacheErr)
		}
		return err
	}
	if err := s.cache.Replace(ctx, devices, time.Now().UTC()); err != nil {
		return err
	}
	s.metrics.RecordRefresh(devices)
	s.logger.Info("refreshed eWeLink device cache", "devices", len(devices))
	return nil
}

func (s *Service) RefreshLoop(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := s.Refresh(ctx); err != nil {
				s.logger.Error("eWeLink device refresh failed; retaining previous snapshot", "error", err)
			}
		}
	}
}

func (s *Service) Snapshot(ctx context.Context) (Snapshot, error) { return s.cache.Snapshot(ctx) }
func (s *Service) Device(ctx context.Context, id string) (Device, bool, error) {
	return s.cache.Get(ctx, id)
}

// Switch sends one control request and, only after its success, updates the
// in-memory state. The local value lasts only until the following refresh.
func (s *Service) Switch(ctx context.Context, id, state string) (Device, error) {
	device, ok, err := s.cache.Get(ctx, id)
	if err != nil {
		return Device{}, err
	}
	if !ok {
		return Device{}, fmt.Errorf("device %q was not found", id)
	}

	state, err = requestedState(device, state)
	if err != nil {
		return Device{}, err
	}
	if err := s.remote.ControlSwitch(ctx, device, state); err != nil {
		return Device{}, err
	}
	if err := s.cache.SetSwitch(ctx, id, state); err != nil {
		return Device{}, err
	}
	s.metrics.RecordSwitch(device, state)
	updated, _, err := s.cache.Get(ctx, id)
	if err != nil {
		return Device{}, err
	}
	return updated, nil
}

func requestedState(device Device, state string) (string, error) {
	state = strings.ToLower(strings.TrimSpace(state))
	switchValue, ok := device.Params["switch"].(string)
	if !ok || (switchValue != "on" && switchValue != "off") {
		return "", fmt.Errorf("device %q does not expose a scalar params.switch state", device.ID)
	}

	switch state {
	case "on", "off":
		return state, nil
	case "":
		if switchValue == "on" {
			return "off", nil
		}
		return "on", nil
	default:
		return "", fmt.Errorf("unsupported state %q; use on or off, or omit state to toggle", state)
	}
}
