package mcp

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strings"

	bolt "go.klarlabs.de/bolt"
	"go.klarlabs.de/mcp"

	"github.com/felixgeelhaar/heartbeat/internal/domain"
	"github.com/felixgeelhaar/heartbeat/internal/storage"
)

type analyzeInput struct {
	HealthCheckID string `json:"healthcheck_id" jsonschema:"required,description=Health check session ID"`
}

type trendsInput struct {
	TeamID string `json:"team_id" jsonschema:"required,description=Team ID"`
	Limit  int    `json:"limit,omitempty" jsonschema:"description=Number of sessions to analyze (default 10)"`
}

type discussionInput struct {
	HealthCheckID string `json:"healthcheck_id" jsonschema:"required,description=Health check session ID"`
	IncludeTrends bool   `json:"include_trends,omitempty" jsonschema:"description=Include cross-session trend data (default true)"`
}

// analyzeMetricEntry is a single metric entry in an analysis summary's
// strengths/concerns lists.
type analyzeMetricEntry struct {
	Metric string  `json:"metric"`
	Score  float64 `json:"score"`
	Votes  string  `json:"votes"`
}

// analyzeOutput is the structured result of analyze_healthcheck.
type analyzeOutput struct {
	HealthCheck  string                `json:"healthcheck"`
	Template     string                `json:"template"`
	Status       domain.Status         `json:"status"`
	Participants int                   `json:"participants"`
	Strengths    []analyzeMetricEntry  `json:"strengths"`
	Concerns     []analyzeMetricEntry  `json:"concerns"`
	AllResults   []domain.MetricResult `json:"all_results"`
}

// trendsOutput is the structured result of get_trends.
type trendsOutput struct {
	TeamID         string               `json:"team_id"`
	SessionsCount  int                  `json:"sessions_count"`
	Improving      []domain.MetricTrend `json:"improving"`
	Declining      []domain.MetricTrend `json:"declining"`
	Stable         []domain.MetricTrend `json:"stable"`
	NeedsAttention []domain.MetricTrend `json:"needs_attention"`
}

// discussionTopic is a single suggested discussion topic.
type discussionTopic struct {
	Priority   int      `json:"priority"`
	Metric     string   `json:"metric"`
	Reason     string   `json:"reason"`
	DataPoints []string `json:"data_points"`
	Questions  []string `json:"suggested_questions"`
}

// discussionOutput is the structured result of get_discussion_topics.
type discussionOutput struct {
	HealthCheck   string            `json:"healthcheck"`
	Topics        []discussionTopic `json:"topics"`
	TrendWarnings []string          `json:"trend_warnings"`
	Participants  int               `json:"participants"`
}

func registerAnalyzeTools(srv *mcp.Server, store *storage.Store, logger *bolt.Logger) {
	srv.Tool("analyze_healthcheck").
		Description("Generate an AI-friendly analysis summary of a health check session. Includes strongest/weakest areas, metrics needing attention, and participation stats.").
		OutputSchema(analyzeOutput{}).
		Handler(func(ctx context.Context, in analyzeInput) (any, error) {
			hc, tmpl, results, votes, err := loadHealthCheckData(store, in.HealthCheckID)
			if err != nil {
				return nil, err
			}

			// Sort by score for strengths/concerns
			sorted := make([]domain.MetricResult, len(results))
			copy(sorted, results)
			sort.Slice(sorted, func(i, j int) bool {
				return sorted[i].Score > sorted[j].Score
			})

			participants := uniqueParticipants(votes)

			var strengths, concerns []analyzeMetricEntry
			for _, r := range sorted {
				if r.TotalVotes == 0 {
					continue
				}
				entry := analyzeMetricEntry{
					Metric: r.MetricName,
					Score:  r.Score,
					Votes:  fmt.Sprintf("G:%d Y:%d R:%d", r.GreenCount, r.YellowCount, r.RedCount),
				}
				if r.Score >= 2.5 {
					strengths = append(strengths, entry)
				} else if r.Score < 2.0 {
					concerns = append(concerns, entry)
				}
			}

			return analyzeOutput{
				HealthCheck:  hc.Name,
				Template:     tmpl.Name,
				Status:       hc.Status,
				Participants: len(participants),
				Strengths:    strengths,
				Concerns:     concerns,
				AllResults:   results,
			}, nil
		})

	srv.Tool("get_trends").
		Description("Analyze trends for a team across all historical sessions. Flags declining metrics and highlights improving ones.").
		OutputSchema(trendsOutput{}).
		Handler(func(ctx context.Context, in trendsInput) (any, error) {
			limit := in.Limit
			if limit <= 0 {
				limit = 10
			}

			hcs, err := store.FindAllHealthChecks(domain.HealthCheckFilter{
				TeamID: &in.TeamID,
				Limit:  limit,
			})
			if err != nil {
				return nil, err
			}
			if len(hcs) == 0 {
				return nil, fmt.Errorf("no sessions found for team %q", in.TeamID)
			}

			sort.Slice(hcs, func(i, j int) bool {
				return hcs[i].CreatedAt.Before(hcs[j].CreatedAt)
			})

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
				for _, r := range domain.ComputeMetricResults(votes, tmpl.Metrics) {
					metricScores[r.MetricName] = append(metricScores[r.MetricName], domain.SessionScore{
						HealthCheckID:   hc.ID,
						HealthCheckName: hc.Name,
						Score:           r.Score,
						Date:            hc.CreatedAt.Format("2006-01-02"),
					})
				}
			}

			var improving, declining, stable []domain.MetricTrend
			for name, sessions := range metricScores {
				var delta float64
				if len(sessions) >= 2 && sessions[0].Score > 0 {
					delta = sessions[len(sessions)-1].Score - sessions[0].Score
				}
				trend := domain.MetricTrend{
					MetricName: name,
					Sessions:   sessions,
					Tendency:   domain.ComputeTendency(delta),
					Delta:      delta,
				}
				switch trend.Tendency {
				case domain.TendencyImproving:
					improving = append(improving, trend)
				case domain.TendencyDeclining:
					declining = append(declining, trend)
				default:
					stable = append(stable, trend)
				}
			}

			return trendsOutput{
				TeamID:         in.TeamID,
				SessionsCount:  len(hcs),
				Improving:      improving,
				Declining:      declining,
				Stable:         stable,
				NeedsAttention: declining,
			}, nil
		})

	srv.Tool("get_discussion_topics").
		Description("Generate suggested discussion topics based on health check results. Prioritizes metrics with high disagreement, declining trends, and consistently low scores.").
		OutputSchema(discussionOutput{}).
		Handler(func(ctx context.Context, in discussionInput) (any, error) {
			hc, tmpl, results, votes, err := loadHealthCheckData(store, in.HealthCheckID)
			if err != nil {
				return nil, err
			}
			_ = tmpl

			var topics []discussionTopic
			priority := 1

			for _, r := range results {
				if r.TotalVotes == 0 {
					continue
				}

				var reasons []string
				var dataPoints []string
				var questions []string

				// Check for disagreement (high variance in votes)
				if r.GreenCount > 0 && r.RedCount > 0 {
					spread := math.Abs(float64(r.GreenCount-r.RedCount)) / float64(r.TotalVotes)
					if spread < 0.5 {
						reasons = append(reasons, "high disagreement")
						dataPoints = append(dataPoints, fmt.Sprintf("Split vote: %d green, %d yellow, %d red", r.GreenCount, r.YellowCount, r.RedCount))
						questions = append(questions, fmt.Sprintf("What different experiences lead to such varied opinions on %s?", r.MetricName))
					}
				}

				// Check for low score
				if r.Score < 2.0 {
					reasons = append(reasons, "low score")
					dataPoints = append(dataPoints, fmt.Sprintf("Score: %.1f/3.0", r.Score))
					questions = append(questions, fmt.Sprintf("What specific changes would improve %s the most?", r.MetricName))
				}

				if len(reasons) > 0 {
					topics = append(topics, discussionTopic{
						Priority:   priority,
						Metric:     r.MetricName,
						Reason:     strings.Join(reasons, " + "),
						DataPoints: dataPoints,
						Questions:  questions,
					})
					priority++
				}
			}

			// Include trend data if requested
			var trendWarnings []string
			if in.IncludeTrends {
				prevHCs, err := store.FindAllHealthChecks(domain.HealthCheckFilter{
					TeamID: &hc.TeamID,
					Limit:  5,
				})
				if err == nil && len(prevHCs) >= 2 {
					sort.Slice(prevHCs, func(i, j int) bool {
						return prevHCs[i].CreatedAt.Before(prevHCs[j].CreatedAt)
					})

					metricScores := make(map[string][]float64)
					for _, prev := range prevHCs {
						prevTmpl, err := store.FindTemplateByID(prev.TemplateID)
						if err != nil {
							continue
						}
						prevVotes, err := store.FindVotesByHealthCheck(prev.ID)
						if err != nil {
							continue
						}
						for _, r := range domain.ComputeMetricResults(prevVotes, prevTmpl.Metrics) {
							metricScores[r.MetricName] = append(metricScores[r.MetricName], r.Score)
						}
					}

					for name, scores := range metricScores {
						if len(scores) >= 2 {
							delta := scores[len(scores)-1] - scores[0]
							if delta < -0.5 {
								trendWarnings = append(trendWarnings, fmt.Sprintf("%s has declined %.1f points over %d sessions", name, -delta, len(scores)))
							}
						}
					}
				}
			}

			participants := uniqueParticipants(votes)

			return discussionOutput{
				HealthCheck:   hc.Name,
				Topics:        topics,
				TrendWarnings: trendWarnings,
				Participants:  len(participants),
			}, nil
		})
}

func loadHealthCheckData(store *storage.Store, id string) (*domain.HealthCheck, *domain.Template, []domain.MetricResult, []*domain.Vote, error) {
	hc, err := store.FindHealthCheckByID(id)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	if hc == nil {
		return nil, nil, nil, nil, fmt.Errorf("health check %q not found", id)
	}

	tmpl, err := store.FindTemplateByID(hc.TemplateID)
	if err != nil {
		return nil, nil, nil, nil, err
	}

	votes, err := store.FindVotesByHealthCheck(id)
	if err != nil {
		return nil, nil, nil, nil, err
	}

	results := domain.ComputeMetricResults(votes, tmpl.Metrics)
	return hc, tmpl, results, votes, nil
}

func uniqueParticipants(votes []*domain.Vote) map[string]bool {
	p := make(map[string]bool)
	for _, v := range votes {
		p[v.Participant] = true
	}
	return p
}
