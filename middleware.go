// Package echootelmiddleware is a middleware for OpenTelemetry for echo.
package echootelmiddleware

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"

	"github.com/adlandh/response-dumper"
	"github.com/labstack/echo/v5"
	"github.com/labstack/echo/v5/middleware"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/propagation"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
	oteltrace "go.opentelemetry.io/otel/trace"
)

const (
	tracerKey  = "echo-otel-middleware"
	tracerName = "github.com/adlandh/echo-otel-middleware/v2"
)

type BodySkipper func(*echo.Context) (skipReqBody bool, skipRespBody bool)

type (
	// OtelConfig defines the config for OpenTelemetry middleware.
	OtelConfig struct {
		// Skipper defines a function to skip middleware.
		Skipper middleware.Skipper

		// BodySkipper defines a function to exclude body from logging
		BodySkipper BodySkipper

		// OpenTelemetry TracerProvider
		TracerProvider oteltrace.TracerProvider

		// OpenTelemetry Propagator
		Propagator propagation.TextMapPropagator

		// add req headers & resp headers to tracing tags
		AreHeadersDump bool

		// add req body & resp body to attributes
		IsBodyDump bool

		// remove \\n from values (necessary for sentry)
		RemoveNewLines bool

		// Tag name limit size. <=0 for unlimited, for sentry use 32
		LimitNameSize int

		// Tag value limit size (in bytes). <=0 for unlimited, for sentry use 200
		LimitValueSize int
	}
)

var (
	// DefaultOtelConfig is the default OpenTelemetry middleware config.
	DefaultOtelConfig = OtelConfig{
		Skipper:        middleware.DefaultSkipper,
		BodySkipper:    defaultBodySkipper,
		AreHeadersDump: true,
		IsBodyDump:     false,
	}
)

// shouldSkipMiddleware determines if the middleware should be skipped.
func shouldSkipMiddleware(c *echo.Context, config OtelConfig) bool {
	return config.Skipper(c) || c.Request() == nil || c.Response() == nil
}

// processNextHandler calls the next handler and processes any errors.
func processNextHandler(c *echo.Context, next echo.HandlerFunc, config OtelConfig, span oteltrace.Span) error {
	err := next(c)
	if err != nil {
		// Record error in span
		span.RecordError(err)
		setAttr(span, config, attribute.String("echo.error", err.Error()))

		// Call custom registered error handler
		if c.Echo() != nil {
			c.Echo().HTTPErrorHandler(c, err)
		}
	}

	return err
}

// addPathParameters adds path parameters to the span.
func addPathParameters(c *echo.Context, config OtelConfig, span oteltrace.Span) {
	if path := c.Path(); path != "" {
		setAttr(span, config, semconv.HTTPRoute(path))
	}

	for _, paramName := range c.RouteInfo().Parameters {
		setAttr(span, config, attribute.String("http.path."+paramName, c.Param(paramName)))
	}
}

// dumpRequestBody reads and dumps the request body to the span.
// It returns the request with the body reset for further processing.
func dumpRequestBody(request *http.Request, config OtelConfig, span oteltrace.Span, skipReqBody bool) {
	if request.Body == nil {
		return
	}

	reqBody := []byte("[excluded]")

	if !skipReqBody {
		var err error

		reqBody, err = io.ReadAll(request.Body)
		if err == nil {
			_ = request.Body.Close()
			request.Body = io.NopCloser(bytes.NewBuffer(reqBody)) // reset original request body
		}
	}

	setAttr(span, config, attribute.String("http.request.body", string(reqBody)))
}

// setupResponseDumper creates and sets up a response dumper.
func setupResponseDumper(c *echo.Context) *response.Dumper {
	respDumper := response.NewDumper(c.Response())
	c.SetResponse(respDumper)

	return respDumper
}

// dumpReq processes the request for tracing, adding path parameters, headers, and body to the span.
// It returns a response dumper if body dumping is enabled.
func dumpReq(c *echo.Context, config OtelConfig, span oteltrace.Span, request *http.Request, skipReqBody bool) *response.Dumper {
	// Add path parameters
	addPathParameters(c, config, span)

	// Dump request headers
	if config.AreHeadersDump {
		setAttr(span, config, dumpHeaders("http.request.headers", request.Header)...)
	}

	// Dump request & response body
	var respDumper *response.Dumper

	if config.IsBodyDump {
		// Dump request body
		dumpRequestBody(request, config, span, skipReqBody)

		// Setup response dumper
		respDumper = setupResponseDumper(c)
	}

	return respDumper
}

// setSpanStatus sets the span status based on the HTTP status code.
func setSpanStatus(span oteltrace.Span, status int) {
	switch {
	case status >= 400:
		span.SetStatus(codes.Error, "")
	case status >= 200:
		span.SetStatus(codes.Ok, "")
	default:
		span.SetStatus(codes.Unset, "")
	}
}

// dumpResponseHeaders dumps the response headers to the span.
func dumpResponseHeaders(c *echo.Context, config OtelConfig, span oteltrace.Span) {
	if config.AreHeadersDump {
		setAttr(span, config, dumpHeaders("http.response.headers", c.Response().Header())...)
	}
}

// dumpResponseBody dumps the response body to the span.
func dumpResponseBody(respDumper *response.Dumper, config OtelConfig, span oteltrace.Span, skipRespBody bool) {
	respBody := respDumper.GetResponse()

	if respBody != "" && skipRespBody {
		respBody = "[excluded]"
	}

	setAttr(span, config, attribute.String("http.response.body", respBody))
}

// dumpResp processes the response for tracing, adding status, headers, and body to the span.
func dumpResp(c *echo.Context, config OtelConfig, span oteltrace.Span, respDumper *response.Dumper, err error, skipRespBody bool) {
	status := responseStatus(c, respDumper, err)

	// Set span status based on HTTP status code
	setSpanStatus(span, status)

	// Add status code attribute if available
	if status > 0 {
		setAttr(span, config, semconv.HTTPResponseStatusCode(status))
	}

	// Dump response headers
	dumpResponseHeaders(c, config, span)

	// Dump response body
	if config.IsBodyDump && respDumper != nil {
		dumpResponseBody(respDumper, config, span, skipRespBody)
	}
}

// createSpanName creates a span name based on the HTTP method, path, and request URI.
func createSpanName(request *http.Request, path string) string {
	opName := "HTTP " + request.Method + " URL: " + path
	if path != request.RequestURI {
		opName = opName + " URI: " + request.RequestURI
	}

	return opName
}

// createSpanOptions creates span options with common HTTP attributes.
func createSpanOptions(request *http.Request, realIP, requestID string) []oteltrace.SpanStartOption {
	return []oteltrace.SpanStartOption{
		oteltrace.WithSpanKind(oteltrace.SpanKindServer),
		oteltrace.WithAttributes(
			attribute.String("client_ip", realIP),
			attribute.String("request_id", requestID),
			attribute.String("user_agent", request.UserAgent()),
			attribute.String("http.method", request.Method),
			attribute.String("http.proto", request.Proto),
			attribute.String("http.host", request.Host),
			attribute.String("http.scheme", request.URL.Scheme),
		),
	}
}

// createSpan creates a new span for the request and returns the request, span, context, and a cleanup function.
func createSpan(c *echo.Context, config OtelConfig) (*http.Request, oteltrace.Span, context.Context, func()) {
	tracer := config.TracerProvider.Tracer(tracerName)
	c.Set(tracerKey, tracer)

	request := c.Request()
	savedCtx := request.Context()

	// Ensure request.URL is not nil
	if request.URL == nil {
		request.URL = &url.URL{}
	}

	realIP := c.RealIP()
	requestID := getRequestID(c)

	// Extract propagated context
	ctx := config.Propagator.Extract(savedCtx, propagation.HeaderCarrier(request.Header))

	// Create span
	opName := createSpanName(request, c.Path())
	opts := createSpanOptions(request, realIP, requestID)
	ctx, span := tracer.Start(ctx, opName, opts...)

	// Return cleanup function
	return request, span, ctx, func() {
		request = request.WithContext(savedCtx)
		c.SetRequest(request)
		span.End()
	}
}

func setDefaultValues(config *OtelConfig) {
	if config.TracerProvider == nil {
		config.TracerProvider = otel.GetTracerProvider()
	}

	if config.Propagator == nil {
		config.Propagator = otel.GetTextMapPropagator()
	}

	if config.Skipper == nil {
		config.Skipper = middleware.DefaultSkipper
	}

	if config.BodySkipper == nil {
		config.BodySkipper = defaultBodySkipper
	}
}

func responseStatus(c *echo.Context, respDumper *response.Dumper, err error) int {
	if respDumper != nil {
		return respDumper.StatusCode()
	}

	resp, unwrapErr := echo.UnwrapResponse(c.Response())
	if unwrapErr == nil && resp != nil {
		return resp.Status
	}

	if err != nil {
		var hsc echo.HTTPStatusCoder
		if errors.As(err, &hsc) {
			return hsc.StatusCode()
		}
	}

	return 0
}

func dumpHeaders(prefix string, h http.Header) []attribute.KeyValue {
	attrs := make([]attribute.KeyValue, 0, len(h))
	for k, v := range h {
		attrs = append(attrs, formatKey(k, prefix).StringSlice(v))
	}

	return attrs
}

// Middleware returns a OpenTelemetry middleware with default config
func Middleware() echo.MiddlewareFunc {
	return MiddlewareWithConfig(DefaultOtelConfig)
}

// MiddlewareWithConfig returns a OpenTelemetry middleware with config.
func MiddlewareWithConfig(config OtelConfig) echo.MiddlewareFunc {
	// Ensure default values are set
	setDefaultValues(&config)

	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c *echo.Context) error {
			// Skip middleware if necessary
			if shouldSkipMiddleware(c, config) {
				return next(c)
			}

			// Create span for the request
			request, span, ctx, endSpan := createSpan(c, config)
			defer endSpan()

			// Skip attribute/body/header processing if span is not recording.
			if !span.IsRecording() {
				c.SetRequest(request.WithContext(ctx))

				err := next(c)
				if err != nil {
					if c.Echo() != nil {
						c.Echo().HTTPErrorHandler(c, err)
					}
				}

				return err
			}

			// Determine if request/response bodies should be skipped
			skipReqBody := false
			skipRespBody := false

			if config.IsBodyDump {
				skipReqBody, skipRespBody = config.BodySkipper(c)
			}

			// Process request for tracing
			respDumper := dumpReq(c, config, span, request, skipReqBody)

			// Setup request context with the span
			c.SetRequest(request.WithContext(ctx))

			// Call next middleware/controller and handle errors
			err := processNextHandler(c, next, config, span)

			// Process response for tracing
			dumpResp(c, config, span, respDumper, err, skipRespBody)

			return err
		}
	}
}
