//go:build !linux

package ebsmount

import (
	"context"
	"fmt"
)

// MountForRestore is unsupported outside Linux; surviva's EBS restore path
// only targets Linux (real EC2 instances).
func MountForRestore(ctx context.Context, volumeID, mountPoint string) error {
	return fmt.Errorf("EBS restore is only supported on Linux")
}
