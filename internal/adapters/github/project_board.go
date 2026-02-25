package github

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
)

// GraphQL queries for GitHub Projects V2.
const (
	queryProjectByOrg = `query($owner: String!, $number: Int!) {
  organization(login: $owner) { projectV2(number: $number) { id } }
}`

	queryProjectByUser = `query($owner: String!, $number: Int!) {
  user(login: $owner) { projectV2(number: $number) { id } }
}`

	queryFieldAndOptions = `query($projectID: ID!, $fieldName: String!) {
  node(id: $projectID) {
    ... on ProjectV2 {
      field(name: $fieldName) {
        ... on ProjectV2SingleSelectField { id options { id name } }
      }
    }
  }
}`

	queryIssueProjectItems = `query($issueID: ID!) {
  node(id: $issueID) {
    ... on Issue { projectItems(first: 20) { nodes { id project { id } } } }
  }
}`

	mutationSetItemFieldValue = `mutation($projectID: ID!, $itemID: ID!, $fieldID: ID!, $optionID: String!) {
  updateProjectV2ItemFieldValue(input: {
    projectId: $projectID, itemId: $itemID, fieldId: $fieldID,
    value: { singleSelectOptionId: $optionID }
  }) { projectV2Item { id } }
}`
)

// Response types for GraphQL unmarshalling.
type (
	projectByOrgResponse struct {
		Organization struct {
			ProjectV2 struct {
				ID string `json:"id"`
			} `json:"projectV2"`
		} `json:"organization"`
	}

	projectByUserResponse struct {
		User struct {
			ProjectV2 struct {
				ID string `json:"id"`
			} `json:"projectV2"`
		} `json:"user"`
	}

	fieldAndOptionsResponse struct {
		Node struct {
			Field struct {
				ID      string `json:"id"`
				Options []struct {
					ID   string `json:"id"`
					Name string `json:"name"`
				} `json:"options"`
			} `json:"field"`
		} `json:"node"`
	}

	issueProjectItemsResponse struct {
		Node struct {
			ProjectItems struct {
				Nodes []struct {
					ID      string `json:"id"`
					Project struct {
						ID string `json:"id"`
					} `json:"project"`
				} `json:"nodes"`
			} `json:"projectItems"`
		} `json:"node"`
	}
)

// ProjectBoardSync manages GitHub Projects V2 board status updates.
// It lazily resolves project/field/option IDs via GraphQL and caches them.
type ProjectBoardSync struct {
	client *Client
	config *ProjectBoardConfig
	owner  string

	mu        sync.RWMutex
	projectID string
	fieldID   string
	optionIDs map[string]string // lowercase status name -> option node ID
}

// NewProjectBoardSync returns a ProjectBoardSync instance, or nil if config is nil or disabled.
func NewProjectBoardSync(client *Client, config *ProjectBoardConfig, owner string) *ProjectBoardSync {
	if config == nil || !config.Enabled {
		return nil
	}
	return &ProjectBoardSync{
		client: client,
		config: config,
		owner:  owner,
	}
}

// UpdateProjectItemStatus moves the issue's project card to the named status column.
// Returns nil (not error) if: config disabled, issue not in project, or status not mapped.
// Returns error only for unexpected API failures.
func (p *ProjectBoardSync) UpdateProjectItemStatus(ctx context.Context, issueNodeID string, statusName string) error {
	if statusName == "" {
		return nil
	}

	if err := p.ensureResolved(ctx); err != nil {
		return fmt.Errorf("resolve project board IDs: %w", err)
	}

	optionID, ok := p.optionIDs[strings.ToLower(statusName)]
	if !ok {
		slog.Warn("project board status not found in options", "status", statusName)
		return nil
	}

	itemID, err := p.getIssueProjectItemID(ctx, issueNodeID)
	if err != nil {
		return fmt.Errorf("get issue project item: %w", err)
	}
	if itemID == "" {
		slog.Warn("issue not found in project board", "issue_node_id", issueNodeID, "project_id", p.projectID)
		return nil
	}

	if err := p.setItemFieldValue(ctx, itemID, optionID); err != nil {
		return fmt.Errorf("set project item field value: %w", err)
	}

	return nil
}

// ensureResolved lazy-loads project/field/option IDs with a read-through cache.
func (p *ProjectBoardSync) ensureResolved(ctx context.Context) error {
	p.mu.RLock()
	resolved := p.projectID != "" && p.fieldID != "" && p.optionIDs != nil
	p.mu.RUnlock()

	if resolved {
		return nil
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	// Double-check after acquiring write lock.
	if p.projectID != "" && p.fieldID != "" && p.optionIDs != nil {
		return nil
	}

	projectID, err := p.resolveProjectID(ctx)
	if err != nil {
		return err
	}
	p.projectID = projectID

	fieldID, optionIDs, err := p.resolveFieldAndOptions(ctx)
	if err != nil {
		return err
	}
	p.fieldID = fieldID
	p.optionIDs = optionIDs

	return nil
}

// resolveProjectID queries for the project ID, trying organization first then user.
func (p *ProjectBoardSync) resolveProjectID(ctx context.Context) (string, error) {
	vars := map[string]interface{}{
		"owner":  p.owner,
		"number": p.config.ProjectNumber,
	}

	// Try organization first.
	var orgResp projectByOrgResponse
	err := p.client.ExecuteGraphQL(ctx, queryProjectByOrg, vars, &orgResp)
	if err == nil && orgResp.Organization.ProjectV2.ID != "" {
		return orgResp.Organization.ProjectV2.ID, nil
	}

	// Fallback to user.
	var userResp projectByUserResponse
	err = p.client.ExecuteGraphQL(ctx, queryProjectByUser, vars, &userResp)
	if err != nil {
		return "", fmt.Errorf("resolve project ID for %s #%d: %w", p.owner, p.config.ProjectNumber, err)
	}
	if userResp.User.ProjectV2.ID == "" {
		return "", fmt.Errorf("project #%d not found for owner %s", p.config.ProjectNumber, p.owner)
	}

	return userResp.User.ProjectV2.ID, nil
}

// resolveFieldAndOptions fetches the Status field ID and all option name→ID mappings.
func (p *ProjectBoardSync) resolveFieldAndOptions(ctx context.Context) (string, map[string]string, error) {
	fieldName := p.config.StatusField
	if fieldName == "" {
		fieldName = "Status"
	}

	vars := map[string]interface{}{
		"projectID": p.projectID,
		"fieldName": fieldName,
	}

	var resp fieldAndOptionsResponse
	if err := p.client.ExecuteGraphQL(ctx, queryFieldAndOptions, vars, &resp); err != nil {
		return "", nil, fmt.Errorf("resolve field %q: %w", fieldName, err)
	}

	if resp.Node.Field.ID == "" {
		return "", nil, fmt.Errorf("field %q not found in project", fieldName)
	}

	optionIDs := make(map[string]string, len(resp.Node.Field.Options))
	for _, opt := range resp.Node.Field.Options {
		optionIDs[strings.ToLower(opt.Name)] = opt.ID
	}

	return resp.Node.Field.ID, optionIDs, nil
}

// getIssueProjectItemID finds the project item ID for the given issue in this project.
// Returns "" if the issue is not in the project.
func (p *ProjectBoardSync) getIssueProjectItemID(ctx context.Context, issueNodeID string) (string, error) {
	vars := map[string]interface{}{
		"issueID": issueNodeID,
	}

	var resp issueProjectItemsResponse
	if err := p.client.ExecuteGraphQL(ctx, queryIssueProjectItems, vars, &resp); err != nil {
		return "", fmt.Errorf("query issue project items: %w", err)
	}

	for _, item := range resp.Node.ProjectItems.Nodes {
		if item.Project.ID == p.projectID {
			return item.ID, nil
		}
	}

	return "", nil
}

// setItemFieldValue calls the updateProjectV2ItemFieldValue mutation.
func (p *ProjectBoardSync) setItemFieldValue(ctx context.Context, itemID string, optionID string) error {
	vars := map[string]interface{}{
		"projectID": p.projectID,
		"itemID":    itemID,
		"fieldID":   p.fieldID,
		"optionID":  optionID,
	}

	return p.client.ExecuteGraphQL(ctx, mutationSetItemFieldValue, vars, nil)
}
