---
name: golang-echo-otel-middleware
description: Use when adding or configuring github.com/adlandh/echo-otel-middleware/v2 in a Go Echo application, or when writing examples for this middleware.
---

# Skill: golang-echo-otel-middleware

Use this skill when adding or configuring `github.com/adlandh/echo-otel-middleware/v2` in a Go Echo application, or when writing examples for this middleware.

## Essentials

- Import path must include `/v2`: `github.com/adlandh/echo-otel-middleware/v2`.
- Alias examples as `echootelmiddleware` to match the package name and README.
- This middleware targets Echo v5: use `github.com/labstack/echo/v5` and handler signatures with `*echo.Context`.
- `Middleware()` uses global OpenTelemetry tracer provider and propagator; use `MiddlewareWithConfig` when the app owns explicit OTel setup.

## Basic Usage

```go
app := echo.New()
app.Use(echootelmiddleware.Middleware())
```

Use explicit config when a tracer provider or propagator is constructed by the application:

```go
app.Use(echootelmiddleware.MiddlewareWithConfig(echootelmiddleware.OtelConfig{
	TracerProvider: tp,
	Propagator:     otel.GetTextMapPropagator(),
}))
```

## Config Defaults

- `TracerProvider`: `otel.GetTracerProvider()`.
- `Propagator`: `otel.GetTextMapPropagator()`.
- `Skipper`: Echo `middleware.DefaultSkipper`.
- `BodySkipper`: no-op, returns `false, false`.
- `AreHeadersDump`: `true`.
- `IsBodyDump`: `false`.
- `RemoveNewLines`: `false`.
- `LimitNameSize` and `LimitValueSize`: `0`, meaning unlimited.

## Safe Body Dumping

- Body dumping is opt-in with `IsBodyDump: true` because it can capture PII, tokens, and secrets.
- Prefer `BodySkipper` to exclude sensitive endpoints, compressed bodies, uploads, auth payloads, or large responses.
- `BodySkipper` only runs when `IsBodyDump` is true and returns `(skipReqBody, skipRespBody)`.

```go
BodySkipper: func(c *echo.Context) (bool, bool) {
	if c.Request().Header.Get("Content-Encoding") == "gzip" {
		return true, true
	}
	return false, false
},
```

## Attribute Limits

- `LimitNameSize` and `LimitValueSize` are byte-based, not rune-based.
- Truncation preserves valid UTF-8; values over a limit greater than 10 get a trailing `...`.
- `RemoveNewLines` replaces `\n` with spaces in string attributes, useful for backends such as Sentry.

## Do Not Assume

- Metrics are not implemented in this middleware; do not add or document metrics unless explicitly requested.
- Echo v4 examples are wrong for this package because context types and imports differ.
