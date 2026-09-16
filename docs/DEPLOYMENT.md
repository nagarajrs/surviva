# Deploying surviva

A from-scratch install on a real Linux server (Ubuntu/Debian). Every step
here has actually been run — this isn't a guess at what should work, it's
the same sequence used to verify each module against real CRIU, a real
MySQL server, and real AWS Lambda/Step Functions during development.

## One-shot install

[`../deploy/install.sh`](../deploy/install.sh) automates steps 1–5 below
(build, install CRIU, create directories, write a basic SQLite-backed
`surviva.conf`, install and start the systemd unit) on Ubuntu/Debian,
RHEL-family (RHEL/CentOS/Rocky/Alma/Fedora), or Amazon Linux (2 or 2023).
Idempotent — safe to re-run; never overwrites an existing `surviva.conf`.
Run it from inside a checked-out repo, as root:

```bash
git clone https://github.com/nagarajrs/surviva.git
cd surviva
sudo ./deploy/install.sh
```

`CLOUD_PROVIDER`/`POLL_INTERVAL_SECONDS` env vars override the generated
config's defaults (`aws`/`5`). It does **not** set up MySQL or the Lambda/
Step Functions notify target — add those to `/etc/surviva/surviva.conf` by
hand afterward, per steps 3–4 below, then `sudo systemctl restart
surviva-daemon`.

**Verified against real infra:** Ubuntu (WSL2 and bare systemd), end to
end including a re-run for idempotency. The RHEL-family/Amazon Linux
branches use the same CRIU-build sequence with the analogous `dnf`/`yum`
package names (best-effort EPEL enablement — see the script's comments)
but haven't been run against a real RHEL/Amazon Linux box yet; if a
package name turns out wrong for your distro version, the fix is a
one-line edit to the `amzn|rhel` case in the script.

The manual steps below are what the script automates — useful if you want
to understand or customize what it's doing, or you're on a distro it
doesn't recognize.

## 1. Build

```bash
git clone https://github.com/nagarajrs/surviva.git
cd surviva
go build -o surviva ./cmd/surviva
sudo install -m 0755 surviva /usr/local/bin/surviva
```

## 2. Install CRIU

Ubuntu's own apt repos don't carry a `criu` package. Build the version this
project has actually verified (v4.2) from source:

```bash
sudo apt update
sudo apt install -y build-essential pkg-config \
  libprotobuf-dev libprotobuf-c-dev protobuf-c-compiler protobuf-compiler \
  python3-protobuf libcap-dev libnl-3-dev libnet-dev libbsd-dev iproute2 uuid-dev git

git clone --depth 1 --branch v4.2 https://github.com/checkpoint-restore/criu.git
cd criu
make -j"$(nproc)"
sudo make install-criu
criu --version
sudo criu check   # some checks may warn under a restrictive kernel config -- not necessarily fatal
```

## 3. Create directories and the config file

```bash
sudo mkdir -p /etc/surviva /var/lib/surviva/checkpoints /var/log/surviva
sudo chmod 700 /etc/surviva   # surviva.conf can hold a MySQL password in plaintext -- root-only, not world-readable
sudo chmod 600 /etc/surviva/surviva.conf
```

If `surviva.conf` sets `DBPassword` and somehow ends up group/world-readable
anyway, `surviva daemon`/any `surviva` CLI command prints a warning to
stderr at load time (not a hard failure — see
[specs/config.md](specs/config.md)).

`/etc/surviva/surviva.conf` — the minimal SQLite-backed setup:

```
CloudProvider=aws
PollIntervalSeconds=5
AuditLogPath=/var/log/surviva/audit.log
CheckpointBaseDir=/var/lib/surviva/checkpoints
DBPath=/var/lib/surviva/jobs.db

# Optional: point the job table at MySQL instead of the built-in SQLite file.
# See specs/store.md for the full backend design.
#DBType=mysql
#DBHost=127.0.0.1
#DBPort=3306
#DBUser=surviva
#DBPassword=changeme
#DBName=surviva_jobs

# Optional: notify a Lambda function or Step Functions state machine the
# instant a Spot interruption/rebalance signal is detected. See
# specs/daemon.md for the JSON payload schema. Requires the IAM permissions
# in step 4 below.
#NotifyTargetType=lambda
#NotifyTargetARN=arn:aws:lambda:us-east-1:123456789012:function:my-fn
```

See [specs/config.md](specs/config.md) for the complete directive
reference (every key, type, and validation rule).

## 4. IAM (only relevant with `CloudProvider=aws`)

Spot rebalance/interruption polling talks to the EC2 Instance Metadata
Service directly — no IAM permissions needed for that at all.

If (and only if) `NotifyTargetType` is set, the instance role needs
exactly one of these, scoped to the specific ARN you configured:

```json
{
  "Version": "2012-10-17",
  "Statement": [
    {
      "Effect": "Allow",
      "Action": "lambda:InvokeFunction",
      "Resource": "arn:aws:lambda:us-east-1:123456789012:function:my-fn"
    }
  ]
}
```

```json
{
  "Version": "2012-10-17",
  "Statement": [
    {
      "Effect": "Allow",
      "Action": "states:StartExecution",
      "Resource": "arn:aws:states:us-east-1:123456789012:stateMachine:my-sm"
    }
  ]
}
```

## 5. Install and start the systemd unit

```bash
sudo cp deploy/systemd/surviva-daemon.service /etc/systemd/system/
sudo systemctl daemon-reload
sudo systemctl enable --now surviva-daemon
```

The unit runs as root — CRIU dump/restore need broad kernel privileges
(`CAP_SYS_ADMIN`/`CAP_SYS_PTRACE` at minimum), and this hasn't been
narrowed down to a smaller capability set yet. Worth revisiting later;
documented honestly here rather than glossed over.

Logs go to the journal (systemd's default), not a file — switching to
file-based output (e.g. `StandardOutput=append:/var/log/surviva-daemon.log`
in the unit) is a one-line change if you need to ship logs elsewhere
(CloudWatch Agent, Fluent Bit, etc.).

## 6. Verify

```bash
sudo systemctl status surviva-daemon
sudo journalctl -u surviva-daemon -f
surviva list
```

Try a real job: `surviva run -- sleep 300`, then `surviva pause <job-id>`,
`surviva show <job-id>`, `surviva resume <job-id>`.

## Uninstall

```bash
sudo systemctl disable --now surviva-daemon
sudo rm /etc/systemd/system/surviva-daemon.service
sudo systemctl daemon-reload
sudo rm /usr/local/bin/surviva
sudo rm -rf /etc/surviva /var/lib/surviva /var/log/surviva/audit.log
```
