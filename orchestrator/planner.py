"""
Task Planner

Converts tickets from Linear/Jira into Navigator task documents.
"""

import json
from dataclasses import dataclass
from typing import Optional, List, Dict, Any
from datetime import datetime


@dataclass
class Ticket:
    """Represents a ticket from a project management tool."""
    id: str
    identifier: str
    title: str
    description: str
    priority: int
    labels: List[str]
    project: Optional[str] = None
    assignee: Optional[str] = None
    created_at: Optional[datetime] = None


@dataclass
class Task:
    """Represents a Navigator task document."""
    id: str
    title: str
    description: str
    requirements: List[str]
    acceptance_criteria: List[str]
    technical_approach: str
    files_to_modify: List[str]
    estimated_complexity: str  # low, medium, high
    priority: int


class TaskPlanner:
    """Converts tickets into structured task documents."""

    def __init__(self, model: str = "claude-opus-4-6"):
        self.model = model

    def plan_from_ticket(self, ticket: Ticket) -> Task:
        """
        Convert a ticket into a structured task.

        In production, this would call Claude to analyze the ticket
        and generate a detailed implementation plan.
        """
        # Extract requirements from description
        requirements = self._extract_requirements(ticket.description)

        # Generate acceptance criteria
        acceptance_criteria = self._generate_acceptance_criteria(ticket)

        # Estimate complexity
        complexity = self._estimate_complexity(ticket)

        return Task(
            id=f"TASK-{ticket.identifier}",
            title=ticket.title,
            description=ticket.description,
            requirements=requirements,
            acceptance_criteria=acceptance_criteria,
            technical_approach="",  # Would be LLM-generated
            files_to_modify=[],     # Would be determined by codebase analysis
            estimated_complexity=complexity,
            priority=ticket.priority
        )

    def _extract_requirements(self, description: str) -> List[str]:
        """Extract requirements from ticket description."""
        requirements = []
        lines = description.split('\n')

        for line in lines:
            line = line.strip()
            if line.startswith('- ') or line.startswith('* '):
                requirements.append(line[2:])
            elif line.startswith('[ ]') or line.startswith('[x]'):
                requirements.append(line[4:].strip())

        return requirements if requirements else [description[:200]]

    def _generate_acceptance_criteria(self, ticket: Ticket) -> List[str]:
        """Generate acceptance criteria from ticket."""
        criteria = []

        # Basic criteria from title
        criteria.append(f"Feature '{ticket.title}' is implemented")
        criteria.append("All tests pass")
        criteria.append("Code follows project standards")

        return criteria

    def _estimate_complexity(self, ticket: Ticket) -> str:
        """Estimate task complexity."""
        desc_length = len(ticket.description)

        if ticket.priority == 1:  # Urgent
            return "high"
        elif desc_length > 500:
            return "high"
        elif desc_length > 200:
            return "medium"
        else:
            return "low"

    def to_markdown(self, task: Task) -> str:
        """Convert task to Navigator markdown format."""
        requirements = '\n'.join(f"- {r}" for r in task.requirements)
        criteria = '\n'.join(f"- [ ] {c}" for c in task.acceptance_criteria)

        return f"""# {task.id}: {task.title}

## Overview
{task.description}

## Requirements
{requirements}

## Acceptance Criteria
{criteria}

## Technical Approach
{task.technical_approach or "To be determined during implementation."}

## Files to Modify
{', '.join(task.files_to_modify) or "To be determined during implementation."}

## Metadata
- **Priority**: {task.priority}
- **Complexity**: {task.estimated_complexity}
- **Created**: {datetime.now().isoformat()}
"""


def plan_ticket(ticket_data: Dict[str, Any]) -> str:
    """
    Entry point for Go orchestrator.

    Args:
        ticket_data: JSON-serializable ticket data

    Returns:
        Markdown task document
    """
    ticket = Ticket(
        id=ticket_data.get('id', ''),
        identifier=ticket_data.get('identifier', ''),
        title=ticket_data.get('title', ''),
        description=ticket_data.get('description', ''),
        priority=ticket_data.get('priority', 4),
        labels=ticket_data.get('labels', []),
        project=ticket_data.get('project'),
        assignee=ticket_data.get('assignee'),
    )

    planner = TaskPlanner()
    task = planner.plan_from_ticket(ticket)
    return planner.to_markdown(task)


if __name__ == "__main__":
    # Test with sample ticket
    sample_ticket = {
        "id": "123",
        "identifier": "PROJ-123",
        "title": "Add user authentication",
        "description": """Implement user authentication flow.

Requirements:
- Login with email/password
- JWT token management
- Logout functionality
- Password reset flow""",
        "priority": 2,
        "labels": ["pilot", "feature"],
    }

    result = plan_ticket(sample_ticket)
    print(result)
