"""SSM RunCommand wrapper for executing bench tasks on EC2 instances."""

import logging
import time

import boto3

from config import (
    AWS_REGION,
    S3_BUCKET,
    SSM_COMMAND_TIMEOUT_SEC,
    SSM_POLL_INTERVAL_SEC,
)

logger = logging.getLogger(__name__)


class SSMExecutor:
    """Send commands to EC2 instances via SSM and track their completion."""

    def __init__(self, region: str = AWS_REGION):
        self.ssm = boto3.client("ssm", region_name=region)
        self._active_commands: dict[str, dict] = {}  # command_id -> metadata

    def send_task(
        self,
        instance_id: str,
        task_name: str,
        trial_id: str,
        run_id: str,
        *,
        model: str = "claude-opus-4-6",
        timeout_sec: int = SSM_COMMAND_TIMEOUT_SEC,
        s3_bucket: str = S3_BUCKET,
    ) -> str:
        """Send a bench task to an instance via SSM RunCommand.

        Returns the SSM command ID for tracking.
        """
        commands = [
            # Download task runner from S3
            f"aws s3 cp s3://{s3_bucket}/bench/assets/run-bench-task.sh /tmp/run-bench-task.sh --quiet",
            "chmod +x /tmp/run-bench-task.sh",
            # Execute with params
            (
                f"export TASK_NAME='{task_name}' "
                f"TRIAL_ID='{trial_id}' "
                f"RUN_ID='{run_id}' "
                f"S3_BUCKET='{s3_bucket}' "
                f"MODEL='{model}' "
                f"&& bash /tmp/run-bench-task.sh"
            ),
        ]

        response = self.ssm.send_command(
            InstanceIds=[instance_id],
            DocumentName="AWS-RunShellScript",
            Parameters={
                "commands": commands,
                "executionTimeout": [str(timeout_sec)],
            },
            TimeoutSeconds=timeout_sec + 60,  # Outer timeout slightly longer
            CloudWatchOutputConfig={
                "CloudWatchOutputEnabled": True,
                "CloudWatchLogGroupName": f"/pilot/bench/{run_id}",
            },
            Comment=f"bench: {task_name} trial={trial_id} run={run_id}",
        )

        command_id = response["Command"]["CommandId"]
        self._active_commands[command_id] = {
            "instance_id": instance_id,
            "task_name": task_name,
            "trial_id": trial_id,
            "run_id": run_id,
            "started_at": time.time(),
        }

        logger.info(
            f"Dispatched {task_name}/{trial_id} to {instance_id} (cmd={command_id})"
        )
        return command_id

    def check_command(self, command_id: str) -> dict:
        """Check the status of an SSM command.

        Returns dict with:
            status: "Pending" | "InProgress" | "Success" | "Failed" | "TimedOut" | "Cancelled"
            stdout: str (if completed)
            stderr: str (if completed)
        """
        meta = self._active_commands.get(command_id, {})
        instance_id = meta.get("instance_id", "")

        if not instance_id:
            return {"status": "Unknown", "stdout": "", "stderr": ""}

        try:
            result = self.ssm.get_command_invocation(
                CommandId=command_id,
                InstanceId=instance_id,
            )
            return {
                "status": result["Status"],
                "stdout": result.get("StandardOutputContent", ""),
                "stderr": result.get("StandardErrorContent", ""),
                "status_details": result.get("StatusDetails", ""),
            }
        except self.ssm.exceptions.InvocationDoesNotExist:
            return {"status": "Pending", "stdout": "", "stderr": ""}
        except Exception as e:
            logger.warning(f"Error checking command {command_id}: {e}")
            return {"status": "Error", "stdout": "", "stderr": str(e)}

    def wait_for_command(
        self,
        command_id: str,
        poll_interval: int = SSM_POLL_INTERVAL_SEC,
        timeout_sec: int = SSM_COMMAND_TIMEOUT_SEC,
    ) -> dict:
        """Block until command completes or times out."""
        start = time.time()
        while time.time() - start < timeout_sec:
            result = self.check_command(command_id)
            status = result["status"]

            if status in ("Success", "Failed", "TimedOut", "Cancelled"):
                if command_id in self._active_commands:
                    meta = self._active_commands.pop(command_id)
                    result["task_name"] = meta["task_name"]
                    result["trial_id"] = meta["trial_id"]
                    result["duration_sec"] = time.time() - meta["started_at"]
                return result

            time.sleep(poll_interval)

        return {"status": "TimedOut", "stdout": "", "stderr": "Local timeout reached"}

    def poll_all_active(self) -> list[dict]:
        """Check all active commands, return list of completed ones.

        Completed commands are removed from the active set.
        """
        completed = []
        to_remove = []

        for command_id, meta in self._active_commands.items():
            result = self.check_command(command_id)
            status = result["status"]

            if status in ("Success", "Failed", "TimedOut", "Cancelled"):
                result["command_id"] = command_id
                result["task_name"] = meta["task_name"]
                result["trial_id"] = meta["trial_id"]
                result["instance_id"] = meta["instance_id"]
                result["duration_sec"] = time.time() - meta["started_at"]
                completed.append(result)
                to_remove.append(command_id)

        for cmd_id in to_remove:
            del self._active_commands[cmd_id]

        return completed

    @property
    def active_count(self) -> int:
        return len(self._active_commands)

    def get_active_instances(self) -> set[str]:
        """Return set of instance IDs currently running tasks."""
        return {meta["instance_id"] for meta in self._active_commands.values()}
