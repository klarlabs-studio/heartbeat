package mcp

import (
	"context"
	"fmt"
	"sort"

	bolt "go.klarlabs.de/bolt"
	"go.klarlabs.de/mcp"

	"github.com/felixgeelhaar/heartbeat/internal/domain"
	"github.com/felixgeelhaar/heartbeat/internal/storage"
)

type compareInput struct {
	TeamID string `json:"team_id" jsonschema:"required,description=Team ID to compare sessions for"`
	Limit  int    `json:"limit,omitempty" jsonschema:"description=Number of most recent sessions to compare (default 5)"`
}

// compareOutput is the structured result of compare_sessions.
type compareOutput struct {
	TeamID        string               `json:"team_id"`
	SessionsCount int                  `json:"sessions_count"`
	Trends        []domain.MetricTrend `json:"trends"`
}

func registerCompareTools(srv *mcp.Server, store *storage.Store, logger *bolt.Logger) {
	srv.Tool("compare_sessions").
		Description("Compare results across multiple health check sessions to track trends over time. Shows per-metric score progression and tendency (improving/stable/declining).").
		OutputSchema(compareOutput{}).
		Handler(func(ctx context.Context, in compareInput) (any, error) {
			limit := in.Limit
			if limit <= 0 {
				limit = 5
			}

			hcs, err := store.FindAllHealthChecks(domain.HealthCheckFilter{
				TeamID: &in.TeamID,
				Limit:  limit,
			})
			if err != nil {
				return nil, err
			}
			if len(hcs) == 0 {
				return nil, fmt.Errorf("no health check sessions found for team %q", in.TeamID)
			}

			// Reverse to chronological order (oldest first)
			sort.Slice(hcs, func(i, j int) bool {
				return hcs[i].CreatedAt.Before(hcs[j].CreatedAt)
			})

			// Collect per-metric scores across sessions
			metricScores := make(map[string][]domain.SessionScore)

			for _, hc := range hcs {
				tmpl, err := store.FindTemplateByID(hc.TemplateID)
				if err != nil {
					return nil, err
				}

				votes, err := store.FindVotesByHealthCheck(hc.ID)
				if err != nil {
					return nil, err
				}

				results := domain.ComputeMetricResults(votes, tmpl.Metrics)
				for _, r := range results {
					metricScores[r.MetricName] = append(metricScores[r.MetricName], domain.SessionScore{
						HealthCheckID:   hc.ID,
						HealthCheckName: hc.Name,
						Score:           r.Score,
						Date:            hc.CreatedAt.Format("2006-01-02"),
					})
				}
			}

			// Build trends
			var trends []domain.MetricTrend
			for name, sessions := range metricScores {
				var delta float64
				if len(sessions) >= 2 && sessions[0].Score > 0 {
					delta = sessions[len(sessions)-1].Score - sessions[0].Score
				}
				trends = append(trends, domain.MetricTrend{
					MetricName: name,
					Sessions:   sessions,
					Tendency:   domain.ComputeTendency(delta),
					Delta:      delta,
				})
			}

			sort.Slice(trends, func(i, j int) bool {
				return trends[i].MetricName < trends[j].MetricName
			})

			return compareOutput{
				TeamID:        in.TeamID,
				SessionsCount: len(hcs),
				Trends:        trends,
			}, nil
		})
}
