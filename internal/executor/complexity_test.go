package executor

import (
	"testing"
)

func TestDetectComplexity(t *testing.T) {
	tests := []struct {
		name     string
		task     *Task
		expected Complexity
	}{
		// Trivial cases
		{
			name:     "typo fix",
			task:     &Task{Description: "Fix typo in README.md"},
			expected: ComplexityTrivial,
		},
		{
			name:     "add logging",
			task:     &Task{Description: "Add log statement to debug connection"},
			expected: ComplexityTrivial,
		},
		{
			name:     "rename variable",
			task:     &Task{Description: "Rename variable from x to count"},
			expected: ComplexityTrivial,
		},
		{
			name:     "update comment",
			task:     &Task{Description: "Update comment to reflect new behavior"},
			expected: ComplexityTrivial,
		},
		{
			name:     "remove unused import",
			task:     &Task{Description: "Remove unused import statements"},
			expected: ComplexityTrivial,
		},

		// Simple cases
		{
			name:     "add field",
			task:     &Task{Description: "Add field to user struct"},
			expected: ComplexitySimple,
		},
		{
			name:     "add parameter",
			task:     &Task{Description: "Add parameter to function"},
			expected: ComplexitySimple,
		},
		{
			name:     "quick fix",
			task:     &Task{Description: "Quick fix for null check"},
			expected: ComplexitySimple,
		},
		{
			name:     "short description heuristic",
			task:     &Task{Description: "Update the button color"},
			expected: ComplexitySimple,
		},

		// Epic cases
		{
			name:     "epic tag in title",
			task:     &Task{Title: "[epic] Implement new authentication system", Description: "Multi-phase auth rewrite"},
			expected: ComplexityEpic,
		},
		{
			name:     "epic keyword alone is NOT epic",
			task:     &Task{Description: "This is an epic task that spans multiple sprints"},
			expected: ComplexitySimple,
		},
		{
			name:     "roadmap keyword alone is NOT epic",
			task:     &Task{Description: "Implement the roadmap for Q2 features"},
			expected: ComplexitySimple,
		},
		{
			name:     "multi-phase keyword alone is NOT epic",
			task:     &Task{Description: "This is a multi-phase implementation"},
			expected: ComplexitySimple,
		},
		{
			name:     "milestone keyword alone is NOT epic",
			task:     &Task{Description: "Complete milestone 3 with all features"},
			expected: ComplexitySimple,
		},
		{
			name: "epic keyword with structural signals IS epic",
			task: &Task{Description: `This epic feature requires a full implementation plan.
We need to handle multiple components across the codebase with careful coordination.
The implementation spans several areas and requires thorough testing and validation.
Each component needs proper error handling, logging, and documentation updates.
- [ ] Setup infrastructure
- [ ] Core implementation
- [ ] Integration tests`},
			expected: ComplexityEpic,
		},
		{
			name: "5+ checkboxes alone is NOT epic",
			task: &Task{Description: `Implement user management:
- [ ] Create user model
- [ ] Add user API endpoints
- [ ] Implement user validation
- [ ] Add user permissions
- [ ] Create user tests
- [ ] Add user documentation`},
			expected: ComplexityMedium,
		},
		{
			name: "5+ checkboxes with 200+ words IS epic",
			task: &Task{Description: `## Overview
This comprehensive feature requires implementing a full user management system with proper authentication,
authorization, and data validation. The system must integrate with our existing auth provider and support
role-based access control across all API endpoints. We need to ensure backward compatibility with the
current user model while adding new fields and capabilities. The implementation spans multiple layers
of the application including the database schema, API layer, business logic, and frontend components.
Each component must be properly tested with both unit and integration tests to ensure reliability.
The migration path from the old system must be carefully planned to avoid any downtime or data loss.
Performance testing is required to validate that the new system handles our expected load of concurrent users.
Additionally we need to set up proper monitoring and alerting for the new components to ensure we catch
any issues early. The deployment strategy should include feature flags to allow gradual rollout and quick
rollback if needed. Security review is mandatory before the feature goes live in production environments.

- [ ] Create user model with proper validations
- [ ] Add user API endpoints with auth middleware
- [ ] Implement role-based permission system
- [ ] Add comprehensive test coverage
- [ ] Create user documentation and API docs
- [ ] Set up monitoring and alerting`},
			expected: ComplexityEpic,
		},
		{
			name: "3+ numbered phases",
			task: &Task{Description: `Implementation plan:
Phase 1: Design the database schema
Phase 2: Implement the API layer
Phase 3: Create the frontend components
Phase 4: Add integration tests`},
			expected: ComplexityEpic,
		},
		{
			name: "phase keyword sections",
			task: &Task{Description: `Implementation:
Phase 1: Setup
Phase 2: Core logic
Phase 3: Testing`},
			expected: ComplexityEpic,
		},
		{
			name: "200+ words with structural markers",
			task: &Task{Description: `## Overview
This is a comprehensive implementation that requires significant planning and coordination across multiple teams.
The feature spans multiple components and requires careful consideration of the existing architecture patterns.
We need to ensure backward compatibility while introducing the new functionality in a phased approach.
The project involves frontend, backend, database, and infrastructure changes that must be carefully orchestrated.
Each phase builds on the previous one and has its own set of deliverables and acceptance criteria to meet.

## Phase 1: Foundation
Set up the basic infrastructure and data models needed for the feature implementation.
This includes database migrations for new tables and columns, API scaffolding with proper versioning,
and initial frontend components with placeholder data. We also need to configure the CI/CD pipeline
to support the new deployment requirements and set up monitoring dashboards for the new services.

## Phase 2: Core Implementation
Build out the main business logic and user-facing features with full functionality.
This is the bulk of the work and requires coordination across teams including frontend, backend, and QA.
We need to implement the core algorithms, integrate with external services, handle edge cases,
and ensure the system performs well under expected load. Documentation should be written in parallel.

## Phase 3: Polish and Testing
Add comprehensive tests including unit tests, integration tests, and end-to-end tests.
Fix edge cases discovered during testing and polish the user experience based on feedback.
This phase ensures production readiness and documentation completeness before the final release.
We need to conduct load testing, security review, and accessibility audit before going live.

The implementation should follow our established patterns and coding standards for consistency.
We need to coordinate with the design team for the UI components and the platform team for infrastructure.
Performance testing will be critical given the expected load on this feature during peak hours.
We should also plan for graceful degradation and proper error handling throughout the system.
Regular sync meetings with stakeholders will be necessary to ensure alignment on priorities and timeline.`},
			expected: ComplexityEpic,
		},

		// False positive prevention - file paths and code blocks
		{
			name:     "file path with epic should not trigger",
			task:     &Task{Title: "Add method to epic.go", Description: "Add CreateSubIssues method to internal/executor/epic.go"},
			expected: ComplexitySimple,
		},
		{
			name:     "code block with EpicPlan should not trigger",
			task:     &Task{Description: "Add this code:\n```go\nfunc (r *Runner) Method(plan *EpicPlan) error {\n    return nil\n}\n```"},
			expected: ComplexitySimple,
		},
		{
			name:     "identifier PlanEpic should not trigger",
			task:     &Task{Description: "Call the `PlanEpic` method after detection"},
			expected: ComplexitySimple,
		},
		{
			name:     "epic keyword alone no longer triggers",
			task:     &Task{Description: "This is an epic task spanning multiple sprints"},
			expected: ComplexitySimple,
		},
		{
			name:     "epic in prose without signals is not epic",
			task:     &Task{Description: "This epic feature requires changes to epic.go"},
			expected: ComplexitySimple,
		},

		// Complex cases
		{
			name:     "refactor",
			task:     &Task{Description: "Refactor the authentication system"},
			expected: ComplexityComplex,
		},
		{
			name:     "migration",
			task:     &Task{Description: "Database migration for new schema"},
			expected: ComplexityComplex,
		},
		{
			name:     "architecture",
			task:     &Task{Description: "Update system architecture for microservices"},
			expected: ComplexityComplex,
		},
		{
			name:     "rewrite",
			task:     &Task{Description: "Rewrite the parser from scratch"},
			expected: ComplexityComplex,
		},

		// Medium cases (default)
		{
			name:     "medium length description",
			task:     &Task{Description: "Implement a new endpoint that fetches user data from the database and returns it formatted as JSON with proper error handling"},
			expected: ComplexityMedium,
		},
		{
			name:     "feature without keywords",
			task:     &Task{Description: "Create new component for displaying charts with proper styling and responsive design"},
			expected: ComplexityMedium,
		},

		// Edge cases
		{
			name:     "nil task",
			task:     nil,
			expected: ComplexityMedium,
		},
		{
			name:     "empty description",
			task:     &Task{Description: ""},
			expected: ComplexitySimple,
		},
		{
			name:     "title contains pattern",
			task:     &Task{Title: "Fix typo", Description: "Some description"},
			expected: ComplexityTrivial,
		},

		// Long description triggers complex
		{
			name: "very long description",
			task: &Task{Description: `This task requires implementing a comprehensive solution that spans multiple files and components.
				We need to update the data layer, add new API endpoints, modify the frontend components, update tests,
				and ensure backward compatibility. The implementation should follow our coding standards and include
				proper documentation. We also need to consider performance implications and add appropriate caching
				where necessary. The feature should support both authenticated and anonymous users with different
				permission levels.`},
			expected: ComplexityComplex,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := DetectComplexity(tt.task)
			if got != tt.expected {
				t.Errorf("DetectComplexity() = %v, want %v", got, tt.expected)
			}
		})
	}
}

func TestComplexity_Methods(t *testing.T) {
	tests := []struct {
		complexity          Complexity
		isTrivial           bool
		isSimple            bool
		isEpic              bool
		shouldSkipNavigator bool
		shouldRunResearch   bool
	}{
		{ComplexityTrivial, true, true, false, true, false},
		{ComplexitySimple, false, true, false, false, false},
		{ComplexityMedium, false, false, false, false, true},
		{ComplexityComplex, false, false, false, false, true},
		{ComplexityEpic, false, false, true, false, false},
	}

	for _, tt := range tests {
		t.Run(string(tt.complexity), func(t *testing.T) {
			if got := tt.complexity.IsTrivial(); got != tt.isTrivial {
				t.Errorf("IsTrivial() = %v, want %v", got, tt.isTrivial)
			}
			if got := tt.complexity.IsSimple(); got != tt.isSimple {
				t.Errorf("IsSimple() = %v, want %v", got, tt.isSimple)
			}
			if got := tt.complexity.IsEpic(); got != tt.isEpic {
				t.Errorf("IsEpic() = %v, want %v", got, tt.isEpic)
			}
			if got := tt.complexity.ShouldSkipNavigator(); got != tt.shouldSkipNavigator {
				t.Errorf("ShouldSkipNavigator() = %v, want %v", got, tt.shouldSkipNavigator)
			}
			if got := tt.complexity.ShouldRunResearch(); got != tt.shouldRunResearch {
				t.Errorf("ShouldRunResearch() = %v, want %v", got, tt.shouldRunResearch)
			}
		})
	}
}

func TestComplexity_String(t *testing.T) {
	tests := []struct {
		complexity Complexity
		expected   string
	}{
		{ComplexityTrivial, "trivial"},
		{ComplexitySimple, "simple"},
		{ComplexityMedium, "medium"},
		{ComplexityComplex, "complex"},
		{ComplexityEpic, "epic"},
	}

	for _, tt := range tests {
		t.Run(tt.expected, func(t *testing.T) {
			if got := tt.complexity.String(); got != tt.expected {
				t.Errorf("String() = %v, want %v", got, tt.expected)
			}
		})
	}
}
