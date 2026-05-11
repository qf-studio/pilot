---
name: Bench Pause & Resume
description: GLM-5.1 leaderboard v2 run paused at 277/445 trials (79.2%, 55 tasks complete). 34 tasks remaining, resume command ready.
type: project
originSessionId: b433b0c9-d9a5-4db4-96c5-b39fefd557b4
---
GLM-5.1 leaderboard v2 run paused 2026-04-16 at 12:50 UTC.

**Status:** 277/445 trials (62.2%), 79.2% score (42/53 resolved), CI [66.5%, 88.0%]
**Reason:** Z.AI MAX subscription at 90% token cap — paused to avoid hitting limit.
**ASG:** `pilot-agent-pool` scaled to 0 (eu-central-1, profile: quantflow). Instance i-06526cb6854accc36 stopped.
**Orchestrator:** PID 34025 killed (SIGTERM). Log at `/tmp/bench-glm-leaderboard-v2.log`.

**Why:** SIGINT was swallowed by Python's poll loop. SIGTERM killed process but bypassed `finally` block — had to manually scale ASG to 0.

**How to apply:** When Z.AI subscription resets, resume with the command below. Results in S3 are untouched.

**Resume command:**
```bash
AWS_PROFILE=quantflow python3 pilot-bench/aws/orchestrator.py \
  --run-id glm-leaderboard-v2-resume \
  --tasks "overfull-hbox,password-recovery,path-tracing,path-tracing-reverse,polyglot-c-py,polyglot-rust-c,portfolio-optimization,protein-assembly,prove-plus-comm,pypi-server,pytorch-model-cli,pytorch-model-recovery,qemu-alpine-ssh,qemu-startup,query-optimize,raman-fitting,regex-chess,regex-log,reshard-c4-data,rstan-to-pystan,sam-cell-seg,sanitize-git-repo,schemelike-metacircular-eval,sparql-university,sqlite-db-truncate,sqlite-with-gcov,torch-pipeline-parallelism,torch-tensor-parallelism,train-fasttext,tune-mjcf,video-processing,vulnerable-secret,winning-avg-corewars,write-compressor" \
  --k-trials 5 --max-parallel 1 --model glm-5.1
```

**Remaining:** 168 trials across 34 tasks (33 never started + overfull-hbox partial).
**ETA on resume:** ~28h at 6 trials/hr.
