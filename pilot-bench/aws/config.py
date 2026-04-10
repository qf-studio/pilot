"""AWS Bench Runner configuration constants."""

# AWS
AWS_REGION = "eu-central-1"
AWS_PROFILE = "quantflow"

# S3
S3_BUCKET = "pilot-s3-agent-data"
S3_BENCH_PREFIX = "bench"
S3_RUNS_PREFIX = f"{S3_BENCH_PREFIX}/runs"
S3_ASSETS_PREFIX = f"{S3_BENCH_PREFIX}/assets"

# ASG / EC2
ASG_NAME = "pilot-agent-pool"
ASG_RESUME_TIMEOUT_SEC = 300  # 5 min to wait for InService
ASG_SSM_WAIT_SEC = 240  # 4 min for SSM agent to come online

# SSM Parameter Store
SSM_ANTHROPIC_API_KEY = "/pilot/ANTHROPIC_API_KEY"
SSM_GITHUB_TOKEN = "/pilot/GITHUB_TOKEN"

# SSM RunCommand
SSM_COMMAND_TIMEOUT_SEC = 7200  # 2 hours — tasks take 12-90 min
SSM_POLL_INTERVAL_SEC = 15

# Terminal Bench 2.0
TB_GIT_URL = "https://github.com/laude-institute/terminal-bench-2.git"
TB_GIT_REF = "main"  # Pin to specific commit after validation
TB_DOCKER_TAG = "20251031"  # Default Docker image tag for tasks

# Pilot
PILOT_BINARY_S3_KEY = f"{S3_ASSETS_PREFIX}/pilot-linux-amd64.gz"
PILOT_DB_S3_KEY = f"{S3_ASSETS_PREFIX}/pilot.db"
PILOT_CONFIG_S3_KEY = f"{S3_ASSETS_PREFIX}/pilot-config.yaml"
TASK_RUNNER_S3_KEY = f"{S3_ASSETS_PREFIX}/run-bench-task.sh"
TASK_MANIFEST_S3_KEY = f"{S3_ASSETS_PREFIX}/tasks-manifest.json"

# Execution defaults
DEFAULT_MODEL = "claude-opus-4-6"
DEFAULT_K_TRIALS = 1
DEFAULT_MAX_PARALLEL = 5
MAIN_TIMEOUT_SEC = 5400  # 90 min — matches Harbor PilotAgent

# Instance requirements (from Terminal Bench docs)
# 82 tasks: 1 vCPU/2GB, 15 tasks: 4GB, 2 tasks: 8GB
RECOMMENDED_INSTANCE_TYPE = "t3.xlarge"  # 4 vCPU, 16GB
RECOMMENDED_EBS_SIZE_GB = 40
