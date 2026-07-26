# Changelog

## [v1.1.1](https://github.com/klarlabs-studio/heartbeat/releases/tag/v1.1.1) (2026-07-26)

No user-facing change. This release exists because **v1.1.0 shipped with zero
artefacts** — it was published by hand and nothing ever ran goreleaser, so
there were no binaries to download and the Homebrew formula stayed at 1.0.0.

### Changed

- **The release is now automated.** `.github/workflows/release.yml` did not
  exist; `.goreleaser.yaml` had declared a `brews:` block since it was written
  and nothing invoked it.
- **Publishes to `klarlabs-studio/homebrew-tap`**, whose owner matches the
  credential that writes it. Pointing at a personally-owned tap with an
  org-owned token returns `403 Resource not accessible by personal access
  token`.
- **Ships a Homebrew cask rather than a formula**, `brews` being deprecated in
  goreleaser. `goreleaser check` now reports no deprecations.

## [v1.1.0](https://github.com/klarlabs-studio/heartbeat/releases/tag/v1.1.0) (2026-07-10)

Released without a changelog entry at the time; reconstructed from history.

### Added

- MCP data tools advertise output schemas (#12)

### Changed

- Klarlabs library dependencies migrated to `go.klarlabs.de` vanity paths
- `go.klarlabs.de/mcp` to v1.22.0, migrated off the removed in-library auth
- Dependabot auto-merge for patch and minor updates (#5)

### Fixed

- CI build path corrected to `./cmd/heartbeat/` (#3)
- Transitive vite/postcss vulnerabilities patched in the dashboard SPA

## [v1.0.0](https://github.com/klarlabs-studio/heartbeat/releases/tag/v1.0.0) (2026-03-28)

### Breaking Changes

- Complete pivot from Go library to MCP server product
- Removed `Indicator` and `HealthMetric` exported types (replaced by internal domain model)
- Module now provides `cmd/heartbeat` binary instead of importable package

### Features

- MCP server with 24 tools for full health check lifecycle
- Dual transport: stdio (single-user) and HTTP/SSE (multi-user)
- Token-based authentication with auto-filled participant identity
- `my_pending_healthchecks` tool for authenticated users
- statekit-powered state machine: open → closed → archived lifecycle
- Guards enforce business rules (can't close without votes)
- `reopen_healthcheck` and `archive_healthcheck` tools
- bolt structured logging (JSON prod, colored console dev)
- fortify resilience middleware via mcp-go (rate limiting, timeouts)
- SQLite persistence (pure Go, no CGO) with WAL mode and busy timeout
- Built-in Spotify Squad Health Check template (10 metrics)
- Custom template creation
- Team management with member tracking
- Vote submission with upsert semantics (one vote per participant per metric)
- Aggregated results with score computation (1-3 scale)
- Cross-session comparison with trend detection (improving/stable/declining)
- AI-friendly analysis: strengths, concerns, discussion topics
- Discussion topic generation based on disagreement, low scores, and declining trends
- Live web dashboard with real-time updates via WebSocket (React SPA, `--dashboard-addr :3000`)
- Event bus architecture: Store publishes events on mutations, dashboard auto-updates
- MCP Apps UIResource: interactive voting form and results heatmap (rendered in Claude Desktop)
- Dashboard REST API: `/api/teams`, `/api/healthchecks`, `/api/healthchecks/{id}/results`
- SPA embedded in binary via `embed.FS` — single binary deployment
- Dark glassmorphism UI design (Linear/Vercel aesthetic)
- Web-based voting with metric descriptions (good/bad anchors visible)
- Spotify metric picker: click individual metrics or "Add All" for custom templates
- Radar/spider chart: SVG at-a-glance visualization of all metric scores
- AI discussion guide: surfaces disagreement patterns and low scores with suggested questions
- Anonymous voting: toggle per health check, strips names and comments from results
- Team health trends: bar chart + per-metric sparklines with tendency indicators
- Comments visible in expanded metric rows
- Participant avatars with colored initials
- Pre-commit hooks: go fmt, go vet, go test, nox security scan, coverctl coverage

## [v0.1.1](https://github.com/FelixGeelhaar/go-teamhealthcheck/releases/tag/v0.1.1) (2020-04-14)

Github Action, Documentation and several documentation updates

## [v0.1.0](https://github.com/FelixGeelhaar/go-teamhealthcheck/releases/tag/v0.1.0) (2020-04-14)

### Features

- Added the inital model for the Health metric and the traffic light indicators
  ([56ebeee](https://github.com/FelixGeelhaar/go-teamhealthcheck/commit/56ebeee7a7e8a6e4d92aa00136698e8a726d6a0a))

### Bugs

No bugs smashed
