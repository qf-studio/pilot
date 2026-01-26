package oauth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
)

var (
	ErrNotFound      = errors.New("integration not found")
	ErrAlreadyExists = errors.New("integration already exists")
)

// Store provides OAuth integration storage
type Store struct {
	pool  *pgxpool.Pool
	redis *redis.Client
}

// NewStore creates a new OAuth store
func NewStore(pool *pgxpool.Pool, redis *redis.Client) *Store {
	return &Store{pool: pool, redis: redis}
}

// --- Integration Operations ---

// SaveIntegration saves or updates an integration
func (s *Store) SaveIntegration(ctx context.Context, integration *Integration) error {
	providerData, _ := json.Marshal(integration.ProviderData)

	_, err := s.pool.Exec(ctx, `
		INSERT INTO integrations (id, org_id, provider, access_token, refresh_token, token_expires_at, provider_user_id, provider_data, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
		ON CONFLICT (org_id, provider) DO UPDATE SET
			access_token = EXCLUDED.access_token,
			refresh_token = EXCLUDED.refresh_token,
			token_expires_at = EXCLUDED.token_expires_at,
			provider_user_id = EXCLUDED.provider_user_id,
			provider_data = EXCLUDED.provider_data,
			updated_at = NOW()
	`, integration.ID, integration.OrgID, integration.Provider, integration.AccessToken,
		integration.RefreshToken, integration.TokenExpiresAt, integration.ProviderUserID,
		providerData, integration.CreatedAt, integration.UpdatedAt)

	if err != nil {
		return fmt.Errorf("failed to save integration: %w", err)
	}
	return nil
}

// GetIntegration retrieves an integration by org and provider
func (s *Store) GetIntegration(ctx context.Context, orgID uuid.UUID, provider Provider) (*Integration, error) {
	row := s.pool.QueryRow(ctx, `
		SELECT id, org_id, provider, access_token, refresh_token, token_expires_at, provider_user_id, provider_data, created_at, updated_at
		FROM integrations WHERE org_id = $1 AND provider = $2
	`, orgID, provider)

	return s.scanIntegration(row)
}

// GetIntegrationByID retrieves an integration by ID
func (s *Store) GetIntegrationByID(ctx context.Context, id uuid.UUID) (*Integration, error) {
	row := s.pool.QueryRow(ctx, `
		SELECT id, org_id, provider, access_token, refresh_token, token_expires_at, provider_user_id, provider_data, created_at, updated_at
		FROM integrations WHERE id = $1
	`, id)

	return s.scanIntegration(row)
}

func (s *Store) scanIntegration(row pgx.Row) (*Integration, error) {
	var i Integration
	var providerDataJSON []byte
	var tokenExpiresAt *time.Time

	err := row.Scan(&i.ID, &i.OrgID, &i.Provider, &i.AccessToken, &i.RefreshToken,
		&tokenExpiresAt, &i.ProviderUserID, &providerDataJSON, &i.CreatedAt, &i.UpdatedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}

	i.TokenExpiresAt = tokenExpiresAt
	if providerDataJSON != nil {
		_ = json.Unmarshal(providerDataJSON, &i.ProviderData)
	}

	return &i, nil
}

// ListIntegrations returns all integrations for an organization
func (s *Store) ListIntegrations(ctx context.Context, orgID uuid.UUID) ([]*Integration, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, org_id, provider, access_token, refresh_token, token_expires_at, provider_user_id, provider_data, created_at, updated_at
		FROM integrations WHERE org_id = $1
		ORDER BY provider
	`, orgID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var integrations []*Integration
	for rows.Next() {
		var i Integration
		var providerDataJSON []byte
		var tokenExpiresAt *time.Time

		if err := rows.Scan(&i.ID, &i.OrgID, &i.Provider, &i.AccessToken, &i.RefreshToken,
			&tokenExpiresAt, &i.ProviderUserID, &providerDataJSON, &i.CreatedAt, &i.UpdatedAt); err != nil {
			return nil, err
		}

		i.TokenExpiresAt = tokenExpiresAt
		if providerDataJSON != nil {
			_ = json.Unmarshal(providerDataJSON, &i.ProviderData)
		}
		integrations = append(integrations, &i)
	}

	return integrations, nil
}

// DeleteIntegration removes an integration
func (s *Store) DeleteIntegration(ctx context.Context, orgID uuid.UUID, provider Provider) error {
	result, err := s.pool.Exec(ctx, `
		DELETE FROM integrations WHERE org_id = $1 AND provider = $2
	`, orgID, provider)

	if err != nil {
		return fmt.Errorf("failed to delete integration: %w", err)
	}
	if result.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// --- OAuth State Operations (Redis) ---

const stateKeyPrefix = "oauth_state:"
const stateTTL = 10 * time.Minute

// SaveOAuthState saves OAuth state to Redis
func (s *Store) SaveOAuthState(ctx context.Context, state *OAuthState) error {
	data, err := json.Marshal(state)
	if err != nil {
		return fmt.Errorf("failed to marshal state: %w", err)
	}

	key := stateKeyPrefix + state.State
	if err := s.redis.Set(ctx, key, data, stateTTL).Err(); err != nil {
		return fmt.Errorf("failed to save state: %w", err)
	}

	return nil
}

// GetOAuthState retrieves and deletes OAuth state from Redis
func (s *Store) GetOAuthState(ctx context.Context, state string) (*OAuthState, error) {
	key := stateKeyPrefix + state

	// Get and delete atomically
	data, err := s.redis.GetDel(ctx, key).Bytes()
	if err != nil {
		if errors.Is(err, redis.Nil) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("failed to get state: %w", err)
	}

	var oauthState OAuthState
	if err := json.Unmarshal(data, &oauthState); err != nil {
		return nil, fmt.Errorf("failed to unmarshal state: %w", err)
	}

	// Check expiration
	if time.Now().After(oauthState.ExpiresAt) {
		return nil, ErrNotFound
	}

	return &oauthState, nil
}

// UpdateTokens updates tokens for an integration
func (s *Store) UpdateTokens(ctx context.Context, id uuid.UUID, accessToken, refreshToken string, expiresAt *time.Time) error {
	result, err := s.pool.Exec(ctx, `
		UPDATE integrations
		SET access_token = $2, refresh_token = $3, token_expires_at = $4, updated_at = NOW()
		WHERE id = $1
	`, id, accessToken, refreshToken, expiresAt)

	if err != nil {
		return fmt.Errorf("failed to update tokens: %w", err)
	}
	if result.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}
