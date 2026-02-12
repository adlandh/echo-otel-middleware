# Echo OTel Middleware

[![Go Reference](https://pkg.go.dev/badge/github.com/adlandh/echo-otel-middleware.svg)](https://pkg.go.dev/github.com/adlandh/echo-otel-middleware)
[![Go Report Card](https://goreportcard.com/badge/github.com/adlandh/echo-otel-middleware)](https://goreportcard.com/report/github.com/adlandh/echo-otel-middleware)

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

- `AreHeadersDump` (default: true): include request/response headers in span attributes.
- `IsBodyDump` (default: false): include request/response bodies in span attributes.
- `RemoveNewLines` (default: false): replace `\n` with spaces in attribute values.
- `LimitNameSize` (default: 0): max tag name length in bytes, `<=0` means unlimited.
- `LimitValueSize` (default: 0): max tag value length in bytes, `<=0` means unlimited.

## Security

Dumping headers or bodies can capture PII or secrets. Prefer `BodySkipper` to exclude sensitive endpoints or payloads.
