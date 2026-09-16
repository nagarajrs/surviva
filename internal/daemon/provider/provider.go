// Package provider defines the interface daemon uses to watch for a cloud
// interruption/rebalance signal, independent of which cloud it's running
// on. AWS is the only implementation today (internal/daemon/provider/aws);
// Azure/GCP become new packages implementing the same interface later, once
// config accepts those provider names.
package provider

import "context"

// Provider watches for an interruption/rebalance-style signal and calls
// onSignal (at most once per real signal) until ctx is done.
type Provider interface {
	Run(ctx context.Context, onSignal func(trigger string))
}
