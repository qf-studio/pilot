package linear

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

const (
	linearAPIURL = "https://api.linear.app/graphql"
)

// Client is a Linear API client
type Client struct {
	apiKey     string
	httpClient *http.Client
}

// NewClient creates a new Linear client
func NewClient(apiKey string) *Client {
	return &Client{
		apiKey: apiKey,
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

// Issue represents a Linear issue
type Issue struct {
	ID          string    `json:"id"`
	Identifier  string    `json:"identifier"`
	Title       string    `json:"title"`
	Description string    `json:"description"`
	Priority    int       `json:"priority"`
	State       State     `json:"state"`
	Labels      []Label   `json:"labels"`
	Assignee    *User     `json:"assignee"`
	Project     *Project  `json:"project"`
	Team        Team      `json:"team"`
	CreatedAt   time.Time `json:"createdAt"`
	UpdatedAt   time.Time `json:"updatedAt"`
}

// State represents an issue state
type State struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Type string `json:"type"`
}

// Label represents a Linear label
type Label struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// User represents a Linear user
type User struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Email string `json:"email"`
}

// Project represents a Linear project
type Project struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// Team represents a Linear team
type Team struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Key  string `json:"key"`
}

// GraphQLRequest represents a GraphQL request
type GraphQLRequest struct {
	Query     string                 `json:"query"`
	Variables map[string]interface{} `json:"variables,omitempty"`
}

// GraphQLResponse represents a GraphQL response
type GraphQLResponse struct {
	Data   json.RawMessage `json:"data"`
	Errors []GraphQLError  `json:"errors,omitempty"`
}

// GraphQLError represents a GraphQL error
type GraphQLError struct {
	Message string `json:"message"`
}

// Execute executes a GraphQL query
func (c *Client) Execute(ctx context.Context, query string, variables map[string]interface{}, result interface{}) error {
	reqBody := GraphQLRequest{
		Query:     query,
		Variables: variables,
	}

	body, err := json.Marshal(reqBody)
	if err != nil {
		return fmt.Errorf("failed to marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, linearAPIURL, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", c.apiKey)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("failed to execute request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("failed to read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("API error: %s", string(respBody))
	}

	var gqlResp GraphQLResponse
	if err := json.Unmarshal(respBody, &gqlResp); err != nil {
		return fmt.Errorf("failed to parse response: %w", err)
	}

	if len(gqlResp.Errors) > 0 {
		return fmt.Errorf("GraphQL error: %s", gqlResp.Errors[0].Message)
	}

	if result != nil {
		if err := json.Unmarshal(gqlResp.Data, result); err != nil {
			return fmt.Errorf("failed to parse data: %w", err)
		}
	}

	return nil
}

// GetIssue fetches an issue by ID
func (c *Client) GetIssue(ctx context.Context, id string) (*Issue, error) {
	query := `
		query GetIssue($id: String!) {
			issue(id: $id) {
				id
				identifier
				title
				description
				priority
				state {
					id
					name
					type
				}
				labels {
					nodes {
						id
						name
					}
				}
				assignee {
					id
					name
					email
				}
				project {
					id
					name
				}
				team {
					id
					name
					key
				}
				createdAt
				updatedAt
			}
		}
	`

	var result struct {
		Issue Issue `json:"issue"`
	}

	if err := c.Execute(ctx, query, map[string]interface{}{"id": id}, &result); err != nil {
		return nil, err
	}

	return &result.Issue, nil
}

// UpdateIssueState updates an issue's state
func (c *Client) UpdateIssueState(ctx context.Context, issueID, stateID string) error {
	mutation := `
		mutation UpdateIssue($id: String!, $stateId: String!) {
			issueUpdate(id: $id, input: { stateId: $stateId }) {
				success
			}
		}
	`

	return c.Execute(ctx, mutation, map[string]interface{}{
		"id":      issueID,
		"stateId": stateID,
	}, nil)
}

// AddComment adds a comment to an issue
func (c *Client) AddComment(ctx context.Context, issueID, body string) error {
	mutation := `
		mutation CreateComment($issueId: String!, $body: String!) {
			commentCreate(input: { issueId: $issueId, body: $body }) {
				success
			}
		}
	`

	return c.Execute(ctx, mutation, map[string]interface{}{
		"issueId": issueID,
		"body":    body,
	}, nil)
}
