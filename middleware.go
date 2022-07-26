package echo_otel_middleware

import (
	"bytes"
	"io/ioutil"

	"github.com/labstack/echo/v4"
	"github.com/labstack/echo/v4/middleware"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/propagation"
	semconv "go.opentelemetry.io/otel/semconv/v1.10.0"
	oteltrace "go.opentelemetry.io/otel/trace"
)

const (
	tracerKey  = "echo-otel-middleware"
	tracerName = "github.com/adlandh/echo-otel-middleware"
)

type (
	// TraceConfig defines the config for Trace middleware.
	OtelConfig struct {
		// Skipper defines a function to skip middleware.
		Skipper middleware.Skipper

		// OpenTelemetry TracerProvider
		TracerProvider oteltrace.TracerProvider

		// OpenTelemetry Propagator
		Propagator propagation.TextMapPropagator

		// add req headers & resp headers to tracing tags
		AreHeadersDump bool

		// add req body & resp body to attributes
		IsBodyDump bool

		// prevent logging long http request bodies
		LimitHTTPBody bool

		// http body limit size (in bytes)
		// NOTE: don't specify values larger than 60000 as jaeger can't handle values in span.LogKV larger than 60000 bytes
		LimitSize int
	}
)

var (
	// DefaultTraceConfig is the default Trace middleware config.
	DefaultOtelConfig = OtelConfig{
		Skipper:        middleware.DefaultSkipper,
		AreHeadersDump: true,
		IsBodyDump:     false,
		LimitHTTPBody:  true,
		LimitSize:      60_000,
	}
)

//Middleware returns a OpenTelemetry middleware with default config
func Middleware() echo.MiddlewareFunc {
	return MiddlewareWithConfig(DefaultOtelConfig)
}

// MiddlewareWithConfig returns a OpenTelemetry middleware with config.
func MiddlewareWithConfig(config OtelConfig) echo.MiddlewareFunc {
	if config.TracerProvider == nil {
		config.TracerProvider = otel.GetTracerProvider()
	}
	tracer := config.TracerProvider.Tracer(tracerName)

	if config.Propagator == nil {
		config.Propagator = otel.GetTextMapPropagator()
	}

	if config.Skipper == nil {
		config.Skipper = middleware.DefaultSkipper
	}

	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			if config.Skipper(c) {
				return next(c)
			}

			c.Set(tracerKey, tracer)
			request := c.Request()
			savedCtx := request.Context()
			defer func() {
				request = request.WithContext(savedCtx)
				c.SetRequest(request)
			}()
			opname := "HTTP " + request.Method + " URL: " + c.Path()
			realIP := c.RealIP()
			requestID := getRequestID(c) // request-id generated by reverse-proxy

			var sp oteltrace.Span
			var err error

			ctx := config.Propagator.Extract(savedCtx, propagation.HeaderCarrier(request.Header))
			opts := []oteltrace.SpanStartOption{
				oteltrace.WithAttributes(semconv.NetAttributesFromHTTPRequest("tcp", request)...),
				oteltrace.WithAttributes(semconv.EndUserAttributesFromHTTPRequest(request)...),
				oteltrace.WithAttributes(semconv.HTTPServerAttributesFromHTTPRequest("", c.Path(), request)...),
				oteltrace.WithSpanKind(oteltrace.SpanKindServer),
				oteltrace.WithAttributes(attribute.String("client_ip", realIP), attribute.String("request_id", requestID)),
			}
			ctx, sp = tracer.Start(ctx, opname, opts...)
			defer sp.End()

			//Dump request headers
			if config.AreHeadersDump {
				for k := range request.Header {
					sp.SetAttributes(attribute.String("http.req.header."+k, request.Header.Get(k)))
				}
			}

			// Dump request & response body
			var respDumper *responseDumper
			if config.IsBodyDump {
				// request
				reqBody := []byte{}
				if c.Request().Body != nil {
					reqBody, _ = ioutil.ReadAll(c.Request().Body)

					if config.LimitHTTPBody {
						sp.SetAttributes(attribute.String("http.req.body", limitString(string(reqBody), config.LimitSize)))
					} else {
						sp.SetAttributes(attribute.String("http.req.body", string(reqBody)))
					}
				}

				request.Body = ioutil.NopCloser(bytes.NewBuffer(reqBody)) // reset original request body

				// response
				respDumper = newResponseDumper(c.Response())
				c.Response().Writer = respDumper
			}

			// setup request context - add opentracing span
			c.SetRequest(request.WithContext(ctx))

			// call next middleware / controller
			err = next(c)
			if err != nil {
				sp.SetAttributes(attribute.String("echo.error", err.Error()))
				c.Error(err) // call custom registered error handler
			}

			attrs := semconv.HTTPAttributesFromHTTPStatusCode(c.Response().Status)
			spanStatus, spanMessage := semconv.SpanStatusFromHTTPStatusCodeAndSpanKind(c.Response().Status, oteltrace.SpanKindServer)
			sp.SetAttributes(attrs...)
			sp.SetStatus(spanStatus, spanMessage)

			//Dump response headers
			if config.AreHeadersDump {
				for k := range c.Response().Header() {
					sp.SetAttributes(attribute.String("http.resp.header."+k, c.Response().Header().Get(k)))
				}
			}

			// Dump response body
			if config.IsBodyDump {
				if config.LimitHTTPBody {
					sp.SetAttributes(attribute.String("http.resp.body", limitString(respDumper.GetResponse(), config.LimitSize)))
				} else {
					sp.SetAttributes(attribute.String("http.resp.body", respDumper.GetResponse()))
				}
			}

			return nil // error was already processed with ctx.Error(err)
		}
	}
}
