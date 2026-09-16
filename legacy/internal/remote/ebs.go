package remote

import (
	"context"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
)

// ValidateEBSVolume checks that volumeID is currently attached (to
// instanceID, when known) with DeleteOnTermination=false — the safety
// requirement that makes the EBS checkpoint path durable across Spot
// termination. A volume that would be deleted along with the instance is
// worthless as a checkpoint destination, so surviva refuses to start
// rather than silently checkpointing to a volume that won't survive.
func ValidateEBSVolume(ctx context.Context, cfg aws.Config, volumeID, instanceID string) error {
	client := ec2.NewFromConfig(cfg)
	out, err := client.DescribeVolumes(ctx, &ec2.DescribeVolumesInput{VolumeIds: []string{volumeID}})
	if err != nil {
		return fmt.Errorf("describe volume %s: %w", volumeID, err)
	}
	if len(out.Volumes) == 0 {
		return fmt.Errorf("volume %s not found", volumeID)
	}

	vol := out.Volumes[0]
	if len(vol.Attachments) == 0 {
		return fmt.Errorf("volume %s is not attached to any instance", volumeID)
	}
	att := vol.Attachments[0]

	if instanceID != "" && aws.ToString(att.InstanceId) != instanceID {
		return fmt.Errorf("volume %s is attached to instance %s, not this instance (%s)", volumeID, aws.ToString(att.InstanceId), instanceID)
	}
	if att.DeleteOnTermination == nil || *att.DeleteOnTermination {
		return fmt.Errorf("volume %s has DeleteOnTermination=true — surviva refuses to checkpoint to a volume that would be deleted along with the instance on Spot termination; set DeleteOnTermination=false on this attachment", volumeID)
	}
	return nil
}
