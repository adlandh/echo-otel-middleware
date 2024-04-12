# echo-otel-middleware
Echo OpenTelemetry middleware based on Jaeger tracing middleware

## Usage:

```shell
go get github.com/adlandh/echo-otel-middleware
```

In your app:

```go
package main

import (
	"context"
	"log"
	"net/http"

	"github.com/adlandh/echo-otel-middleware"
	"github.com/labstack/echo/v4"
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
	}))

	// Add some endpoints
	app.POST("/", func(c echo.Context) error {
		return c.String(http.StatusOK, "Hello, World!")
	})

	app.GET("/", func(c echo.Context) error {
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
