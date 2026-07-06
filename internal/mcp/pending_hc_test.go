package mcp_test

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	bolt "go.klarlabs.de/bolt"
	mcpserver "go.klarlabs.de/mcp/server"

	"github.com/felixgeelhaar/heartbeat/internal/auth"
	"github.com/felixgeelhaar/heartbeat/internal/domain"
	"github.com/felixgeelhaar/heartbeat/internal/lifecycle"
	mcptools "github.com/felixgeelhaar/heartbeat/internal/mcp"
	"github.com/felixgeelhaar/heartbeat/internal/storage"
)

// executeTool calls a named tool directly with a custom context (bypassing TestClient).
func executeTool(t *testing.T, srv interface {
	GetTool(string) (*mcpserver.Tool, bool)
}, ctx context.Context, toolName string, args map[string]any) (json.RawMessage, error) {
	t.Helper()

	tool, ok := srv.GetTool(toolName)
	if !ok {
		t.Fatalf("tool %q not found", toolName)
	}

	argBytes, err := json.Marshal(args)
	if err != nil {
		t.Fatalf("marshal args: %v", err)
	}

	result, err := tool.Execute(ctx, argBytes)
	if err != nil {
		return nil, err
	}

	data, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("marshal result: %v", err)
	}
	return data, nil
}

func newRawServer(t *testing.T) (*mcpserver.Server, *storage.Store) {
	t.Helper()
	logger := bolt.New(bolt.NewConsoleHandler(os.Stderr))
	store, err := storage.New(":memory:", logger)
	if err != nil {
		t.Fatalf("create store: %v", err)
	}
	t.Cleanup(func() { store.Close() })

	lc, err := lifecycle.New(logger)
	if err != nil {
		t.Fatalf("lifecycle: %v", err)
	}

	srv := mcptools.NewServer(store, logger, lc)
	return srv, store
}

func TestMyPendingHealthchecks_WithAuth_NoTeamID(t *testing.T) {
	srv, _ := newRawServer(t)

	// Identity with no team_id in metadata
	identity := &auth.Identity{
		ID:       "user-1",
		Name:     "Alice",
		Metadata: map[string]any{},
	}
	ctx := auth.ContextWithIdentity(context.Background(), identity)

	_, err := executeTool(t, srv, ctx, "my_pending_healthchecks", map[string]any{})
	if err == nil {
		t.Error("expected error when identity has no team_id")
	}
}

func TestMyPendingHealthchecks_WithAuth_ValidTeamID_NoPending(t *testing.T) {
	srv, store := newRawServer(t)
	now := time.Now()

	// Create team and health check
	tmpl, _ := store.FindTemplateByName("Spotify Squad Health Check")
	team := &domain.Team{
		ID: uuid.NewString(), Name: "Auth Test Team",
		Members: []string{"Alice"}, CreatedAt: now, UpdatedAt: now,
	}
	store.CreateTeam(team)

	// Create HC and have Alice vote on all metrics
	hc := &domain.HealthCheck{
		ID: uuid.NewString(), TeamID: team.ID, TemplateID: tmpl.ID,
		Name: "Auth Sprint", Status: domain.StatusOpen, CreatedAt: now,
	}
	store.CreateHealthCheck(hc)

	// Vote on all 10 Spotify metrics
	for _, m := range tmpl.Metrics {
		store.UpsertVote(&domain.Vote{
			ID:            uuid.NewString(),
			HealthCheckID: hc.ID,
			MetricName:    m.Name,
			Participant:   "Alice",
			Color:         domain.VoteGreen,
			CreatedAt:     now,
		})
	}

	identity := &auth.Identity{
		ID:   "user-alice",
		Name: "Alice",
		Metadata: map[string]any{
			"team_id": team.ID,
		},
	}
	ctx := auth.ContextWithIdentity(context.Background(), identity)

	raw, err := executeTool(t, srv, ctx, "my_pending_healthchecks", map[string]any{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var resp struct {
		User    string `json:"user"`
		Pending []any  `json:"pending"`
	}
	json.Unmarshal(raw, &resp)

	if resp.User != "Alice" {
		t.Errorf("expected user 'Alice', got %q", resp.User)
	}
	// Alice has voted on all metrics — should be 0 pending HCs
	if len(resp.Pending) != 0 {
		t.Errorf("expected 0 pending HCs, got %d", len(resp.Pending))
	}
}

func TestMyPendingHealthchecks_WithAuth_HasPending(t *testing.T) {
	srv, store := newRawServer(t)
	now := time.Now()

	// Create team and health check
	tmpl, _ := store.FindTemplateByName("Spotify Squad Health Check")
	team := &domain.Team{
		ID: uuid.NewString(), Name: "Pending Test Team",
		Members: []string{"Bob"}, CreatedAt: now, UpdatedAt: now,
	}
	store.CreateTeam(team)

	hc := &domain.HealthCheck{
		ID: uuid.NewString(), TeamID: team.ID, TemplateID: tmpl.ID,
		Name: "Pending Sprint", Status: domain.StatusOpen, CreatedAt: now,
	}
	store.CreateHealthCheck(hc)

	// Bob has only voted on 1 metric — 9 metrics still pending
	store.UpsertVote(&domain.Vote{
		ID:            uuid.NewString(),
		HealthCheckID: hc.ID,
		MetricName:    "Fun",
		Participant:   "Bob",
		Color:         domain.VoteGreen,
		CreatedAt:     now,
	})

	identity := &auth.Identity{
		ID:   "user-bob",
		Name: "Bob",
		Metadata: map[string]any{
			"team_id": team.ID,
		},
	}
	ctx := auth.ContextWithIdentity(context.Background(), identity)

	raw, err := executeTool(t, srv, ctx, "my_pending_healthchecks", map[string]any{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var resp struct {
		User    string `json:"user"`
		Pending []struct {
			TotalMetrics   int      `json:"total_metrics"`
			VotedMetrics   int      `json:"voted_metrics"`
			PendingMetrics []string `json:"pending_metrics"`
		} `json:"pending"`
	}
	json.Unmarshal(raw, &resp)

	if resp.User != "Bob" {
		t.Errorf("expected user 'Bob', got %q", resp.User)
	}
	if len(resp.Pending) != 1 {
		t.Fatalf("expected 1 pending HC, got %d", len(resp.Pending))
	}
	if resp.Pending[0].TotalMetrics != 10 {
		t.Errorf("expected 10 total metrics, got %d", resp.Pending[0].TotalMetrics)
	}
	if resp.Pending[0].VotedMetrics != 1 {
		t.Errorf("expected 1 voted metric, got %d", resp.Pending[0].VotedMetrics)
	}
	if len(resp.Pending[0].PendingMetrics) != 9 {
		t.Errorf("expected 9 pending metrics, got %d", len(resp.Pending[0].PendingMetrics))
	}
}

func TestSubmitVote_WithAuthIdentity(t *testing.T) {
	srv, store := newRawServer(t)
	now := time.Now()

	// Set up team and HC
	tmpl, _ := store.FindTemplateByName("Spotify Squad Health Check")
	team := &domain.Team{
		ID: uuid.NewString(), Name: "Auth Vote Team",
		Members: []string{"Carol"}, CreatedAt: now, UpdatedAt: now,
	}
	store.CreateTeam(team)

	hc := &domain.HealthCheck{
		ID: uuid.NewString(), TeamID: team.ID, TemplateID: tmpl.ID,
		Name: "Auth Vote Sprint", Status: domain.StatusOpen, CreatedAt: now,
	}
	store.CreateHealthCheck(hc)

	// Submit vote using auth identity (no explicit participant)
	identity := &auth.Identity{
		ID:   "user-carol",
		Name: "Carol",
		Metadata: map[string]any{
			"team_id": team.ID,
		},
	}
	ctx := auth.ContextWithIdentity(context.Background(), identity)

	raw, err := executeTool(t, srv, ctx, "submit_vote", map[string]any{
		"healthcheck_id": hc.ID,
		"metric_name":    "Fun",
		// no "participant" — should be auto-filled from identity
		"color": "green",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var vote struct {
		Participant string `json:"Participant"`
		Color       string `json:"Color"`
	}
	json.Unmarshal(raw, &vote)

	if vote.Participant != "Carol" {
		t.Errorf("expected participant 'Carol' from auth identity, got %q", vote.Participant)
	}
}
