# Repository Guidelines

Single-package Echo OpenTelemetry middleware. Start with `middleware.go`, `helpers.go`, then the matching tests; there are no subpackages or generated files.

## API and Imports

- Module path is `github.com/adlandh/echo-otel-middleware/v2`; keep the `/v2` suffix in imports and docs.
- Package name is `echootelmiddleware`; README examples alias the import to that name.
- Uses Echo v5 (`github.com/labstack/echo/v5`), whose handlers and skippers take `*echo.Context`.
- Treat exported API as non-breaking: `Middleware`, `MiddlewareWithConfig`, `OtelConfig`, `BodySkipper`, `DefaultOtelConfig`.

## Commands

- CI tests: `go test -race -coverprofile=coverage.txt -covermode=atomic ./...`
- Pre-push tests: `go test -cover -race ./...`
- Focused test: `go test -run TestName .`
- Lint: `golangci-lint run`

## Tooling Gotchas

- `go.mod` declares Go `1.25.0`; CI installs Go `1.25`.
- `.lefthook.yml` and CI lint download `.golangci.yml` from `adlandh/golangci-lint-config` before running lint. Do not rely on local edits to `.golangci.yml`; upstream the shared config instead.
- Current lint config sets `run.tests: false`, so `*_test.go` files are not linted.

## Behavior Notes

- `AreHeadersDump` defaults true; `IsBodyDump` defaults false. Body dumping can capture PII/secrets, so prefer `BodySkipper` for exclusions.
- Request/response attribute names and value limits are byte-based and must preserve valid UTF-8; see `helpers_test.go` before changing truncation.
