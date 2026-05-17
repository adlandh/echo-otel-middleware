// Package echootelmiddleware is a middleware for OpenTelemetry for echo.
package echootelmiddleware

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

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

// HeaderSkipper reports whether the named header should be omitted from span
// attributes. The name is passed in its canonical (MIME) form.
type HeaderSkipper func(name string) bool

type (
	// OtelConfig defines the config for OpenTelemetry middleware.
	OtelConfig struct {
		// Skipper defines a function to skip middleware.
		Skipper middleware.Skipper

		// BodySkipper defines a function to exclude body from logging
		BodySkipper BodySkipper

		// HeaderSkipper redacts sensitive headers from span attributes.
		// The default denies Authorization, Cookie, Set-Cookie,
		// Proxy-Authorization, and X-Api-Key.
		HeaderSkipper HeaderSkipper

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

		// MaxBodyDumpSize caps the number of bytes buffered from the request or
		// response body when IsBodyDump is enabled. Set to <0 for unlimited
		// (unsafe: a large upload can exhaust memory). Default is 64 KiB.
		MaxBodyDumpSize int64
	}
)

const defaultMaxBodyDumpSize int64 = 64 * 1024

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

// processNextHandler calls the next handler and records any error on the span.
// The error is returned unchanged so Echo's router invokes HTTPErrorHandler
// exactly once.
func processNextHandler(c *echo.Context, next echo.HandlerFunc, config OtelConfig, span oteltrace.Span) error {
	err := next(c)
	if err != nil {
		span.RecordError(err)
		setAttr(span, config, attribute.String("echo.error", err.Error()))
	}

	return err
}

// addPathParameters adds the matched route and any path parameters to the
// span in a single SetAttributes call.
func addPathParameters(c *echo.Context, config OtelConfig, span oteltrace.Span) {
	params := c.RouteInfo().Parameters
	path := c.Path()

	if path == "" && len(params) == 0 {
		return
	}

	attrs := make([]attribute.KeyValue, 0, len(params)+1)
	if path != "" {
		attrs = append(attrs, semconv.HTTPRoute(path))
	}

	for _, paramName := range params {
		attrs = append(attrs, attribute.String("http.path."+paramName, c.Param(paramName)))
	}

	setAttr(span, config, attrs...)
}

// teeReadCloser preserves the original body's Close while reading from a
// MultiReader. Used when the request body exceeds MaxBodyDumpSize so the
// handler can still consume the remaining bytes.
type teeReadCloser struct {
	io.Reader
	io.Closer
}

// dumpRequestBody reads (up to MaxBodyDumpSize bytes of) the request body and
// attaches it to the span. The original body is reset so the handler still
// sees the full payload.
func dumpRequestBody(request *http.Request, config OtelConfig, span oteltrace.Span, skipReqBody bool) {
	if request.Body == nil {
		return
	}

	if skipReqBody {
		setAttr(span, config, attribute.String("http.request.body", "[excluded]"))
		return
	}

	origBody := request.Body
	maxSize := config.MaxBodyDumpSize

	var (
		buf       []byte
		err       error
		truncated bool
	)

	if maxSize > 0 {
		// Read one extra byte to detect truncation.
		buf, err = io.ReadAll(io.LimitReader(origBody, maxSize+1))
		if err == nil && int64(len(buf)) > maxSize {
			truncated = true
			dumped := buf[:maxSize]
			// Hand the handler a stream of: already-read bytes + remaining body.
			request.Body = teeReadCloser{
				Reader: io.MultiReader(bytes.NewReader(buf), origBody),
				Closer: origBody,
			}
			buf = dumped
		} else if err == nil {
			_ = origBody.Close()
			request.Body = io.NopCloser(bytes.NewReader(buf))
		}
	} else {
		buf, err = io.ReadAll(origBody)
		if err == nil {
			_ = origBody.Close()
			request.Body = io.NopCloser(bytes.NewReader(buf))
		}
	}

	if err != nil {
		span.RecordError(err)
		setAttr(span, config, attribute.String("http.request.body", "[read-error]"))

		return
	}

	body := strings.ToValidUTF8(string(buf), "")
	if truncated {
		body += "[truncated]"
	}

	setAttr(span, config, attribute.String("http.request.body", body))
}

// setupResponseDumper creates and sets up a response dumper.
func setupResponseDumper(c *echo.Context) *response.Dumper {
	respDumper := response.NewDumper(c.Response())
	c.SetResponse(respDumper)

	return respDumper
}

// dumpReq processes the request for tracing, adding path parameters, headers, and body to the span.
// It returns a response dumper if body dumping is enabled.
func dumpReq(c *echo.Context, config OtelConfig, span oteltrace.Span, request *http.Request, skipReqBody, skipRespBody bool) *response.Dumper {
	// Add path parameters
	addPathParameters(c, config, span)

	// Dump request headers
	if config.AreHeadersDump {
		setAttr(span, config, dumpHeaders("http.request.headers", request.Header, config.HeaderSkipper)...)
	}

	// Dump request & response body
	var respDumper *response.Dumper

	if config.IsBodyDump {
		// Dump request body
		dumpRequestBody(request, config, span, skipReqBody)

		// Only install the response dumper if we plan to use it; otherwise the
		// response is buffered for the full request lifetime for nothing.
		if !skipRespBody {
			respDumper = setupResponseDumper(c)
		}
	}

	return respDumper
}

// setSpanStatus sets the span status based on the HTTP status code, attaching
// a human-readable message for error statuses.
func setSpanStatus(span oteltrace.Span, status int, err error) {
	switch {
	case status >= 400:
		msg := http.StatusText(status)
		if err != nil {
			msg = err.Error()
		}

		span.SetStatus(codes.Error, msg)
	case status >= 200:
		span.SetStatus(codes.Ok, "")
	}
}

// dumpResponseHeaders dumps the response headers to the span.
func dumpResponseHeaders(c *echo.Context, config OtelConfig, span oteltrace.Span) {
	if config.AreHeadersDump {
		setAttr(span, config, dumpHeaders("http.response.headers", c.Response().Header(), config.HeaderSkipper)...)
	}
}

// dumpResponseBody dumps the response body to the span.
func dumpResponseBody(c *echo.Context, respDumper *response.Dumper, config OtelConfig, span oteltrace.Span, skipRespBody bool) {
	var respBody string

	switch {
	case skipRespBody:
		respBody = "[excluded]"
	case !isTextualContentType(c.Response().Header().Get(echo.HeaderContentType)):
		respBody = "[non-text content]"
	default:
		respBody = strings.ToValidUTF8(respDumper.GetResponse(), "")
		if config.MaxBodyDumpSize > 0 && int64(len(respBody)) > config.MaxBodyDumpSize {
			respBody = respBody[:config.MaxBodyDumpSize] + "[truncated]"
		}
	}

	setAttr(span, config, attribute.String("http.response.body", respBody))
}

// dumpResp processes the response for tracing, adding status, headers, and body to the span.
func dumpResp(c *echo.Context, config OtelConfig, span oteltrace.Span, respDumper *response.Dumper, err error, skipRespBody bool) {
	status := responseStatus(c, respDumper, err)

	// Set span status based on HTTP status code
	setSpanStatus(span, status, err)

	// Add status code attribute if available
	if status > 0 {
		setAttr(span, config, semconv.HTTPResponseStatusCode(status))
	}

	// Dump response headers
	dumpResponseHeaders(c, config, span)

	// Dump response body. When the response was skipped up-front we never
	// installed a dumper, but still emit the marker attribute for parity with
	// the request side.
	if config.IsBodyDump {
		switch {
		case respDumper != nil:
			dumpResponseBody(c, respDumper, config, span, skipRespBody)
		case skipRespBody:
			setAttr(span, config, attribute.String("http.response.body", "[excluded]"))
		}
	}
}

// createSpanName builds an OTel-conformant span name: "{METHOD} {route}".
// Falls back to "HTTP {METHOD}" when the route is unknown (e.g. not matched
// by the router). The raw request URI is never included to avoid leaking
// query parameters and inflating cardinality.
func createSpanName(request *http.Request, path string) string {
	if path == "" {
		return "HTTP " + request.Method
	}

	return request.Method + " " + path
}

// createSpanOptions creates span options with common HTTP attributes using
// current OpenTelemetry semantic conventions.
func createSpanOptions(request *http.Request, realIP, requestID string) []oteltrace.SpanStartOption {
	attrs := []attribute.KeyValue{
		semconv.HTTPRequestMethodKey.String(request.Method),
		semconv.URLScheme(request.URL.Scheme),
		semconv.ServerAddress(request.Host),
		semconv.UserAgentOriginal(request.UserAgent()),
	}

	if realIP != "" {
		attrs = append(attrs, semconv.ClientAddress(realIP))
	}

	if requestID != "" {
		attrs = append(attrs, attribute.String("http.request.id", requestID))
	}

	if name, version := splitProto(request.Proto); name != "" {
		attrs = append(attrs, semconv.NetworkProtocolName(name))
		if version != "" {
			attrs = append(attrs, semconv.NetworkProtocolVersion(version))
		}
	}

	return []oteltrace.SpanStartOption{
		oteltrace.WithSpanKind(oteltrace.SpanKindServer),
		oteltrace.WithAttributes(attrs...),
	}
}

// splitProto splits e.g. "HTTP/1.1" into ("http", "1.1").
func splitProto(proto string) (name, version string) {
	if i := strings.Index(proto, "/"); i >= 0 {
		return strings.ToLower(proto[:i]), proto[i+1:]
	}

	return strings.ToLower(proto), ""
}

// createSpan creates a new span for the request and returns the request, span, context, and a cleanup function.
func createSpan(c *echo.Context, config OtelConfig) (*http.Request, oteltrace.Span, context.Context, func()) {
	tracer := config.TracerProvider.Tracer(tracerName)
	c.Set(tracerKey, tracer)

	request := c.Request()
	savedCtx := request.Context()
	origResp := c.Response()

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

	// Return cleanup function: restore the original request/response so any
	// outer middleware sees the values it handed us, then end the span.
	return request, span, ctx, func() {
		request = request.WithContext(savedCtx)
		c.SetRequest(request)
		c.SetResponse(origResp)
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

	if config.HeaderSkipper == nil {
		config.HeaderSkipper = defaultHeaderSkipper
	}

	if config.MaxBodyDumpSize == 0 {
		config.MaxBodyDumpSize = defaultMaxBodyDumpSize
	}
}

func responseStatus(c *echo.Context, respDumper *response.Dumper, err error) int {
	if respDumper != nil {
		return respDumper.StatusCode()
	}

	// On handler error the response has not yet been written (Echo's
	// HTTPErrorHandler runs after this middleware returns), so prefer the
	// error's HTTPStatusCoder. Generic errors map to 500.
	if err != nil {
		var hsc echo.HTTPStatusCoder
		if errors.As(err, &hsc) {
			return hsc.StatusCode()
		}

		return http.StatusInternalServerError
	}

	resp, unwrapErr := echo.UnwrapResponse(c.Response())
	if unwrapErr == nil && resp != nil {
		return resp.Status
	}

	return 0
}

func dumpHeaders(prefix string, h http.Header, skip HeaderSkipper) []attribute.KeyValue {
	attrs := make([]attribute.KeyValue, 0, len(h))

	for k, v := range h {
		if skip != nil && skip(k) {
			attrs = append(attrs, formatKey(k, prefix).String("[redacted]"))
			continue
		}

		attrs = append(attrs, formatKey(k, prefix).StringSlice(v))
	}

	return attrs
}

// recordPanic attaches a recovered panic value to the span as an error event
// and sets the span status to Error.
func recordPanic(span oteltrace.Span, r any) {
	var err error
	if e, ok := r.(error); ok {
		err = e
	} else {
		err = fmt.Errorf("panic: %v", r)
	}

	span.RecordError(err, oteltrace.WithStackTrace(true))
	span.SetStatus(codes.Error, err.Error())
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

			// Record panics on the span and re-panic so upstream recovery
			// middleware (Echo's Recover, etc.) still works.
			defer func() {
				if r := recover(); r != nil {
					recordPanic(span, r)
					panic(r)
				}
			}()

			// Skip attribute/body/header processing if span is not recording.
			if !span.IsRecording() {
				c.SetRequest(request.WithContext(ctx))

				return next(c)
			}

			// Determine if request/response bodies should be skipped
			skipReqBody := false
			skipRespBody := false

			if config.IsBodyDump {
				skipReqBody, skipRespBody = config.BodySkipper(c)
			}

			// Process request for tracing
			respDumper := dumpReq(c, config, span, request, skipReqBody, skipRespBody)

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
