"""S3-based pattern DB synchronization for AWS bench runs.

Manages the learning DB lifecycle:
- Download seed DB from S3 before each run
- Merge trial-produced DBs back after tasks complete
- Upload updated master DB to S3 for next run

Reuses the merge logic from pilot_agent/agent.py:_merge_patterns().
"""

import fcntl
import logging
import sqlite3
import uuid
from pathlib import Path

import boto3

from config import AWS_REGION, PILOT_DB_S3_KEY, S3_BUCKET, S3_RUNS_PREFIX

logger = logging.getLogger(__name__)


class PatternSync:
    """Sync learning pattern DB between S3 and local."""

    def __init__(
        self,
        s3_bucket: str = S3_BUCKET,
        region: str = AWS_REGION,
    ):
        self.s3 = boto3.client("s3", region_name=region)
        self.s3_bucket = s3_bucket

    def download_seed_db(self, local_path: Path) -> bool:
        """Download the master pattern DB from S3.

        Falls back to local seed DB if S3 copy doesn't exist.
        Returns True if download succeeded.
        """
        local_path.parent.mkdir(parents=True, exist_ok=True)
        try:
            self.s3.download_file(self.s3_bucket, PILOT_DB_S3_KEY, str(local_path))
            logger.info(f"Downloaded pattern DB from s3://{self.s3_bucket}/{PILOT_DB_S3_KEY}")
            return True
        except Exception as e:
            logger.info(f"No S3 pattern DB ({e}), using local seed")
            # Try local seed as fallback
            seed = Path(__file__).parent.parent / "pilot_agent" / "data" / "pilot.db"
            if seed.exists():
                import shutil
                shutil.copy2(seed, local_path)
                return True
            return False

    def upload_master_db(self, local_path: Path) -> None:
        """Upload the merged pattern DB to S3 as the new master."""
        if not local_path.exists():
            logger.warning(f"Pattern DB {local_path} not found, skipping upload")
            return

        self.s3.upload_file(
            str(local_path),
            self.s3_bucket,
            PILOT_DB_S3_KEY,
            ExtraArgs={"ServerSideEncryption": "aws:kms"},
        )
        logger.info(f"Uploaded pattern DB to s3://{self.s3_bucket}/{PILOT_DB_S3_KEY}")

    def collect_trial_dbs(self, run_id: str, results_dir: Path) -> list[Path]:
        """Download all trial pattern DBs from S3 for a given run.

        Returns list of local paths to downloaded DBs.
        """
        prefix = f"{S3_RUNS_PREFIX}/{run_id}/"
        dbs = []

        paginator = self.s3.get_paginator("list_objects_v2")
        for page in paginator.paginate(Bucket=self.s3_bucket, Prefix=prefix):
            for obj in page.get("Contents", []):
                key = obj["Key"]
                if key.endswith("pilot-patterns.db"):
                    rel = key[len(prefix):]
                    local = results_dir / rel
                    local.parent.mkdir(parents=True, exist_ok=True)
                    self.s3.download_file(self.s3_bucket, key, str(local))
                    dbs.append(local)

        logger.info(f"Collected {len(dbs)} trial pattern DBs")
        return dbs

    def merge_all(self, trial_dbs: list[Path], master_db: Path) -> int:
        """Merge all trial DBs into the master DB.

        Returns total number of new + boosted patterns.
        """
        total_merged = 0
        total_boosted = 0

        for db_path in trial_dbs:
            if not db_path.exists():
                continue
            try:
                m, b = self._merge_patterns(db_path, master_db)
                total_merged += m
                total_boosted += b
            except Exception as e:
                logger.warning(f"Failed to merge {db_path}: {e}")

        logger.info(
            f"Pattern merge complete: {total_merged} new, {total_boosted} boosted"
        )
        return total_merged + total_boosted

    def _merge_patterns(self, source_db: Path, target_db: Path) -> tuple[int, int]:
        """Thread-safe merge of patterns from source into target.

        Ported from pilot_agent/agent.py:_merge_patterns().

        Returns (new_count, boosted_count).
        """
        lock_path = target_db.with_suffix(".merge-lock")
        with open(lock_path, "w") as lock:
            fcntl.flock(lock, fcntl.LOCK_EX)
            try:
                src = sqlite3.connect(str(source_db))
                dst = sqlite3.connect(str(target_db))

                # Get existing titles in target
                existing = {}
                for row in dst.execute(
                    "SELECT id, title, confidence FROM cross_patterns"
                ):
                    existing[row[1]] = (row[0], row[2])

                # Get all patterns from source
                src_patterns = src.execute(
                    "SELECT title, pattern_type, description, context, confidence, "
                    "occurrences, is_anti_pattern, scope FROM cross_patterns"
                ).fetchall()

                merged = 0
                boosted = 0
                for title, ptype, desc, ctx, conf, occ, is_anti, scope in src_patterns:
                    if title in existing:
                        old_id, old_conf = existing[title]
                        new_conf = min(0.95, old_conf + 0.05)
                        if new_conf > old_conf:
                            dst.execute(
                                "UPDATE cross_patterns SET confidence = ?, "
                                "occurrences = occurrences + 1 WHERE id = ?",
                                (new_conf, old_id),
                            )
                            boosted += 1
                    else:
                        pattern_id = f"learned-{uuid.uuid4().hex[:12]}"
                        dst.execute(
                            "INSERT INTO cross_patterns "
                            "(id, pattern_type, title, description, context, examples, "
                            "confidence, occurrences, is_anti_pattern, scope) "
                            "VALUES (?, ?, ?, ?, ?, '[]', ?, ?, ?, ?)",
                            (
                                pattern_id,
                                ptype,
                                title,
                                desc,
                                ctx or "",
                                min(conf, 0.6),
                                occ,
                                is_anti,
                                scope or "global",
                            ),
                        )
                        merged += 1

                dst.commit()
                src.close()
                dst.close()

                return merged, boosted

            finally:
                fcntl.flock(lock, fcntl.LOCK_UN)
