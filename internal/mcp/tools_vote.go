package mcp

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	bolt "go.klarlabs.de/bolt"
	"go.klarlabs.de/mcp"

	"github.com/felixgeelhaar/heartbeat/internal/auth"
	"github.com/felixgeelhaar/heartbeat/internal/domain"
	"github.com/felixgeelhaar/heartbeat/internal/storage"
)

type submitVoteInput struct {
	HealthCheckID string `json:"healthcheck_id" jsonschema:"required,description=Health check session ID"`
	MetricName    string `json:"metric_name" jsonschema:"required,description=Name of the metric to vote on"`
	Participant   string `json:"participant,omitempty" jsonschema:"description=Name of the person voting (auto-filled from auth when available)"`
	Color         string `json:"color" jsonschema:"required,description=Vote color: green yellow or red"`
	Comment       string `json:"comment,omitempty" jsonschema:"description=Optional comment explaining the vote"`
}

type getResultsInput struct {
	HealthCheckID string `json:"healthcheck_id" jsonschema:"required,description=Health check session ID"`
}

// getResultsOutput is the structured result of get_results.
type getResultsOutput struct {
	HealthCheck  *domain.HealthCheck   `json:"healthcheck"`
	Results      []domain.MetricResult `json:"results"`
	AverageScore float64               `json:"average_score"`
	Participants int                   `json:"participants"`
	TotalVotes   int                   `json:"total_votes"`
}

func registerVoteTools(srv *mcp.Server, store *storage.Store, logger *bolt.Logger) {
	srv.Tool("submit_vote").
		Description("Submit a vote for a metric in an open health check. One vote per participant per metric; re-submitting updates the existing vote.").
		Handler(func(ctx context.Context, in submitVoteInput) (any, error) {
			// Auto-fill participant from auth identity when available
			if in.Participant == "" {
				if identity := auth.IdentityFromContext(ctx); identity != nil {
					in.Participant = identity.Name
				} else {
					return nil, fmt.Errorf("participant is required (no auth identity available)")
				}
			}

			// Validate health check exists
			hc, err := store.FindHealthCheckByID(in.HealthCheckID)
			if err != nil {
				return nil, err
			}
			if hc == nil {
				return nil, fmt.Errorf("health check %q not found", in.HealthCheckID)
			}

			// Get template for metric validation
			tmpl, err := store.FindTemplateByID(hc.TemplateID)
			if err != nil {
				return nil, err
			}

			// Use aggregate root to validate and create vote
			vote, err := hc.CastVote(in.MetricName, in.Participant, in.Color, in.Comment, tmpl.Metrics)
			if err != nil {
				return nil, err
			}
			vote.ID = uuid.NewString()
			vote.CreatedAt = time.Now()

			if err := store.UpsertVote(vote); err != nil {
				return nil, fmt.Errorf("submit vote: %w", err)
			}

			return vote, nil
		})

	srv.Tool("get_results").
		Description("Get aggregated results for a health check: per-metric breakdown of green/yellow/red counts, computed score (1-3), and all comments").
		OutputSchema(getResultsOutput{}).
		Handler(func(ctx context.Context, in getResultsInput) (any, error) {
			hc, err := store.FindHealthCheckByID(in.HealthCheckID)
			if err != nil {
				return nil, err
			}
			if hc == nil {
				return nil, fmt.Errorf("health check %q not found", in.HealthCheckID)
			}

			tmpl, err := store.FindTemplateByID(hc.TemplateID)
			if err != nil {
				return nil, err
			}

			votes, err := store.FindVotesByHealthCheck(in.HealthCheckID)
			if err != nil {
				return nil, err
			}

			results := domain.ComputeMetricResults(votes, tmpl.Metrics)

			// Compute overall stats
			var totalScore float64
			var totalVotes int
			participants := make(map[string]bool)
			for _, v := range votes {
				participants[v.Participant] = true
			}
			for _, r := range results {
				totalScore += r.Score * float64(r.TotalVotes)
				totalVotes += r.TotalVotes
			}
			var avgScore float64
			if totalVotes > 0 {
				avgScore = totalScore / float64(totalVotes)
			}

			return getResultsOutput{
				HealthCheck:  hc,
				Results:      results,
				AverageScore: avgScore,
				Participants: len(participants),
				TotalVotes:   totalVotes,
			}, nil
		})
}
