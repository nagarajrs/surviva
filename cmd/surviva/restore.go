package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"

	awsconfig "github.com/aws/aws-sdk-go-v2/config"

	"surviva/internal/ebsmount"
	"surviva/internal/ipc"
	"surviva/internal/job"
	"surviva/internal/remote"
	"surviva/internal/resume"
)

// restoreCmd implements `surviva restore [flags] <job-id>`: the command the
// restore orchestrator (infra/terraform) sends via SSM to a replacement
// instance. It reads the job's DynamoDB record, refuses to proceed unless
// it's CHECKPOINT_COMPLETE, fetches the checkpoint data (S3 download or EBS
// mount), resumes the job (criu or a hook), and re-registers the resumed
// process with the local daemon so it's protected again.
func restoreCmd(args []string) int {
	fs := flag.NewFlagSet("restore", flag.ExitOnError)
	dynamoDBTable := fs.String("dynamodb-table", "", "DynamoDB table to read job status from (required)")
	awsRegion := fs.String("aws-region", "", "AWS region override (defaults to the normal environment/instance region resolution)")
	checkpointDir := fs.String("checkpoint-dir", ipc.DefaultCheckpointDir(), "local directory to stage (S3 mode) or mount (EBS mode) checkpoint images under")
	socketPath := fs.String("socket", ipc.DefaultSocketPath(), "path to the surviva daemon socket, to re-track the restored process")
	timeout := fs.Duration("timeout", 5*time.Minute, "overall time budget for the restore")
	_ = fs.Parse(args)

	rest := fs.Args()
	if len(rest) != 1 {
		fmt.Fprintln(os.Stderr, "usage: surviva restore [flags] <job-id>")
		return 2
	}
	jobID := rest[0]

	if *dynamoDBTable == "" {
		fmt.Fprintln(os.Stderr, "surviva restore: -dynamodb-table is required")
		return 2
	}

	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()

	var awsCfgOpts []func(*awsconfig.LoadOptions) error
	if *awsRegion != "" {
		awsCfgOpts = append(awsCfgOpts, awsconfig.WithRegion(*awsRegion))
	}
	awsCfg, err := awsconfig.LoadDefaultConfig(ctx, awsCfgOpts...)
	if err != nil {
		fmt.Fprintf(os.Stderr, "surviva restore: load AWS config: %v\n", err)
		return 1
	}
	statusStore := remote.NewStatusStore(awsCfg, *dynamoDBTable)

	rec, err := statusStore.Get(ctx, jobID)
	if err != nil {
		fmt.Fprintf(os.Stderr, "surviva restore: %v\n", err)
		return 1
	}
	if rec == nil {
		fmt.Fprintf(os.Stderr, "surviva restore: job %s not found\n", jobID)
		return 1
	}
	if rec.Status != string(job.StatusCheckpointComplete) {
		fmt.Fprintf(os.Stderr, "surviva restore: refusing to restore job %s: status is %s, not %s\n", jobID, rec.Status, job.StatusCheckpointComplete)
		return 1
	}

	if err := statusStore.Restoring(ctx, jobID); err != nil {
		fmt.Fprintf(os.Stderr, "surviva restore: warning: failed to mark restoring: %v\n", err)
	}

	localDir := filepath.Join(*checkpointDir, jobID)

	switch rec.StorageType {
	case "s3":
		if rec.S3URI == "" {
			return fail(ctx, statusStore, jobID, fmt.Errorf("job %s is storage_type=s3 but has no s3_uri", jobID))
		}
		fmt.Fprintf(os.Stderr, "surviva restore: downloading %s\n", rec.S3URI)
		if err := remote.PullFromURI(ctx, awsCfg, rec.S3URI, localDir); err != nil {
			return fail(ctx, statusStore, jobID, err)
		}

	case "ebs":
		if rec.EBSVolumeID == "" || rec.EBSPath == "" {
			return fail(ctx, statusStore, jobID, fmt.Errorf("job %s is storage_type=ebs but is missing ebs_volume_id/ebs_path", jobID))
		}
		mountPoint := filepath.Dir(rec.EBSPath)
		fmt.Fprintf(os.Stderr, "surviva restore: mounting volume %s at %s\n", rec.EBSVolumeID, mountPoint)
		if err := ebsmount.MountForRestore(ctx, rec.EBSVolumeID, mountPoint); err != nil {
			return fail(ctx, statusStore, jobID, err)
		}
		localDir = rec.EBSPath

	default:
		return fail(ctx, statusStore, jobID, fmt.Errorf("job %s has unknown storage_type %q", jobID, rec.StorageType))
	}

	fmt.Fprintf(os.Stderr, "surviva restore: resuming job %s from %s\n", jobID, localDir)
	pid, err := resume.Run(ctx, localDir, rec.HookResume, jobID)
	if err != nil {
		return fail(ctx, statusStore, jobID, err)
	}
	fmt.Fprintf(os.Stderr, "surviva restore: job %s resumed as pid %d\n", jobID, pid)

	client := ipc.NewClient(*socketPath)
	newJobID, err := client.Register(ipc.RegisterJob{
		PID:            pid,
		PGID:           pid,
		Command:        rec.Command,
		WorkDir:        rec.WorkDir,
		HookCheckpoint: rec.HookCheckpoint,
		HookResume:     rec.HookResume,
		Priority:       rec.Priority,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "surviva restore: warning: resumed but could not register with daemon: %v\n", err)
		fmt.Fprintln(os.Stderr, "surviva restore: the process is running but WITHOUT interruption protection")
	} else {
		fmt.Fprintf(os.Stderr, "surviva restore: tracking resumed job as %s\n", newJobID)
	}

	if err := statusStore.Restored(ctx, jobID); err != nil {
		fmt.Fprintf(os.Stderr, "surviva restore: warning: resumed successfully but failed to mark restored: %v\n", err)
	}

	return 0
}

func fail(ctx context.Context, statusStore *remote.StatusStore, jobID string, cause error) int {
	fmt.Fprintf(os.Stderr, "surviva restore: job %s FAILED: %v\n", jobID, cause)
	if err := statusStore.RestoreFailed(ctx, jobID, cause.Error()); err != nil {
		fmt.Fprintf(os.Stderr, "surviva restore: warning: failed to mark restore failed: %v\n", err)
	}
	return 1
}
