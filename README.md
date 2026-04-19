# Echo OTel Middleware

[![Go Reference](https://pkg.go.dev/badge/github.com/adlandh/echo-otel-middleware/v2.svg)](https://pkg.go.dev/github.com/adlandh/echo-otel-middleware/v2)
[![Go Report Card](https://goreportcard.com/badge/github.com/adlandh/echo-otel-middleware/v2)](https://goreportcard.com/report/github.com/adlandh/echo-otel-middleware/v2)

Echo OpenTelemetry middleware based on Jaeger tracing middleware

## Usage:

```shell
go get github.com/adlandh/echo-otel-middleware/v2
```

In your app:

```go
package main

import (
	"context"
	"log"
	"net/http"

	echootelmiddleware "github.com/adlandh/echo-otel-middleware/v2"
	"github.com/labstack/echo/v5"
	"go.opentelemetry.io/otel"
	stdout "go.opentelemetry.io/otel/exporters/stdout/stdouttrace"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

func main() {
	tp, err := initTracer()
	if err != nil {
		log.Fatal(err)
	}
	defer func() {
		if err := tp.Shutdown(context.Background()); err != nil {
			log.Printf("Error shutting down tracer provider: %v", err)
		}
	}()

	app := echo.New()

	app.Use(echootelmiddleware.MiddlewareWithConfig(echootelmiddleware.OtelConfig{
		TracerProvider: tp,
		AreHeadersDump: true, // dump request && response headers
		IsBodyDump:     true, // dump request && response body
		// No dump for gzip
		BodySkipper: func(c *echo.Context) (bool, bool) {
			if c.Request().Header.Get("Content-Encoding") == "gzip" {
				return true, true
			}
			return false, false
		},
	}))

	// Add some endpoints
	app.POST("/", func(c *echo.Context) error {
		return c.String(http.StatusOK, "Hello, World!")
	})

	app.GET("/", func(c *echo.Context) error {
		return c.String(http.StatusOK, "Hello, World!")
	})

	// And run it
	app.Logger.Fatal(app.Start(":3000"))
}

func initTracer() (*sdktrace.TracerProvider, error) {
	exporter, err := stdout.New(stdout.WithPrettyPrint())
	if err != nil {
		return nil, err
	}
	tp := sdktrace.NewTracerProvider(
		sdktrace.WithSampler(sdktrace.AlwaysSample()),
		sdktrace.WithBatcher(exporter),
	)
	otel.SetTracerProvider(tp)
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(propagation.TraceContext{}, propagation.Baggage{}))
	return tp, nil
}
```

## Options

- `TracerProvider` (default: `otel.GetTracerProvider()`): OpenTelemetry tracer provider.
- `Propagator` (default: `otel.GetTextMapPropagator()`): text map propagator used to extract the parent context from request headers.
- `Skipper` (default: `middleware.DefaultSkipper`): function to skip the middleware entirely for a request.
- `BodySkipper` (default: no-op): function `func(*echo.Context) (skipReqBody, skipRespBody bool)` to exclude request and/or response bodies per request. Only consulted when `IsBodyDump` is true.
- `AreHeadersDump` (default: true): include request/response headers in span attributes.
- `IsBodyDump` (default: false): include request/response bodies in span attributes.
- `RemoveNewLines` (default: false): replace `\n` with spaces in string attribute values (useful for Sentry).
- `LimitNameSize` (default: 0): max attribute name length in bytes; `<=0` means unlimited. Sentry caps at 32.
- `LimitValueSize` (default: 0): max attribute value length in bytes; `<=0` means unlimited. Values longer than the limit are truncated with a trailing `...` when the limit is greater than 10. Sentry caps at 200.

## Security

Dumping headers or bodies can capture PII or secrets. Prefer `BodySkipper` to exclude sensitive endpoints or payloads.
