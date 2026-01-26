package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"

	"github.com/alekspetrov/pilot/cloud/internal/api"
	"github.com/alekspetrov/pilot/cloud/internal/auth"
	"github.com/alekspetrov/pilot/cloud/internal/billing"
	"github.com/alekspetrov/pilot/cloud/internal/oauth"
	"github.com/alekspetrov/pilot/cloud/internal/sandbox"
	"github.com/alekspetrov/pilot/cloud/internal/tenants"
)

func main() {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Load configuration from environment
	config := loadConfig()

	// Initialize PostgreSQL connection pool
	pool, err := pgxpool.New(ctx, config.DatabaseURL)
	if err != nil {
		log.Fatalf("Failed to connect to database: %v", err)
	}
	defer pool.Close()

	// Verify database connection
	if err := pool.Ping(ctx); err != nil {
		log.Fatalf("Failed to ping database: %v", err)
	}
	log.Println("Connected to PostgreSQL")

	// Initialize Redis client
	redisOpts, err := redis.ParseURL(config.RedisURL)
	if err != nil {
		log.Fatalf("Invalid Redis URL: %v", err)
	}
	redisClient := redis.NewClient(redisOpts)
	defer redisClient.Close()

	if err := redisClient.Ping(ctx).Err(); err != nil {
		log.Fatalf("Failed to connect to Redis: %v", err)
	}
	log.Println("Connected to Redis")

	// Initialize stores
	tenantStore := tenants.NewStore(pool)
	oauthStore := oauth.NewStore(pool, redisClient)
	billingStore := billing.NewStore(pool)
	sandboxStore := sandbox.NewStore(pool)

	// Initialize services
	tenantService := tenants.NewService(tenantStore)

	oauthCredentials := map[oauth.Provider]struct{ ClientID, ClientSecret string }{
		oauth.ProviderGitHub: {
			ClientID:     config.GitHubClientID,
			ClientSecret: config.GitHubClientSecret,
		},
		oauth.ProviderLinear: {
			ClientID:     config.LinearClientID,
			ClientSecret: config.LinearClientSecret,
		},
		oauth.ProviderJira: {
			ClientID:     config.JiraClientID,
			ClientSecret: config.JiraClientSecret,
		},
	}
	oauthService := oauth.NewService(oauthStore, config.BaseURL, oauthCredentials)

	stripePriceIDs := map[string]string{
		"free": "", // No Stripe price for free tier
		"pro":  config.StripePriceProID,
		"team": config.StripePriceTeamID,
	}
	billingService := billing.NewService(billingStore, config.StripeSecretKey, config.StripeWebhookSecret, config.BaseURL, stripePriceIDs)

	executor := sandbox.NewExecutor(
		sandboxStore,
		sandbox.ContainerConfig{
			Image:   config.ExecutorImage,
			Memory:  config.ExecutorMemory,
			CPU:     config.ExecutorCPU,
			Timeout: time.Duration(config.ExecutorTimeoutSec) * time.Second,
		},
		sandbox.DefaultResourceLimits(),
		config.MaxConcurrentExecutions,
	)

	tokenService := auth.NewTokenService(
		config.JWTSecretKey,
		15*time.Minute,  // Access token expiry
		7*24*time.Hour,  // Refresh token expiry
	)

	// Initialize API server
	server := api.NewServer(tenantService, oauthService, billingService, executor, tokenService)

	// Start HTTP server
	httpServer := &http.Server{
		Addr:         fmt.Sprintf(":%d", config.Port),
		Handler:      server.Router(),
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 60 * time.Second,
		IdleTimeout:  120 * time.Second,
	}

	// Start server in goroutine
	go func() {
		log.Printf("Starting Pilot Cloud on port %d", config.Port)
		if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("HTTP server error: %v", err)
		}
	}()

	// Start progress update consumer (for WebSocket/SSE)
	go func() {
		for update := range executor.ProgressUpdates() {
			// In production, this would broadcast to connected clients
			log.Printf("Progress: %s - %s (%d%%)", update.ExecutionID, update.Phase, update.Progress)
		}
	}()

	// Wait for shutdown signal
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	<-sigChan

	log.Println("Shutting down...")

	// Graceful shutdown
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer shutdownCancel()

	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		log.Printf("HTTP server shutdown error: %v", err)
	}

	log.Println("Shutdown complete")
}

// Config holds application configuration
type Config struct {
	// Server
	Port    int
	BaseURL string

	// Database
	DatabaseURL string
	RedisURL    string

	// JWT
	JWTSecretKey string

	// OAuth
	GitHubClientID     string
	GitHubClientSecret string
	LinearClientID     string
	LinearClientSecret string
	JiraClientID       string
	JiraClientSecret   string

	// Stripe
	StripeSecretKey      string
	StripeWebhookSecret  string
	StripePriceProID     string
	StripePriceTeamID    string

	// Executor
	ExecutorImage          string
	ExecutorMemory         string
	ExecutorCPU            string
	ExecutorTimeoutSec     int
	MaxConcurrentExecutions int
}

func loadConfig() *Config {
	return &Config{
		Port:    getEnvInt("PORT", 8080),
		BaseURL: getEnv("BASE_URL", "https://api.pilotdev.ai"),

		DatabaseURL: getEnv("DATABASE_URL", "postgres://localhost:5432/pilot_cloud"),
		RedisURL:    getEnv("REDIS_URL", "redis://localhost:6379"),

		JWTSecretKey: getEnv("JWT_SECRET_KEY", "change-me-in-production"),

		GitHubClientID:     getEnv("GITHUB_CLIENT_ID", ""),
		GitHubClientSecret: getEnv("GITHUB_CLIENT_SECRET", ""),
		LinearClientID:     getEnv("LINEAR_CLIENT_ID", ""),
		LinearClientSecret: getEnv("LINEAR_CLIENT_SECRET", ""),
		JiraClientID:       getEnv("JIRA_CLIENT_ID", ""),
		JiraClientSecret:   getEnv("JIRA_CLIENT_SECRET", ""),

		StripeSecretKey:     getEnv("STRIPE_SECRET_KEY", ""),
		StripeWebhookSecret: getEnv("STRIPE_WEBHOOK_SECRET", ""),
		StripePriceProID:    getEnv("STRIPE_PRICE_PRO_ID", ""),
		StripePriceTeamID:   getEnv("STRIPE_PRICE_TEAM_ID", ""),

		ExecutorImage:           getEnv("EXECUTOR_IMAGE", "pilot/executor:latest"),
		ExecutorMemory:          getEnv("EXECUTOR_MEMORY", "2Gi"),
		ExecutorCPU:             getEnv("EXECUTOR_CPU", "1"),
		ExecutorTimeoutSec:      getEnvInt("EXECUTOR_TIMEOUT_SEC", 600),
		MaxConcurrentExecutions: getEnvInt("MAX_CONCURRENT_EXECUTIONS", 10),
	}
}

func getEnv(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
}

func getEnvInt(key string, defaultValue int) int {
	if value := os.Getenv(key); value != "" {
		var result int
		_, _ = fmt.Sscanf(value, "%d", &result)
		return result
	}
	return defaultValue
}
