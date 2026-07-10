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

type templateIDInput struct {
	TemplateID string `json:"template_id" jsonschema:"required,description=Template ID"`
}

type createTemplateInput struct {
	Name        string              `json:"name" jsonschema:"required,description=Template name"`
	Description string              `json:"description,omitempty" jsonschema:"description=Template description"`
	Metrics     []templateMetricDef `json:"metrics" jsonschema:"required,description=Metric definitions"`
}

type templateMetricDef struct {
	Name            string `json:"name" jsonschema:"required,description=Metric name"`
	DescriptionGood string `json:"description_good" jsonschema:"required,description=What good looks like"`
	DescriptionBad  string `json:"description_bad" jsonschema:"required,description=What bad looks like"`
}

func registerTemplateTools(srv *mcp.Server, store *storage.Store, logger *bolt.Logger) {
	srv.Tool("list_templates").
		Description("List all available health check templates. Includes the built-in Spotify Squad Health Check template.").
		Handler(func(ctx context.Context, in struct{}) (any, error) {
			templates, err := store.FindAllTemplates()
			if err != nil {
				return nil, err
			}
			if templates == nil {
				templates = []*domain.Template{}
			}
			return templates, nil
		})

	srv.Tool("get_template").
		Description("Get full details of a template including all metric definitions with good/bad descriptions").
		OutputSchema(domain.Template{}).
		Handler(func(ctx context.Context, in templateIDInput) (any, error) {
			tmpl, err := store.FindTemplateByID(in.TemplateID)
			if err != nil {
				return nil, err
			}
			if tmpl == nil {
				return nil, fmt.Errorf("template %q not found", in.TemplateID)
			}
			return tmpl, nil
		})

	srv.Tool("create_template").
		Description("Create a custom health check template with metrics").
		Handler(func(ctx context.Context, in createTemplateInput) (any, error) {
			tmplID := uuid.NewString()
			metrics := make([]domain.TemplateMetric, len(in.Metrics))
			for i, m := range in.Metrics {
				metrics[i] = domain.TemplateMetric{
					ID:              uuid.NewString(),
					TemplateID:      tmplID,
					Name:            m.Name,
					DescriptionGood: m.DescriptionGood,
					DescriptionBad:  m.DescriptionBad,
					SortOrder:       i + 1,
				}
			}

			tmpl := &domain.Template{
				ID:          tmplID,
				Name:        in.Name,
				Description: in.Description,
				BuiltIn:     false,
				Metrics:     metrics,
				CreatedAt:   time.Now(),
			}

			if err := store.CreateTemplate(tmpl); err != nil {
				return nil, fmt.Errorf("create template: %w", err)
			}
			return tmpl, nil
		})

	srv.Tool("delete_template").
		Description("Delete a custom template. Built-in templates cannot be deleted.").
		Handler(func(ctx context.Context, in templateIDInput) (any, error) {
			if err := store.DeleteTemplate(in.TemplateID); err != nil {
				return nil, err
			}
			return map[string]string{"status": "deleted", "template_id": in.TemplateID}, nil
		})
}
