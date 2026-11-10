package cpa

import (
	"encoding/json"
	"strings"
	"time"

	"github.com/WenPaul/cpa-plugin-step2api/internal/pluginapi"
)

const (
	blockedTTLQuota   = 60 * time.Minute
	blockedTTLRateLimit = 90 * time.Second
)

func isUpstreamQuotaText(text string) bool {
	lower := strings.ToLower(text)
	return strings.Contains(lower, "no left credit") || strings.Contains(lower, "insufficient") || strings.Contains(lower, "quota")
}

func isUpstreamRateText(text string) bool {
	lower := strings.ToLower(text)
	return strings.Contains(lower, "rate_limited") || strings.Contains(lower, "concurrency") || strings.Contains(lower, "too many requests")
}

func normalizeBlockKey(model string) string {
	key := strings.ToLower(strings.TrimSpace(model))
	if i := strings.IndexByte(key, '('); i > 0 {
		key = strings.TrimSpace(key[:i])
	}
	return key
}

func (p *Plugin) markModelBlocked(model string, until time.Time) {
	model = normalizeBlockKey(model)
	if model == "" || until.IsZero() {
		return
	}
	p.blockedMu.Lock()
	if p.blockedUntil == nil {
		p.blockedUntil = make(map[string]time.Time)
	}
	p.blockedUntil[model] = until
	p.blockedMu.Unlock()
}

func (p *Plugin) clearModelBlocked(model string) {
	model = normalizeBlockKey(model)
	if model == "" {
		return
	}
	p.blockedMu.Lock()
	delete(p.blockedUntil, model)
	p.blockedMu.Unlock()
}

func (p *Plugin) modelBlocked(model string) bool {
	model = normalizeBlockKey(model)
	if model == "" {
		return false
	}
	p.blockedMu.RLock()
	until, ok := p.blockedUntil[model]
	p.blockedMu.RUnlock()
	return ok && time.Now().Before(until)
}

func (p *Plugin) handleRequestComplete(raw []byte) (any, error) {
	var req pluginapi.RequestCompletion
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &req)
	}
	model := strings.ToLower(strings.TrimSpace(req.Model))
	if model == "" {
		model = strings.ToLower(strings.TrimSpace(req.RequestedModel))
	}
	if model == "" {
		return map[string]any{"completed": true}, nil
	}
	if req.Outcome == pluginapi.RequestCompletionCanceled {
		return map[string]any{"completed": true}, nil
	}
	text := strings.ToLower(req.Error)
	switch {
	case isUpstreamQuotaText(text):
		p.markModelBlocked(model, time.Now().Add(blockedTTLQuota))
	case req.StatusCode == 429 || isUpstreamRateText(text):
		p.markModelBlocked(model, time.Now().Add(blockedTTLRateLimit))
	}
	return map[string]any{"completed": true}, nil
}
