package models

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/HeyJiqingCode/CopilotGateway/internal/copilot"
	"github.com/HeyJiqingCode/CopilotGateway/internal/upstream"
	"golang.org/x/sync/singleflight"
)

// Info contains model metadata including supported endpoints.
type Info struct {
	ID                 string       `json:"id"`
	SupportedEndpoints []string     `json:"supported_endpoints"`
	Capabilities       Capabilities `json:"capabilities"`
}

// Capabilities 镜像上游 /models 的 capabilities 对象(仅解析我们需要的部分)。
type Capabilities struct {
	Supports Supports `json:"supports"`
}

// Supports 描述模型支持的特性。
type Supports struct {
	// ReasoningEffort 缺失或为空 = 模型不接受 reasoning effort(如 claude-haiku-4.5)。
	ReasoningEffort []string `json:"reasoning_effort"`
}

// modelsListResponse is the response from the /models endpoint.
type modelsListResponse struct {
	Data []Info `json:"data"`
}

// cacheData holds both representations derived from a single /models fetch.
type cacheData struct {
	raw    []byte           // original JSON for serving GET /v1/models
	parsed map[string]*Info // keyed by model ID for capability lookups
}

// Cache is a shared, single-fetch cache for the /models endpoint.
// It stores both the raw JSON (for proxying) and the parsed model map
// (for capability detection), populated from one upstream call.
type Cache struct {
	mu       sync.RWMutex
	data     cacheData
	valid    bool
	cachedAt time.Time
	ttl      time.Duration
	sf       singleflight.Group
	upstream *upstream.Client
}

// NewCache creates a shared models cache.
func NewCache(upstreamClient *upstream.Client, ttl time.Duration) *Cache {
	return &Cache{
		upstream: upstreamClient,
		ttl:      ttl,
	}
}

// Warm pre-populates the cache to avoid cold-cache latency on the first request.
func (c *Cache) Warm(ctx context.Context) {
	if _, _, err := c.get(ctx); err != nil {
		slog.Warn("failed to warm models cache", "error", err)
		return
	}
	slog.Info("models cache warmed")
}

// GetRaw returns the raw JSON bytes of the /models response.
func (c *Cache) GetRaw(ctx context.Context) ([]byte, error) {
	raw, _, err := c.get(ctx)
	return raw, err
}

// GetInfo returns the parsed model info map.
func (c *Cache) GetInfo(ctx context.Context) (map[string]*Info, error) {
	_, parsed, err := c.get(ctx)
	return parsed, err
}

// CachedAt 返回缓存最近一次成功刷新的时间;尚未填充时为零值。
func (c *Cache) CachedAt() time.Time {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.cachedAt
}

func (c *Cache) get(ctx context.Context) ([]byte, map[string]*Info, error) {
	// Fast path: read lock check.
	c.mu.RLock()
	if c.valid && time.Since(c.cachedAt) < c.ttl {
		d := c.data
		c.mu.RUnlock()
		return d.raw, d.parsed, nil
	}
	c.mu.RUnlock()

	slog.Debug("models cache miss, fetching from upstream")

	// Slow path: singleflight to deduplicate concurrent fetches.
	result, err, _ := c.sf.Do("fetch", func() (interface{}, error) {
		// Double-check after acquiring singleflight.
		c.mu.RLock()
		if c.valid && time.Since(c.cachedAt) < c.ttl {
			d := c.data
			c.mu.RUnlock()
			return d, nil
		}
		c.mu.RUnlock()

		d, err := c.fetch(context.WithoutCancel(ctx))
		if err != nil {
			return nil, err
		}

		c.mu.Lock()
		c.data = d
		c.valid = true
		c.cachedAt = time.Now()
		c.mu.Unlock()

		slog.Debug("models cache refreshed", "count", len(d.parsed))
		return d, nil
	})

	if err != nil {
		return nil, nil, err
	}
	d := result.(cacheData)
	return d.raw, d.parsed, nil
}

// Refresh 立即从上游重新拉取目录并替换缓存,不等 TTL 过期;失败时保留原有缓存。
func (c *Cache) Refresh(ctx context.Context) error {
	_, err, _ := c.sf.Do("refresh", func() (interface{}, error) {
		d, err := c.fetch(context.WithoutCancel(ctx))
		if err != nil {
			return nil, err
		}
		c.mu.Lock()
		c.data = d
		c.valid = true
		c.cachedAt = time.Now()
		c.mu.Unlock()
		slog.Debug("models cache refreshed on demand", "count", len(d.parsed))
		return nil, nil
	})
	return err
}

func (c *Cache) fetch(ctx context.Context) (cacheData, error) {
	_, respData, err := c.upstream.Do(ctx, upstream.Request{
		Method:       "GET",
		Endpoint:     "/models",
		ExtraHeaders: copilot.ModelAccessHeaders(copilot.GenerateRequestID()),
	})
	if err != nil {
		return cacheData{}, fmt.Errorf("models request failed: %w", err)
	}

	var modelsResp modelsListResponse
	if err := json.Unmarshal(respData, &modelsResp); err != nil {
		return cacheData{}, fmt.Errorf("failed to parse models response: %w", err)
	}

	parsed := make(map[string]*Info, len(modelsResp.Data))
	for i := range modelsResp.Data {
		parsed[modelsResp.Data[i].ID] = &modelsResp.Data[i]
	}

	return cacheData{raw: respData, parsed: parsed}, nil
}

// SupportsReasoningEffort 报告模型是否接受 reasoning effort(output_config.effort / thinking)。
// 模型通过非空的 capabilities.supports.reasoning_effort 数组声明该能力。
// 未知模型(info == nil)返回 true —— 保守地不剥离请求字段,交由上游裁决。
func SupportsReasoningEffort(info *Info) bool {
	if info == nil {
		return true
	}
	return len(info.Capabilities.Supports.ReasoningEffort) > 0
}
