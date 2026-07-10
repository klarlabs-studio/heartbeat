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

type createHealthCheckInput struct {
	TeamID     string `json:"team_id" jsonschema:"required,description=Team ID"`
	TemplateID string `json:"template_id" jsonschema:"required,description=Template ID to use"`
	Name       string `json:"name" jsonschema:"required,description=Session name (e.g. Sprint 42)"`
}

type healthCheckIDInput struct {
	HealthCheckID string `json:"healthcheck_id" jsonschema:"required,description=Health check session ID"`
}

type listHealthChecksInput struct {
	TeamID string `json:"team_id,omitempty" jsonschema:"description=Filter by team ID"`
	Status string `json:"status,omitempty" jsonschema:"description=Filter by status: open or closed"`
	Limit  int    `json:"limit,omitempty" jsonschema:"description=Maximum number of results (default 20)"`
}

// getHealthCheckOutput is the structured result of get_healthcheck.
type getHealthCheckOutput struct {
	HealthCheck *domain.HealthCheck   `json:"healthcheck"`
	Results     []domain.MetricResult `json:"results"`
}

// pendingHealthCheck describes an open health check with metrics the
// authenticated user has not yet voted on.
type pendingHealthCheck struct {
	HealthCheck    *domain.HealthCheck `json:"healthcheck"`
	TotalMetrics   int                 `json:"total_metrics"`
	VotedMetrics   int                 `json:"voted_metrics"`
	PendingMetrics []string            `json:"pending_metrics"`
}

// myPendingOutput is the structured result of my_pending_healthchecks.
type myPendingOutput struct {
	User    string               `json:"user"`
	Pending []pendingHealthCheck `json:"pending"`
}

func registerHealthCheckTools(srv *mcp.Server, store *storage.Store, logger *bolt.Logger, sm domain.HealthCheckLifecycle) {
	srv.Tool("create_healthcheck").
		Description("Create a new health check session for a team using a template. The session starts in 'open' status, ready for votes.").
		Handler(func(ctx context.Context, in createHealthCheckInput) (any, error) {
			// Validate team exists
			team, err := store.FindTeamByID(in.TeamID)
			if err != nil {
				return nil, err
			}
			if team == nil {
				return nil, fmt.Errorf("team %q not found", in.TeamID)
			}

			// Validate template exists
			tmpl, err := store.FindTemplateByID(in.TemplateID)
			if err != nil {
				return nil, err
			}
			if tmpl == nil {
				return nil, fmt.Errorf("template %q not found", in.TemplateID)
			}

			hc := &domain.HealthCheck{
				ID:         uuid.NewString(),
				TeamID:     in.TeamID,
				TemplateID: in.TemplateID,
				Name:       in.Name,
				Status:     domain.StatusOpen,
				CreatedAt:  time.Now(),
			}

			if err := store.CreateHealthCheck(hc); err != nil {
				return nil, fmt.Errorf("create health check: %w", err)
			}

			return map[string]any{
				"healthcheck": hc,
				"metrics":     tmpl.Metrics,
			}, nil
		})

	srv.Tool("list_healthchecks").
		Description("List health check sessions, optionally filtered by team and/or status").
		Handler(func(ctx context.Context, in listHealthChecksInput) (any, error) {
			filter := domain.HealthCheckFilter{Limit: in.Limit}
			if in.TeamID != "" {
				filter.TeamID = &in.TeamID
			}
			if in.Status != "" {
				s := domain.Status(in.Status)
				filter.Status = &s
			}

			hcs, err := store.FindAllHealthChecks(filter)
			if err != nil {
				return nil, err
			}
			if hcs == nil {
				hcs = []*domain.HealthCheck{}
			}
			return hcs, nil
		})

	srv.Tool("get_healthcheck").
		Description("Get details of a specific health check session including current vote counts per metric").
		OutputSchema(getHealthCheckOutput{}).
		Handler(func(ctx context.Context, in healthCheckIDInput) (any, error) {
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

			return getHealthCheckOutput{
				HealthCheck: hc,
				Results:     results,
			}, nil
		})

	srv.Tool("close_healthcheck").
		Description("Close a health check session to prevent further voting. Requires at least one vote.").
		Handler(func(ctx context.Context, in healthCheckIDInput) (any, error) {
			hc, err := store.FindHealthCheckByID(in.HealthCheckID)
			if err != nil {
				return nil, err
			}
			if hc == nil {
				return nil, fmt.Errorf("health check %q not found", in.HealthCheckID)
			}

			votes, err := store.FindVotesByHealthCheck(in.HealthCheckID)
			if err != nil {
				return nil, err
			}

			if err := sm.Transition(hc, domain.EventClose, len(votes)); err != nil {
				return nil, err
			}

			if err := store.UpdateHealthCheck(hc); err != nil {
				return nil, err
			}

			return map[string]string{"status": "closed", "healthcheck_id": hc.ID}, nil
		})

	srv.Tool("reopen_healthcheck").
		Description("Reopen a closed health check session to accept more votes").
		Handler(func(ctx context.Context, in healthCheckIDInput) (any, error) {
			hc, err := store.FindHealthCheckByID(in.HealthCheckID)
			if err != nil {
				return nil, err
			}
			if hc == nil {
				return nil, fmt.Errorf("health check %q not found", in.HealthCheckID)
			}

			if err := sm.Transition(hc, domain.EventReopen, 0); err != nil {
				return nil, err
			}

			if err := store.UpdateHealthCheck(hc); err != nil {
				return nil, err
			}

			return map[string]string{"status": "open", "healthcheck_id": hc.ID}, nil
		})

	srv.Tool("archive_healthcheck").
		Description("Archive a closed health check session").
		Handler(func(ctx context.Context, in healthCheckIDInput) (any, error) {
			hc, err := store.FindHealthCheckByID(in.HealthCheckID)
			if err != nil {
				return nil, err
			}
			if hc == nil {
				return nil, fmt.Errorf("health check %q not found", in.HealthCheckID)
			}

			if err := sm.Transition(hc, domain.EventArchive, 0); err != nil {
				return nil, err
			}

			if err := store.UpdateHealthCheck(hc); err != nil {
				return nil, err
			}

			return map[string]string{"status": "archived", "healthcheck_id": hc.ID}, nil
		})

	srv.Tool("delete_healthcheck").
		Description("Delete a health check session and all its votes").
		Handler(func(ctx context.Context, in healthCheckIDInput) (any, error) {
			if err := store.DeleteHealthCheck(in.HealthCheckID); err != nil {
				return nil, err
			}
			return map[string]string{"status": "deleted", "healthcheck_id": in.HealthCheckID}, nil
		})

	srv.Tool("my_pending_healthchecks").
		Description("List open health checks where the authenticated user has not yet voted on all metrics. Requires authentication.").
		OutputSchema(myPendingOutput{}).
		Handler(func(ctx context.Context, in struct{}) (any, error) {
			identity := auth.IdentityFromContext(ctx)
			if identity == nil {
				return nil, fmt.Errorf("authentication required")
			}

			teamID, _ := identity.Metadata["team_id"].(string)
			if teamID == "" {
				return nil, fmt.Errorf("no team_id in auth identity")
			}

			// Find open health checks for user's team
			status := domain.StatusOpen
			hcs, err := store.FindAllHealthChecks(domain.HealthCheckFilter{
				TeamID: &teamID,
				Status: &status,
				Limit:  50,
			})
			if err != nil {
				return nil, err
			}

			var pending []pendingHealthCheck
			for _, hc := range hcs {
				tmpl, err := store.FindTemplateByID(hc.TemplateID)
				if err != nil {
					return nil, err
				}

				votes, err := store.FindVotesByHealthCheck(hc.ID)
				if err != nil {
					return nil, err
				}

				// Count metrics this user has voted on
				votedMetrics := make(map[string]bool)
				for _, v := range votes {
					if v.Participant == identity.Name {
						votedMetrics[v.MetricName] = true
					}
				}

				var pendingMetrics []string
				for _, m := range tmpl.Metrics {
					if !votedMetrics[m.Name] {
						pendingMetrics = append(pendingMetrics, m.Name)
					}
				}

				if len(pendingMetrics) > 0 {
					pending = append(pending, pendingHealthCheck{
						HealthCheck:    hc,
						TotalMetrics:   len(tmpl.Metrics),
						VotedMetrics:   len(votedMetrics),
						PendingMetrics: pendingMetrics,
					})
				}
			}

			if pending == nil {
				pending = []pendingHealthCheck{}
			}

			return myPendingOutput{
				User:    identity.Name,
				Pending: pending,
			}, nil
		})
}
