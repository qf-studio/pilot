"""EC2 instance pool manager for warm pool ASG instances."""

import logging
import time

import boto3

from config import (
    ASG_NAME,
    ASG_RESUME_TIMEOUT_SEC,
    ASG_SSM_WAIT_SEC,
    AWS_REGION,
)

logger = logging.getLogger(__name__)


class InstancePool:
    """Manage EC2 warm pool instances for bench task execution."""

    def __init__(
        self,
        asg_name: str = ASG_NAME,
        region: str = AWS_REGION,
    ):
        self.asg_name = asg_name
        self.asg = boto3.client("autoscaling", region_name=region)
        self.ssm = boto3.client("ssm", region_name=region)
        self.ec2 = boto3.client("ec2", region_name=region)
        self._instances: dict[str, str] = {}  # instance_id -> status ("idle" | "busy")

    def scale_up(self, desired: int) -> None:
        """Set ASG desired capacity to resume warm pool instances."""
        logger.info(f"Scaling ASG {self.asg_name} to desired={desired}...")
        self.asg.update_auto_scaling_group(
            AutoScalingGroupName=self.asg_name,
            DesiredCapacity=desired,
        )

    def scale_down(self) -> None:
        """Scale ASG to 0, returning instances to warm pool."""
        logger.info(f"Scaling ASG {self.asg_name} to 0...")
        self.asg.update_auto_scaling_group(
            AutoScalingGroupName=self.asg_name,
            DesiredCapacity=0,
        )

    def wait_for_instances(
        self,
        count: int,
        timeout_sec: int = ASG_RESUME_TIMEOUT_SEC,
    ) -> list[str]:
        """Wait for `count` instances to reach InService state.

        Returns list of instance IDs.
        """
        logger.info(f"Waiting for {count} instances to be InService...")
        start = time.time()
        poll_interval = 10

        while time.time() - start < timeout_sec:
            instances = self._get_in_service_instances()
            if len(instances) >= count:
                logger.info(f"{len(instances)} instances InService: {instances}")
                for iid in instances:
                    self._instances[iid] = "idle"
                return instances

            logger.info(
                f"  {len(instances)}/{count} InService "
                f"({int(time.time() - start)}s elapsed)"
            )
            time.sleep(poll_interval)

        available = self._get_in_service_instances()
        if available:
            logger.warning(
                f"Timeout: only {len(available)}/{count} instances ready. Proceeding with available."
            )
            for iid in available:
                self._instances[iid] = "idle"
            return available

        raise TimeoutError(
            f"No instances became InService after {timeout_sec}s"
        )

    def wait_for_ssm(
        self,
        instance_ids: list[str],
        timeout_sec: int = ASG_SSM_WAIT_SEC,
    ) -> list[str]:
        """Wait for SSM agent to come online on instances.

        Returns list of SSM-reachable instance IDs.
        """
        logger.info(f"Waiting for SSM agent on {len(instance_ids)} instances...")
        start = time.time()
        poll_interval = 10
        ready = set()

        while time.time() - start < timeout_sec:
            for iid in instance_ids:
                if iid in ready:
                    continue
                try:
                    result = self.ssm.describe_instance_information(
                        Filters=[{"Key": "InstanceIds", "Values": [iid]}]
                    )
                    info_list = result.get("InstanceInformationList", [])
                    if info_list and info_list[0].get("PingStatus") == "Online":
                        ready.add(iid)
                        logger.info(f"  SSM online: {iid}")
                except Exception as e:
                    logger.debug(f"  SSM check failed for {iid}: {e}")

            if len(ready) >= len(instance_ids):
                return list(ready)

            logger.info(
                f"  SSM: {len(ready)}/{len(instance_ids)} online "
                f"({int(time.time() - start)}s elapsed)"
            )
            time.sleep(poll_interval)

        ready_list = list(ready)
        if ready_list:
            logger.warning(
                f"SSM timeout: {len(ready_list)}/{len(instance_ids)} online. "
                f"Proceeding with available."
            )
        else:
            raise TimeoutError(
                f"No SSM agents came online after {timeout_sec}s"
            )
        return ready_list

    def acquire_instance(self) -> str | None:
        """Get an idle instance from the pool. Returns instance ID or None."""
        for iid, status in self._instances.items():
            if status == "idle":
                self._instances[iid] = "busy"
                return iid
        return None

    def trim_to_ready(self, ready_ids: list[str]) -> list[str]:
        """Remove instances from pool that are not in the ready set.

        Call after wait_for_ssm() to evict instances whose SSM agent
        never came online, preventing dispatch to unreachable hosts.

        Returns list of removed instance IDs.
        """
        registered = set(self._instances.keys())
        ready_set = set(ready_ids)
        removed = []
        for iid in registered - ready_set:
            del self._instances[iid]
            removed.append(iid)
        if removed:
            logger.warning(
                f"Trimmed {len(removed)} non-SSM-ready instances from pool: {removed}"
            )
        return removed

    def check_health(self) -> list[str]:
        """Check that busy instances are still running.

        Returns list of dead instance IDs that were removed from the pool.
        """
        busy = [iid for iid, state in self._instances.items() if state == "busy"]
        if not busy:
            return []

        dead: list[str] = []
        try:
            response = self.ec2.describe_instances(InstanceIds=busy)
            for reservation in response.get("Reservations", []):
                for instance in reservation.get("Instances", []):
                    state = instance.get("State", {}).get("Name", "")
                    if state not in ("running", "pending"):
                        dead.append(instance["InstanceId"])
        except Exception as e:
            logger.error(f"Health check failed: {e}")
            return []

        for iid in dead:
            self._instances.pop(iid, None)
            logger.warning(f"Dead instance removed from pool: {iid}")

        return dead

    def release_instance(self, instance_id: str) -> None:
        """Mark an instance as idle (available for new tasks)."""
        if instance_id in self._instances:
            self._instances[instance_id] = "idle"
        else:
            logger.warning(
                f"release_instance called for unknown instance {instance_id}"
            )

    def get_idle_count(self) -> int:
        return sum(1 for s in self._instances.values() if s == "idle")

    def get_busy_count(self) -> int:
        return sum(1 for s in self._instances.values() if s == "busy")

    def get_all_instances(self) -> list[str]:
        return list(self._instances.keys())

    def _get_in_service_instances(self) -> list[str]:
        """Query ASG for InService instance IDs."""
        response = self.asg.describe_auto_scaling_groups(
            AutoScalingGroupNames=[self.asg_name]
        )
        groups = response.get("AutoScalingGroups", [])
        if not groups:
            return []

        return [
            inst["InstanceId"]
            for inst in groups[0].get("Instances", [])
            if inst.get("LifecycleState") == "InService"
        ]
