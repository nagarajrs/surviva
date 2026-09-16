package imds

import (
	"context"
	"log"
	"time"
)

// pollInterval matches AWS's guidance to check IMDS every 5 seconds so a
// 2-minute interruption notice isn't missed.
const pollInterval = 5 * time.Second

// Poller watches IMDS for the two Spot signals surviva reacts to. Both are
// edge-triggered: each callback fires at most once per Poller.Run call, the
// first time its signal is observed.
type Poller struct {
	Client *Client

	// OnRebalanceRecommendation fires at most once, the first time a
	// rebalance recommendation is seen. It is a best-effort early signal
	// with no guaranteed lead time — never a substitute for reacting to
	// OnInterruptionNotice.
	OnRebalanceRecommendation func(*RebalanceRecommendation)

	// OnInterruptionNotice fires at most once, the first time a Spot
	// interruption notice is seen. This is the hard ~2-minute deadline;
	// Run stops polling immediately after this fires.
	OnInterruptionNotice func(*InstanceAction)
}

// NewPoller returns a Poller using the given IMDS client.
func NewPoller(client *Client) *Poller {
	return &Poller{Client: client}
}

// Run polls IMDS until ctx is cancelled or an interruption notice fires.
func (p *Poller) Run(ctx context.Context) {
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()

	seenRebalance := false
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if !seenRebalance {
				rr, err := p.Client.RebalanceRecommendation(ctx)
				if err != nil {
					log.Printf("imds: rebalance recommendation check failed: %v", err)
				} else if rr != nil {
					seenRebalance = true
					log.Printf("imds: rebalance recommendation received (notice_time=%s)", rr.NoticeTime)
					if p.OnRebalanceRecommendation != nil {
						p.OnRebalanceRecommendation(rr)
					}
				}
			}

			ia, err := p.Client.SpotInstanceAction(ctx)
			if err != nil {
				log.Printf("imds: interruption notice check failed: %v", err)
				continue
			}
			if ia != nil {
				log.Printf("imds: interruption notice received (action=%s time=%s)", ia.Action, ia.Time)
				if p.OnInterruptionNotice != nil {
					p.OnInterruptionNotice(ia)
				}
				return
			}
		}
	}
}
