// Package aws adapts internal/imds's Spot rebalance/interruption polling to
// the provider.Provider interface.
package aws

import (
	"context"
	"time"

	"surviva/internal/imds"
)

// Provider polls EC2's Instance Metadata Service for Spot signals.
type Provider struct {
	poller *imds.Poller
}

// New returns a Provider polling at the given interval (config.PollInterval).
func New(pollInterval time.Duration) *Provider {
	return &Provider{poller: imds.NewPoller(imds.NewClient(), pollInterval)}
}

// Run implements provider.Provider: both IMDS signals (rebalance
// recommendation, interruption notice) are reported through the single
// onSignal callback, distinguished only by the trigger string, since
// daemon's interruption fan-out reacts identically to either.
func (p *Provider) Run(ctx context.Context, onSignal func(trigger string)) {
	p.poller.OnRebalanceRecommendation = func(*imds.RebalanceRecommendation) {
		onSignal("rebalance-recommendation")
	}
	p.poller.OnInterruptionNotice = func(*imds.InstanceAction) {
		onSignal("interruption-notice")
	}
	p.poller.Run(ctx)
}
