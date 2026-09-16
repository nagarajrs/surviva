// Package aws adapts internal/imds's Spot rebalance/interruption polling to
// the provider.Provider interface.
package aws

import (
	"context"
	"log"
	"time"

	"surviva/internal/daemon"
	"surviva/internal/imds"
)

// Provider polls EC2's Instance Metadata Service for Spot signals.
type Provider struct {
	client *imds.Client
	poller *imds.Poller
}

// New returns a Provider polling at the given interval (config.PollInterval).
func New(pollInterval time.Duration) *Provider {
	client := imds.NewClient()
	return &Provider{client: client, poller: imds.NewPoller(client, pollInterval)}
}

// Run implements provider.Provider: both IMDS signals (rebalance
// recommendation, interruption notice) are reported through the single
// onSignal callback, distinguished by Signal.Trigger, since daemon's
// interruption fan-out reacts identically to either. Signal.Detail is
// populated best-effort from IMDS (instance id, AZ, and whichever of
// action/notice_time applies) -- absent/empty on failure, never fatal, same
// tolerance the rest of the AWS integration already gives IMDS calls.
func (p *Provider) Run(ctx context.Context, onSignal func(daemon.Signal)) {
	p.poller.OnRebalanceRecommendation = func(rr *imds.RebalanceRecommendation) {
		detail := p.baseDetail(ctx)
		detail["notice_time"] = rr.NoticeTime.UTC().Format(time.RFC3339)
		onSignal(daemon.Signal{Trigger: "rebalance-recommendation", Detail: detail})
	}
	p.poller.OnInterruptionNotice = func(ia *imds.InstanceAction) {
		detail := p.baseDetail(ctx)
		detail["action"] = ia.Action
		detail["notice_time"] = ia.Time.UTC().Format(time.RFC3339)
		onSignal(daemon.Signal{Trigger: "interruption-notice", Detail: detail})
	}
	p.poller.Run(ctx)
}

// baseDetail fetches instance id/AZ, best-effort -- a failure here must
// never block reporting the signal itself, so it's logged and the key is
// simply omitted rather than returned as an error.
func (p *Provider) baseDetail(ctx context.Context) map[string]string {
	detail := map[string]string{}
	if id, err := p.client.InstanceID(ctx); err != nil {
		log.Printf("aws provider: instance id unavailable: %v", err)
	} else {
		detail["instance_id"] = id
	}
	if az, err := p.client.AvailabilityZone(ctx); err != nil {
		log.Printf("aws provider: availability zone unavailable: %v", err)
	} else {
		detail["availability_zone"] = az
	}
	return detail
}
