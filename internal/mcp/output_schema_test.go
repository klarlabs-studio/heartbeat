package mcp

import (
	"testing"

	"go.klarlabs.de/mcp/schema"

	"github.com/felixgeelhaar/heartbeat/internal/domain"
)

// TestOutputSchemasGenerate guards every type advertised via a tool's
// OutputSchema(...) call. OutputSchema runs schema.Generate at registration
// time and silently drops the tool if generation fails, so a broken output
// type would disappear from the server with no error surfaced. This test
// fails loudly instead: if any advertised type stops being schema-generatable,
// CI catches it here rather than shipping a server that is missing the tool.
func TestOutputSchemasGenerate(t *testing.T) {
	tests := []struct {
		name string
		typ  any
	}{
		{"analyze_healthcheck", analyzeOutput{}},
		{"get_trends", trendsOutput{}},
		{"get_discussion_topics", discussionOutput{}},
		{"compare_sessions", compareOutput{}},
		{"get_results", getResultsOutput{}},
		{"get_healthcheck", getHealthCheckOutput{}},
		{"my_pending_healthchecks", myPendingOutput{}},
		{"get_team", domain.Team{}},
		{"get_template", domain.Template{}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s, err := schema.Generate(tt.typ)
			if err != nil {
				t.Fatalf("schema.Generate(%T) returned error: %v", tt.typ, err)
			}
			if s == nil {
				t.Fatalf("schema.Generate(%T) returned nil schema", tt.typ)
			}
		})
	}
}
