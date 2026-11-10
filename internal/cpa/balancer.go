package cpa

import (
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	"github.com/WenPaul/cpa-plugin-step2api/internal/stepstudio"
)

const studioBlockTTL = 60 * time.Second

var legCounter uint64

func (p *Plugin) studioBlocked(model string) bool {
	return p.modelBlocked("studio:" + strings.ToLower(strings.TrimSpace(model)))
}

func (p *Plugin) markStudioBlocked(model string) {
	p.markModelBlocked("studio:"+model, time.Now().Add(studioBlockTTL))
}

// shouldRouteToStudio implements the Plan/Studio fill (weighted round-robin).
// Plan keeps the configured share; Studio receives the rest so both channels are
// continuously exercised. A recorded Plan quota failure or a Studio saturation
// temporarily overrides the weights.
func (p *Plugin) shouldRouteToStudio(model string) bool {
	model = strings.ToLower(strings.TrimSpace(model))
	if model == "" {
		return false
	}
	if p.modelBlocked(model) {
		return true
	}
	if p.studioBlocked(model) {
		return false
	}
	planWeight := p.Config().PlanWeight
	studioWeight := p.Config().StudioWeight
	if planWeight < 0 {
		planWeight = 0
	}
	if studioWeight < 0 {
		studioWeight = 0
	}
	total := planWeight + studioWeight
	if total <= 0 {
		return false
	}
	n := atomic.AddUint64(&legCounter, 1) - 1
	return int(n%uint64(total)) >= planWeight
}

func (p *Plugin) noteStudioUpstreamFailure(model string, err error) {
	apiErr, ok := err.(*stepstudio.APIError)
	if !ok || apiErr == nil {
		return
	}
	if apiErr.StatusCode == http.StatusTooManyRequests || isUpstreamRateText(apiErr.Message) || isUpstreamQuotaText(apiErr.Message) {
		p.markStudioBlocked(model)
	}
}
