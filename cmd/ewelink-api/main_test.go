package main

import (
	"context"
	"testing"
)

func TestNewCacheDefaultsToMemory(t *testing.T) {
	t.Setenv("CACHE_MODE", "")
	cache, err := newCache(context.Background())
	if err != nil {
		t.Fatalf("newCache() error = %v", err)
	}
	defer cache.Close()

	if _, err := cache.Snapshot(context.Background()); err != nil {
		t.Fatalf("memory cache Snapshot() error = %v", err)
	}
}

func TestNewCacheRejectsUnknownMode(t *testing.T) {
	t.Setenv("CACHE_MODE", "filesystem")
	if _, err := newCache(context.Background()); err == nil {
		t.Fatal("newCache() error = nil, want invalid mode error")
	}
}
