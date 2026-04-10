# AWS Sandbox Infrastructure for Pilot

## Architecture

```
Pilot Orchestrator (local or EC2)
  │
  ├── Create ECS Task (per ticket)
  │     └── Fargate container (Firecracker microVM)
  │           ├── Golden AMI / Docker image with everything pre-installed
  │           ├── pilot binary + CC 2.1.74 + Node.js + numpy + uv
  │           └── Task executes → PR created → container dies
  │
  ├── Or: Warm Pool EC2 (for bench)
  │     └── Pre-booted instances, ~5s resume
  │
  └── Results → S3 bucket (pattern DB, logs, artifacts)
```

## CloudFormation Stack

### 1. ECR Repository (Docker Image)

```yaml
AWSTemplateFormatVersion: '2010-09-09'
Description: Pilot Agent Sandbox Infrastructure

Parameters:
  Environment:
    Type: String
    Default: dev
    AllowedValues: [dev, staging, prod]

Resources:
  # Container registry for pre-built agent image
  PilotAgentRepo:
    Type: AWS::ECR::Repository
    Properties:
      RepositoryName: !Sub pilot-agent-${Environment}
      ImageScanningConfiguration:
        ScanOnPush: true
      LifecyclePolicy:
        LifecyclePolicyText: |
          {
            "rules": [
              {
                "rulePriority": 1,
                "selection": {
                  "tagStatus": "untagged",
                  "countType": "sinceImagePushed",
                  "countUnit": "days",
                  "countNumber": 7
                },
                "action": { "type": "expire" }
              }
            ]
          }
```

### 2. ECS Cluster + Task Definition

```yaml
  PilotCluster:
    Type: AWS::ECS::Cluster
    Properties:
      ClusterName: !Sub pilot-${Environment}
      CapacityProviders:
        - FARGATE
        - FARGATE_SPOT  # 70% cheaper for bench runs
      DefaultCapacityProviderStrategy:
        - CapacityProvider: FARGATE_SPOT
          Weight: 1

  # Task execution role (pull images, write logs)
  TaskExecutionRole:
    Type: AWS::IAM::Role
    Properties:
      AssumeRolePolicyDocument:
        Version: '2012-10-17'
        Statement:
          - Effect: Allow
            Principal:
              Service: ecs-tasks.amazonaws.com
            Action: sts:AssumeRole
      ManagedPolicyArns:
        - arn:aws:iam::aws:policy/service-role/AmazonECSTaskExecutionRolePolicy

  # Task role (what the agent can access)
  TaskRole:
    Type: AWS::IAM::Role
    Properties:
      AssumeRolePolicyDocument:
        Version: '2012-10-17'
        Statement:
          - Effect: Allow
            Principal:
              Service: ecs-tasks.amazonaws.com
            Action: sts:AssumeRole
      Policies:
        - PolicyName: PilotAgentPolicy
          PolicyDocument:
            Version: '2012-10-17'
            Statement:
              # Access to pattern DB + artifacts bucket
              - Effect: Allow
                Action:
                  - s3:GetObject
                  - s3:PutObject
                Resource: !Sub arn:aws:s3:::pilot-data-${Environment}/*
              # No other permissions — sandboxed

  PilotTaskDef:
    Type: AWS::ECS::TaskDefinition
    Properties:
      Family: !Sub pilot-agent-${Environment}
      Cpu: '2048'       # 2 vCPU
      Memory: '4096'    # 4 GB (matches Terminal Bench containers)
      NetworkMode: awsvpc
      RequiresCompatibilities:
        - FARGATE
      ExecutionRoleArn: !GetAtt TaskExecutionRole.Arn
      TaskRoleArn: !GetAtt TaskRole.Arn
      ContainerDefinitions:
        - Name: pilot-agent
          Image: !Sub ${AWS::AccountId}.dkr.ecr.${AWS::Region}.amazonaws.com/pilot-agent-${Environment}:latest
          Essential: true
          # Environment injected at runtime by Pilot orchestrator
          Environment:
            - Name: IS_SANDBOX
              Value: '1'
          LogConfiguration:
            LogDriver: awslogs
            Options:
              awslogs-group: !Ref LogGroup
              awslogs-region: !Ref AWS::Region
              awslogs-stream-prefix: pilot
          # No port mappings — agent doesn't serve traffic

  LogGroup:
    Type: AWS::Logs::LogGroup
    Properties:
      LogGroupName: !Sub /ecs/pilot-agent-${Environment}
      RetentionInDays: 14
```

### 3. S3 Bucket for Pattern DB + Artifacts

```yaml
  DataBucket:
    Type: AWS::S3::Bucket
    Properties:
      BucketName: !Sub pilot-data-${Environment}
      VersioningConfiguration:
        Status: Enabled
      LifecycleConfiguration:
        Rules:
          - Id: CleanupOldArtifacts
            Status: Enabled
            ExpirationInDays: 30
            Prefix: artifacts/
          # Pattern DB never expires
```

### 4. VPC + Security (Sandboxing)

```yaml
  SandboxVPC:
    Type: AWS::EC2::VPC
    Properties:
      CidrBlock: 10.0.0.0/16
      EnableDnsSupport: true
      EnableDnsHostnames: true

  PrivateSubnet1:
    Type: AWS::EC2::Subnet
    Properties:
      VpcId: !Ref SandboxVPC
      CidrBlock: 10.0.1.0/24
      AvailabilityZone: !Select [0, !GetAZs '']

  PrivateSubnet2:
    Type: AWS::EC2::Subnet
    Properties:
      VpcId: !Ref SandboxVPC
      CidrBlock: 10.0.2.0/24
      AvailabilityZone: !Select [1, !GetAZs '']

  # NAT Gateway for outbound internet (pip install, API calls)
  NatGateway:
    Type: AWS::EC2::NatGateway
    Properties:
      SubnetId: !Ref PrivateSubnet1
      AllocationId: !GetAtt NatEIP.AllocationId

  NatEIP:
    Type: AWS::EC2::EIP

  # Security group — outbound only, no inbound
  AgentSecurityGroup:
    Type: AWS::EC2::SecurityGroup
    Properties:
      GroupDescription: Pilot agent sandbox - outbound only
      VpcId: !Ref SandboxVPC
      SecurityGroupEgress:
        - IpProtocol: tcp
          FromPort: 443
          ToPort: 443
          CidrIp: 0.0.0.0/0  # HTTPS for API calls
        - IpProtocol: tcp
          FromPort: 80
          ToPort: 80
          CidrIp: 0.0.0.0/0  # HTTP for package installs
      # No ingress rules — fully sandboxed
```

## Dockerfile (Golden Image)

```dockerfile
FROM debian:bookworm-slim

# System deps
RUN apt-get update && apt-get install -y --no-install-recommends \
    bash curl wget git jq gcc g++ make python3 python3-pip \
    && rm -rf /var/lib/apt/lists/*

# Node.js + Claude Code (pinned)
RUN curl -fsSL https://deb.nodesource.com/setup_22.x | bash - \
    && apt-get install -y nodejs \
    && npm install -g @anthropic-ai/claude-code@2.1.74

# uv/uvx
RUN curl -LsSf https://astral.sh/uv/install.sh | sh \
    && ln -sf /root/.local/bin/uv /usr/local/bin/uv \
    && ln -sf /root/.local/bin/uvx /usr/local/bin/uvx

# Python deps
RUN pip install --break-system-packages numpy

# Pilot binary (pre-built, stripped)
COPY pilot-linux-amd64 /usr/local/bin/pilot
RUN chmod +x /usr/local/bin/pilot

# Pattern DB seed
COPY pilot.db /root/.pilot/data/pilot.db

# Working directory
WORKDIR /app
RUN git config --global init.defaultBranch main \
    && git config --global user.email "pilot@quantflow.studio" \
    && git config --global user.name "Pilot"

# Logs directory
RUN mkdir -p /logs/agent
```

## How Pilot Orchestrator Runs Tasks

```python
# In Pilot's executor (Go or Python):

import boto3

ecs = boto3.client('ecs')

def run_task(instruction: str, project_path: str, oauth_token: str):
    response = ecs.run_task(
        cluster='pilot-dev',
        taskDefinition='pilot-agent-dev',
        launchType='FARGATE',
        networkConfiguration={
            'awsvpcConfiguration': {
                'subnets': ['subnet-xxx', 'subnet-yyy'],
                'securityGroups': ['sg-xxx'],
                'assignPublicIp': 'DISABLED',
            }
        },
        overrides={
            'containerOverrides': [{
                'name': 'pilot-agent',
                'command': [
                    'pilot', 'task', instruction,
                    '--local', '--project', '/app',
                    '--result-json', '/logs/agent/pilot-result.json',
                ],
                'environment': [
                    {'name': 'CLAUDE_CODE_OAUTH_TOKEN', 'value': oauth_token},
                ],
            }],
        },
    )
    return response['tasks'][0]['taskArn']
```

## Cost Comparison

| Infra | Per Task (2 vCPU, 4GB, 30 min) | Setup Time | Notes |
|-------|-------------------------------|------------|-------|
| Modal | ~$0.05 | 30s + 5-15 min upload | Binary upload hangs |
| Fargate | ~$0.03 | 30-60s (image cached) | No upload — image pre-built |
| Fargate Spot | ~$0.01 | 30-60s | 70% cheaper, can be interrupted |
| EC2 Warm Pool | ~$0.02 | 5-10s | Fastest, needs management |

**For 445 bench trials:**
- Modal: ~$22 + upload pain
- Fargate Spot: ~$4.50
- EC2 Warm Pool: ~$9 (+ idle cost)

## Technical Requirements (from Terminal Bench 2.0 task specs)

### Resource Distribution (89 tasks analyzed)

| Resource | Distribution | Notes |
|----------|-------------|-------|
| **CPU** | 82 tasks: 1 vCPU, 3 tasks: 2 vCPU, 2 tasks: 4 vCPU | 92% need just 1 core |
| **Memory** | 70 tasks: 2GB, 15 tasks: 4GB, 2 tasks: 8GB | 79% fit in 2GB |
| **Storage** | 87 tasks: 10GB | Universal |
| **GPU** | 0 tasks require GPU | None — all CPU-only |
| **Timeout** | min: 600s, median: 900s, max: 12000s | 15 min to 3.3 hours |

### High-Resource Tasks (17 of 89)

| Task | CPU | Memory | Why |
|------|-----|--------|-----|
| mcmc-sampling-stan | 4 | 8GB | MCMC sampling, parallel chains |
| rstan-to-pystan | 4 | 8GB | Statistical model compilation |
| compile-compcert | 2 | 4GB | CompCert C compiler build |
| install-windows-3.11 | 2 | 4GB | QEMU VM |
| overfull-hbox | 2 | 4GB | LaTeX compilation |
| gpt2-codegolf | 1 | 4GB | Model inference in minimal C |
| sam-cell-seg | 1 | 4GB | SAM model inference |
| torch-*-parallelism | 1 | 4GB | PyTorch distributed |
| train-fasttext | 1 | 4GB | FastText training |

### Fargate Task Size Tiers

```yaml
# Tier 1: Standard (72 tasks, 81%)
StandardTask:
  Cpu: '1024'      # 1 vCPU
  Memory: '2048'   # 2 GB
  # Cost: ~$0.02/task (30 min Spot)

# Tier 2: Medium (15 tasks, 17%)
MediumTask:
  Cpu: '2048'      # 2 vCPU
  Memory: '4096'   # 4 GB
  # Cost: ~$0.04/task (30 min Spot)

# Tier 3: Heavy (2 tasks, 2%)
HeavyTask:
  Cpu: '4096'      # 4 vCPU
  Memory: '8192'   # 8 GB
  # Cost: ~$0.08/task (30 min Spot)
```

### Recommended Default Configuration

For production Pilot (non-bench):

```yaml
# Default container spec — handles 95% of tasks
PilotTaskDef:
  Cpu: '2048'       # 2 vCPU (headroom for CC + agent overhead)
  Memory: '4096'    # 4 GB (covers 98% of tasks)
  Storage: '20'     # 20 GB ephemeral (10GB task + room for deps)
  Timeout: 3600     # 1 hour default (configurable per task)
```

**Why 2 vCPU / 4GB default:** CC itself consumes ~300MB (Node.js) + pilot binary ~20MB. With 2GB containers, agent overhead leaves little for the actual task. 4GB gives comfortable headroom.

### Network Requirements

| Endpoint | Port | Required For |
|----------|------|-------------|
| api.anthropic.com | 443 | Claude API calls (CC or direct) |
| registry.npmjs.org | 443 | CC install (if not pre-baked) |
| pypi.org | 443 | pip install during tasks |
| github.com | 443 | git clone, task repos |
| S3 (same region) | 443 | Pattern DB sync, artifacts |

**No inbound ports needed** — agent is outbound-only.

### Estimated AWS Costs (445 bench trials)

| Config | Per Trial | Total | vs Modal |
|--------|-----------|-------|----------|
| Fargate Standard (1vCPU/2GB) | $0.02 | $8.90 | 60% cheaper |
| Fargate Spot (1vCPU/2GB) | $0.006 | $2.67 | 88% cheaper |
| Fargate Mixed (tiered) | $0.025 | $11.12 | 50% cheaper |
| EC2 Warm Pool (t3.medium) | $0.015 | $6.68 | 70% cheaper |

**Production cost estimate** (100 tasks/day, mixed complexity):
- Fargate Spot: ~$0.60/day = **$18/month**
- EC2 Warm Pool: ~$1.50/day = **$45/month** (includes idle cost)

## Migration Path

1. Build Dockerfile, push to ECR
2. Deploy CloudFormation stack
3. Add AWS environment plugin to Harbor (or use Pilot's orchestrator directly)
4. Run bench: same `harbor run` command, just `-e aws` instead of `-e modal`
5. Pattern DB synced via S3 instead of file upload/download
