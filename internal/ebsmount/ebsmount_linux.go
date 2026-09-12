//go:build linux

// Package ebsmount locates and mounts an EBS checkpoint volume on the
// instance restore is running on, by volume id alone — the volume was
// attached by the restore orchestrator (see infra/terraform), which knows
// nothing about device names, only the volume id from the job's DynamoDB
// record.
package ebsmount

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// byIDPrefix is the udev-managed symlink AWS's NVMe driver creates on
// Nitro-based instances, one per attached EBS volume, keyed by volume id
// with the "vol-" dash removed. This is documented, stable AWS behavior
// (see "Identify EBS volumes" in the EC2 user guide) — the actual
// /dev/nvthisor device number a volume gets can vary across attach cycles,
// but this symlink doesn't.
const byIDPrefix = "/dev/disk/by-id/nvme-Amazon_Elastic_Block_Store_"

// waitForDevice bounds how long to wait for the by-id symlink to appear
// after the orchestrator's AttachVolume call reported the volume attached;
// udev can lag slightly behind the kernel registering the block device.
const waitForDevice = 30 * time.Second

// MountForRestore mounts volumeID at mountPoint, unless something is
// already mounted there. The volume's filesystem root is assumed to BE
// the checkpoint directory tree written by surviva daemon's EBS storage
// path — nothing else is expected to be stored on it.
func MountForRestore(ctx context.Context, volumeID, mountPoint string) error {
	if mounted, err := isMounted(mountPoint); err != nil {
		return err
	} else if mounted {
		return nil
	}

	device, err := resolveDevice(ctx, volumeID)
	if err != nil {
		return err
	}

	if err := os.MkdirAll(mountPoint, 0o755); err != nil {
		return fmt.Errorf("create mount point %s: %w", mountPoint, err)
	}

	out, err := exec.CommandContext(ctx, "mount", device, mountPoint).CombinedOutput()
	if err != nil {
		return fmt.Errorf("mount %s (volume %s) at %s: %w\n%s", device, volumeID, mountPoint, err, out)
	}
	return nil
}

func resolveDevice(ctx context.Context, volumeID string) (string, error) {
	byID := byIDPrefix + strings.Replace(volumeID, "-", "", 1)

	deadline := time.Now().Add(waitForDevice)
	for {
		if resolved, err := filepath.EvalSymlinks(byID); err == nil {
			return resolved, nil
		}
		if time.Now().After(deadline) {
			return "", fmt.Errorf("timed out waiting for %s (volume %s never appeared as an NVMe device — is this a Nitro-based instance with the volume actually attached?)", byID, volumeID)
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(time.Second):
		}
	}
}

func isMounted(mountPoint string) (bool, error) {
	data, err := os.ReadFile("/proc/self/mounts")
	if err != nil {
		return false, fmt.Errorf("read /proc/self/mounts: %w", err)
	}
	target := filepath.Clean(mountPoint)
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 2 && filepath.Clean(fields[1]) == target {
			return true, nil
		}
	}
	return false, nil
}
