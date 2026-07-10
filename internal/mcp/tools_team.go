package mcp

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	bolt "go.klarlabs.de/bolt"
	"go.klarlabs.de/mcp"

	"github.com/felixgeelhaar/heartbeat/internal/domain"
	"github.com/felixgeelhaar/heartbeat/internal/storage"
)

type createTeamInput struct {
	Name    string   `json:"name" jsonschema:"required,description=Team name"`
	Members []string `json:"members,omitempty" jsonschema:"description=Initial team member names"`
}

type teamIDInput struct {
	TeamID string `json:"team_id" jsonschema:"required,description=Team ID"`
}

type teamMemberInput struct {
	TeamID string `json:"team_id" jsonschema:"required,description=Team ID"`
	Name   string `json:"name" jsonschema:"required,description=Member name"`
}

func registerTeamTools(srv *mcp.Server, store *storage.Store, logger *bolt.Logger) {
	srv.Tool("create_team").
		Description("Create a new team for running health checks").
		Handler(func(ctx context.Context, in createTeamInput) (any, error) {
			now := time.Now()
			team := &domain.Team{
				ID:        uuid.NewString(),
				Name:      in.Name,
				Members:   in.Members,
				CreatedAt: now,
				UpdatedAt: now,
			}
			if team.Members == nil {
				team.Members = []string{}
			}
			if err := store.CreateTeam(team); err != nil {
				return nil, fmt.Errorf("create team: %w", err)
			}
			return team, nil
		})

	srv.Tool("list_teams").
		Description("List all teams").
		Handler(func(ctx context.Context, in struct{}) (any, error) {
			teams, err := store.FindAllTeams()
			if err != nil {
				return nil, err
			}
			if teams == nil {
				teams = []*domain.Team{}
			}
			return teams, nil
		})

	srv.Tool("get_team").
		Description("Get details of a specific team including members").
		OutputSchema(domain.Team{}).
		Handler(func(ctx context.Context, in teamIDInput) (any, error) {
			team, err := store.FindTeamByID(in.TeamID)
			if err != nil {
				return nil, err
			}
			if team == nil {
				return nil, fmt.Errorf("team %q not found", in.TeamID)
			}
			return team, nil
		})

	srv.Tool("delete_team").
		Description("Delete a team. Health check history is preserved.").
		Handler(func(ctx context.Context, in teamIDInput) (any, error) {
			if err := store.DeleteTeam(in.TeamID); err != nil {
				return nil, err
			}
			return map[string]string{"status": "deleted", "team_id": in.TeamID}, nil
		})

	srv.Tool("add_team_member").
		Description("Add a member to a team").
		Handler(func(ctx context.Context, in teamMemberInput) (any, error) {
			if err := store.AddTeamMember(in.TeamID, in.Name); err != nil {
				return nil, fmt.Errorf("add member: %w", err)
			}
			return map[string]string{"status": "added", "team_id": in.TeamID, "member": in.Name}, nil
		})

	srv.Tool("remove_team_member").
		Description("Remove a member from a team").
		Handler(func(ctx context.Context, in teamMemberInput) (any, error) {
			if err := store.RemoveTeamMember(in.TeamID, in.Name); err != nil {
				return nil, err
			}
			return map[string]string{"status": "removed", "team_id": in.TeamID, "member": in.Name}, nil
		})
}
