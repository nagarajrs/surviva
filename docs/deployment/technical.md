# Deployment Runbook

Practical, validated steps to deploy surviva into a real AWS account. This exact sequence was run end-to-end against AWS, including a genuine AWS Fault Injection Simulator (FIS) triggered Spot interruption. For flag semantics see `../api-cli/technical.md`; for the system model see `../architecture/technical.md`; for full IAM JSON and per-file responsibilities see `../components/technical.md`; for the full FIS validation procedure see `../testing-strategy/technical.md`.

## 1. Bake an AMI (CRIU + surviva pre-installed)

**Do not** install or compile CRIU/surviva at instance boot time. A real timing race was found: the orchestrator's SSM `surviva restore <job-id>` command can arrive on the replacement instance before a boot-time install/build finishes, and the command fails outright. Bake everything into the AMI instead.

Base AMI used: Amazon Linux 2023 (Fedora-derived, has SSM agent pre-installed).

**CRIU — build from source, do not use the dnf package.** AL2023's `criu` dnf package (3.17.1 at time of testing) segfaults on `criu restore` on the current AL2023 kernel. A source build of criu 4.2 restores correctly on the same kernel — verified directly against this exact instance/kernel with a real dump+restore cycle, both via plain `criu` commands and through the actual `surviva daemon`/`surviva run` code path.

```bash
dnf install -y git gcc make protobuf-c-devel protobuf-c-compiler libnl3-devel \
  libnet-devel libcap-devel python3-protobuf libbsd-devel iproute \
  pkgconf-pkg-config protobuf-devel protobuf-compiler libuuid-devel

git clone --depth 1 --branch v4.2 https://github.com/checkpoint-restore/criu.git
cd criu
make -j$(nproc)
make install-criu   # installs to /usr/local/sbin/criu
which -a criu        # confirm /usr/local/sbin/criu precedes any /usr/bin or /usr/sbin criu on PATH
```

Two things changed versus the 3.19 build used earlier in this project (see `../design-record/technical.md`): `libuuid-devel` is now a required build dependency (4.2's build fails with a clear "Can not find some of the required libraries" error without it, listing `libuuid-devel` for RPM-based distros / `uuid-dev` for Debian-based); and `WERROR=0` is no longer needed — 4.2 builds cleanly under AL2023's/Ubuntu 24.04's newer GCC without disabling `-Werror`, unlike 3.19.

**surviva binary:**

```bash
GOOS=linux GOARCH=amd64 go build -o surviva ./cmd/surviva
# copy to the builder instance, then:
install -m 0755 surviva /usr/local/bin/surviva
```

**Boot script** `/usr/local/bin/surviva-boot.sh` — reads deployment config from EC2 user-data at boot, so the same AMI works unmodified for both the original instance and any auto-launched replacement:

```bash
#!/bin/bash
set -e
TOKEN=$(curl -s -X PUT http://169.254.169.254/latest/api/token -H 'X-aws-ec2-metadata-token-ttl-seconds: 21600')
USERDATA=$(curl -s -H "X-aws-ec2-metadata-token: $TOKEN" http://169.254.169.254/latest/user-data)
eval "$USERDATA"
exec /usr/local/bin/surviva daemon \
  --s3-bucket "${SURVIVA_S3_BUCKET:-}" \
  --dynamodb-table "${SURVIVA_TABLE:-}" \
  --aws-region "${SURVIVA_AWS_REGION:-us-east-1}"
```

(Swap `--s3-bucket` for `--ebs-volume-id "${SURVIVA_EBS_VOLUME_ID:-}"` if this fleet uses EBS checkpoint storage instead of S3 — see `../api-cli/technical.md` for the daemon's mutual-exclusivity rules.)

**systemd unit** `/etc/systemd/system/surviva-daemon.service`:

```ini
[Unit]
Description=surviva daemon
After=network-online.target
Wants=network-online.target

[Service]
ExecStart=/usr/local/bin/surviva-boot.sh
Restart=on-failure
RestartSec=5
StandardOutput=journal
StandardError=journal

[Install]
WantedBy=multi-user.target
```

```bash
systemctl daemon-reload
systemctl enable surviva-daemon   # enable, don't start — no user-data exists on the builder itself
```

**Create the image:**

```bash
aws ec2 create-image --instance-id <builder-instance-id> --name surviva-<version>
aws ec2 wait image-available --image-ids <ami-id>
aws ec2 terminate-instances --instance-ids <builder-instance-id>
```

`create-image` reboots the source instance once for filesystem consistency; that's expected.

## 2. Deploy the Terraform module

The module (`infra/terraform/`) creates: the DynamoDB jobs table (+ `instance_id-index` GSI), the EC2 instance role/profile, the Step Functions state machine, the EventBridge rule, and — if `enable_s3_bucket = true` — the checkpoint S3 bucket.

`launch_template_id` is a required input, which creates a chicken-and-egg with step 3 (the launch template needs the instance profile Terraform creates; Terraform needs the launch template ID). Resolve it in two passes:

```bash
cd infra/terraform
terraform init

# Pass 1: apply with a placeholder launch_template_id to materialize the instance profile
terraform apply -var launch_template_id=lt-placeholder ...

terraform output instance_profile_name   # use this in step 3

# (create the real launch template — step 3)

# Pass 2: apply again with the real launch template ID
terraform apply -var launch_template_id=<real-lt-id> ...
```

Key variables (see `terraform.tfvars.example` for the full set with descriptions):

| Variable | Notes |
|---|---|
| `launch_template_id` | required; see two-pass note above |
| `az_subnet_map` | only needed if any job uses EBS storage — maps AZ → subnet so the replacement instance can be pinned to the same AZ as the checkpoint volume (EBS volumes can't cross AZs) |
| `enable_s3_bucket` | `true` if any job uses S3 checkpoint storage |
| `checkpoint_wait_seconds` | default 110s; must be ≥ the daemon's per-job checkpoint timeout, or the orchestrator will query DynamoDB before checkpointing finishes |
| `restore_ssm_document` | default `AWS-RunShellScript` |

## 3. Create the launch template

References the AMI from step 1 and the `instance_profile_name` output from step 2. Its **default user-data** must bake in this deployment's configuration as shell variable exports, e.g.:

```
export SURVIVA_S3_BUCKET=<checkpoints_bucket_name output>
export SURVIVA_TABLE=<jobs_table_name output>
export SURVIVA_AWS_REGION=us-east-1
```

**Critical, found via real testing:** this must be the launch template's own default user-data, not a per-instance override passed only on the original instance's `RunInstances` call. The orchestrator's own `ec2:RunInstances` call for the replacement instance does not carry over the original instance's user-data, and does not set any of its own — it only gets whatever the launch template's default is. Early testing set user-data only as a `run-instances --user-data` override on the original launch; the original instance worked, but the automatically-launched replacement got no config at all and its daemon crash-looped (`eval "$USERDATA"` on an empty string failed, then repeatedly restarted per `Restart=on-failure`).

As belt-and-suspenders, the orchestrator's SSM restore command already appends `--dynamodb-table <table> --aws-region <region>` explicitly (`state_machine.tf`'s `restore_command_prefix` construction) since `surviva restore` genuinely has no other way to learn the table name. But the **daemon's own** bucket/table config on the replacement has no path except user-data — get this right.

## 4. Launch the real instance

```bash
aws ec2 run-instances \
  --launch-template LaunchTemplateId=<lt-id> \
  --instance-market-options 'MarketType=spot' \
  --subnet-id <subnet-in-desired-az> \
  --tag-specifications 'ResourceType=instance,Tags=[{Key=Name,Value=<name>}]'
```

The daemon starts automatically via systemd. The SSM agent (pre-installed on AL2023) typically registers as `Online` within 1–3 minutes of boot — confirm with `aws ssm describe-instance-information --filters "Key=InstanceIds,Values=<id>"` before relying on SSM RunCommand against it.

## 5. Track a job

```bash
surviva run -- <your long-running command>
```

Run this on the instance (interactively, or via SSM RunCommand). This is the only manual step in steady-state operation — see `../architecture/technical.md` for what happens automatically from here.

## 6. Validate before trusting it in production

Run a real simulated interruption via AWS FIS against a real (non-critical) Spot test instance from the same launch template — the `aws:ec2:send-spot-instance-interruptions` action, targeted by instance ARN with `resourceType: aws:ec2:spot-instance`, parameter `durationBeforeInterruption` (e.g. `PT2M`), using an FIS role with the AWS managed policy `AWSFaultInjectionSimulatorEC2Access`. Full experiment-template JSON and the complete account of what this caught (the two IAM/config bugs above) are in `../testing-strategy/technical.md`. Don't skip this — it is what actually found the bugs above; a read-through of the Terraform would not have.

## 7. IAM summary

Full policy JSON lives in `iam.tf` (see `../components/technical.md` for a per-statement walkthrough). Summary:

- **Instance role** (used by both the original and every replacement instance, since they share a launch template): `dynamodb:PutItem/UpdateItem/GetItem` on the jobs table; `s3:PutObject` + `s3:GetObject` on the checkpoint bucket (S3 mode — note both directions are required: Put for the daemon's push, Get for `surviva restore`'s pull on the replacement); `ec2:DescribeVolumes` (EBS mode); `AmazonSSMManagedInstanceCore` (always — required for the orchestrator's SendCommand to reach it).
- **Step Functions role**: `dynamodb:Query` on the table and its GSI; `ec2:RunInstances`, `ec2:DescribeInstances`, `ec2:DescribeVolumes`, `ec2:AttachVolume`; `ssm:SendCommand`, `ssm:DescribeInstanceInformation`; `iam:PassRole` scoped to the instance role with condition `aws:PassedToService = ec2.amazonaws.com`.
- **EventBridge role**: `states:StartExecution` on this state machine only.

## 8. Teardown

```bash
aws s3 rm s3://<checkpoints-bucket> --recursive   # Terraform can't delete a non-empty bucket
cd infra/terraform && terraform destroy

aws ec2 deregister-image --image-id <ami-id>
aws ec2 delete-snapshot --snapshot-id <snapshot-id>   # from the AMI's block device mapping
aws ec2 delete-launch-template --launch-template-id <lt-id>
aws ec2 terminate-instances --instance-ids <any still running>
```
