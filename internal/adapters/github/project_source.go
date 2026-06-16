package github

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"
)

const queryProjectBoardItems = `query($projectID: ID!, $statusField: String!, $cursor: String) {
  node(id: $projectID) {
    ... on ProjectV2 {
      items(first: 100, after: $cursor) {
        pageInfo { hasNextPage endCursor }
        nodes {
          content {
            ... on Issue {
              number
              id
              title
              body
              state
              createdAt
              labels(first: 30) { nodes { name } }
              repository { nameWithOwner }
            }
          }
          fieldValueByName(name: $statusField) {
            ... on ProjectV2ItemFieldSingleSelectValue { name }
          }
        }
      }
    }
  }
}`

// projectBoardItemsResponse is the GraphQL response type for queryProjectBoardItems.
type projectBoardItemsResponse struct {
	Node struct {
		Items struct {
			PageInfo struct {
				HasNextPage bool   `json:"hasNextPage"`
				EndCursor   string `json:"endCursor"`
			} `json:"pageInfo"`
			Nodes []projectBoardItemNode `json:"nodes"`
		} `json:"items"`
	} `json:"node"`
}

type projectBoardItemNode struct {
	Content struct {
		Number    int    `json:"number"`
		ID        string `json:"id"`
		Title     string `json:"title"`
		Body      string `json:"body"`
		State     string `json:"state"`
		CreatedAt string `json:"createdAt"`
		Labels    struct {
			Nodes []struct {
				Name string `json:"name"`
			} `json:"nodes"`
		} `json:"labels"`
		Repository struct {
			NameWithOwner string `json:"nameWithOwner"`
		} `json:"repository"`
	} `json:"content"`
	FieldValueByName struct {
		Name string `json:"name"`
	} `json:"fieldValueByName"`
}

// ProjectBoardSource reads issues from a GitHub Projects V2 board column.
// It mirrors ProjectBoardSync (the write path) but reads instead of writes.
type ProjectBoardSource struct {
	client *Client
	config *ProjectBoardConfig
	owner  string
	repo   string

	mu        sync.RWMutex
	projectID string
}

// NewProjectBoardSource returns a ProjectBoardSource for the given config.
func NewProjectBoardSource(client *Client, config *ProjectBoardConfig, owner, repo string) *ProjectBoardSource {
	return &ProjectBoardSource{
		client: client,
		config: config,
		owner:  owner,
		repo:   repo,
	}
}

// FindIssuesFromProject returns all open issues in the given board status column that
// belong to owner/repo. Labels, title, and body are hydrated directly in the GraphQL
// query so the existing label-filter loop in findOldestUnprocessedIssue works unchanged.
// Items from other repositories in the same board are excluded.
// Cursor pagination runs until hasNextPage is false.
func (s *ProjectBoardSource) FindIssuesFromProject(ctx context.Context, statusColumn string) ([]*Issue, error) {
	projectID, err := s.ensureProjectID(ctx)
	if err != nil {
		return nil, err
	}

	statusField := s.config.StatusField
	if statusField == "" {
		statusField = "Status"
	}

	targetRepo := strings.ToLower(s.owner + "/" + s.repo)

	var issues []*Issue
	var cursor interface{} // nil on first page, string on subsequent pages

	for {
		vars := map[string]interface{}{
			"projectID":   projectID,
			"statusField": statusField,
			"cursor":      cursor,
		}

		var resp projectBoardItemsResponse
		if err := s.client.ExecuteGraphQLTolerant(ctx, queryProjectBoardItems, vars, &resp); err != nil {
			var partialErr *PartialGraphQLError
			if errors.As(err, &partialErr) {
				slog.Warn("board page has partial errors, some nodes dropped",
					"dropped_count", len(partialErr.Errors),
					"errors", partialErr.Error())
				// resp already has the good nodes from the partial response — fall through.
			} else {
				return nil, fmt.Errorf("list project board items: %w", err)
			}
		}

		for _, node := range resp.Node.Items.Nodes {
			c := node.Content
			if c.Number == 0 {
				continue // non-Issue item (DraftIssue, PR)
			}
			if strings.ToLower(c.Repository.NameWithOwner) != targetRepo {
				continue // cross-repo filter
			}
			if strings.ToUpper(c.State) != "OPEN" {
				continue // skip closed/merged issues
			}
			if !strings.EqualFold(node.FieldValueByName.Name, statusColumn) {
				continue // wrong status column
			}

			labels := make([]Label, len(c.Labels.Nodes))
			for i, l := range c.Labels.Nodes {
				labels[i] = Label{Name: l.Name}
			}

			var createdAt time.Time
			if c.CreatedAt != "" {
				if t, err := time.Parse(time.RFC3339, c.CreatedAt); err == nil {
					createdAt = t
				}
			}

			issues = append(issues, &Issue{
				NodeID:    c.ID,
				Number:    c.Number,
				Title:     c.Title,
				Body:      c.Body,
				State:     strings.ToLower(c.State),
				Labels:    labels,
				CreatedAt: createdAt,
			})
		}

		if !resp.Node.Items.PageInfo.HasNextPage {
			break
		}
		cursor = resp.Node.Items.PageInfo.EndCursor
	}

	return issues, nil
}

// ensureProjectID lazily resolves and caches the project node ID.
func (s *ProjectBoardSource) ensureProjectID(ctx context.Context) (string, error) {
	s.mu.RLock()
	id := s.projectID
	s.mu.RUnlock()
	if id != "" {
		return id, nil
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.projectID != "" {
		return s.projectID, nil
	}

	id, err := resolveProjectID(ctx, s.client, s.owner, s.config.ProjectNumber)
	if err != nil {
		return "", err
	}
	s.projectID = id
	return id, nil
}
