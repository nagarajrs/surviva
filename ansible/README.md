# surviva sandbox (Ansible)

Two playbooks that stand up, and then completely tear down, a real,
disposable environment for trying out surviva end to end: a real Spot
instance with CRIU + surviva pre-installed, this project's own
`infra/terraform` control-plane (DynamoDB status table, checkpoint storage,
the restore Step Functions orchestrator, EventBridge rule), and a
ready-to-fire AWS Fault Injection Simulator (FIS) experiment template that
sends a real Spot interruption to the sandbox instance on demand.

This is real AWS infrastructure and it costs real money for as long as it
exists (mainly: one running `t3.micro`-class instance, and briefly a second
one while the AMI is being baked). Run `destroy_sandbox.yml` when you're
done — see [Cost](#cost) below.

## What you get

- A real Spot EC2 instance running `surviva-daemon`, with a demo job already
  tracked (`surviva list` shows it immediately).
- The full self-healing loop working out of the box: fire the FIS
  experiment, watch AWS interrupt the instance, and watch surviva's
  orchestrator checkpoint, wait, launch a replacement, and restore the job —
  no manual steps.
- Your choice of checkpoint storage: S3 (default, simpler) or EBS (a real
  attached checkpoint volume, exercises AZ-pinning) via `storage_mode`.

## Prerequisites

- AWS credentials resolvable by the AWS CLI (`aws sts get-caller-identity`
  must succeed) for an account you're comfortable creating real, billable
  resources in.
- `aws` CLI, `terraform` (>= 1.5), `go` (matching this repo's `go.mod`), and
  `ansible-core` >= 2.15 all on `PATH`.
- The `amazon.aws` collection: `ansible-galaxy collection install -r requirements.yml`
- A VPC subnet you're willing to launch instances into, and its availability
  zone. There is deliberately no default-VPC auto-discovery — set both in
  `group_vars/all.yml`, or pass them with `-e`:

  ```bash
  ansible-playbook playbooks/setup_sandbox.yml \
    -e subnet_id=subnet-0123456789abcdef0 \
    -e availability_zone=us-east-1a
  ```

## Running it

```bash
cd ansible
ansible-galaxy collection install -r requirements.yml

# S3 storage mode (default)
ansible-playbook playbooks/setup_sandbox.yml -e subnet_id=... -e availability_zone=...

# or EBS storage mode
ansible-playbook playbooks/setup_sandbox.yml -e subnet_id=... -e availability_zone=... -e storage_mode=ebs
```

`setup_sandbox.yml` takes a while (the AMI bake, in particular, compiles
CRIU from source — expect 10-15 minutes total). When it finishes it prints:

- how to connect to the instance (`aws ssm start-session --target ...`)
- how to track your own job (`surviva run -- <your command>`)
- the exact `aws fis start-experiment` command to fire a real Spot
  interruption and watch the self-healing loop run
- how to tear it all down

Resource ids are also cached locally in the (gitignored) `.sandbox_facts/`
directory.

**Picking a command to try `surviva run -- <your command>` with:** CRIU
restores a checkpointed process's open file descriptors by reopening them at
their original absolute path, so anything your command reads from, writes
to, or redirects output to must exist at that same path on a replacement
instance too — not just on the one you launched it on. A path under `/tmp`
or anywhere else instance-local won't be there after a real interruption,
and restore will fail outright. Redirect output to `/dev/null` (present on
every instance identically), or pick a command that doesn't touch the
filesystem at all, such as the bundled demo job (`sleep`, discoverable via
`surviva list`).

When you're done:

```bash
ansible-playbook playbooks/destroy_sandbox.yml
```

`destroy_sandbox.yml` doesn't rely on that cache — it re-discovers every
real resource live, by tag, so it also finds and removes an
orchestrator-launched replacement instance if you actually fired the FIS
experiment. It ends by re-checking that nothing sandbox-tagged is left.

## Terraform state

The sandbox runs Terraform in its own workspace (`sandbox`, in
`infra/terraform/`) so it never touches or gets confused by any state
already sitting in that directory's default workspace from your own manual
testing.

## Notes

- If you run these playbooks from a directory ansible considers "world
  writable" (this is common for a Windows path mounted into WSL under
  `/mnt/c/...`), Ansible ignores `ansible.cfg` and prints a warning. Nothing
  here depends on it beyond cosmetics — every play targets `localhost`
  explicitly — but if you want `ansible.cfg` honored, either run from a
  native Linux filesystem path or `export ANSIBLE_CONFIG=$(pwd)/ansible.cfg`.
- `create_fis_demo_template` (default `true`) only creates the experiment
  template — it is never auto-fired.
