// Package echootelmiddleware is a middleware for OpenTelemetry for echo.
package echootelmiddleware

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/adlandh/response-dumper"
	"github.com/labstack/echo/v4"
	"github.com/labstack/echo/v4/middleware"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/propagation"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
	oteltrace "go.opentelemetry.io/otel/trace"
)

const (
	tracerKey  = "echo-otel-middleware"
	tracerName = "github.com/adlandh/echo-otel-middleware"
)

var key = func(k, prefix string) attribute.Key {
	k = strings.ToLower(k)
	k = strings.ReplaceAll(k, "-", "_")
	k = fmt.Sprintf("%s.%s", prefix, k)

	return attribute.Key(k)
}

type BodySkipper func(echo.Context) (skipReqBody bool, skipRespBody bool)

func defaultBodySkipper(echo.Context) (skipReqBody bool, skipRespBody bool) {
	return
}

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

// Middleware returns a OpenTelemetry middleware with default config
func Middleware() echo.MiddlewareFunc {
	return MiddlewareWithConfig(DefaultOtelConfig)
}

// MiddlewareWithConfig returns a OpenTelemetry middleware with config.
func MiddlewareWithConfig(config OtelConfig) echo.MiddlewareFunc {
	var err error

	setDefaultValues(&config)

	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			if config.Skipper(c) || c.Request() == nil || c.Response() == nil {
				return next(c)
			}

			request, span, ctx, endSpan := createSpan(c, config)
			defer endSpan()

			skipReqBody, skipRespBody := config.BodySkipper(c)

			respDumper := dumpReq(c, config, span, request, skipReqBody)

			// setup request context - add opentracing span
			c.SetRequest(request.WithContext(ctx))

			// call next middleware / controller
			err = next(c)
			if err != nil {
				span.RecordError(err)
				setAttr(span, config, attribute.String("echo.error", err.Error()))
				c.Error(err) // call custom registered error handler
			}

			dumpResp(c, config, span, respDumper, skipRespBody)

			return err
		}
	}
}

func dumpReq(c echo.Context, config OtelConfig, span oteltrace.Span, request *http.Request, skipReqBody bool) *response.Dumper {
	// Add path parameters
	if path := c.Path(); path != "" {
		setAttr(span, config, semconv.HTTPRoute(path))
	}

	for _, paramName := range c.ParamNames() {
		setAttr(span, config, attribute.String("http.path."+paramName, c.Param(paramName)))
	}

	// Dump request headers
	if config.AreHeadersDump {
		setAttr(span, config, dumpHeaders("http.request.headers", request.Header)...)
	}

	// Dump request & response body
	var respDumper *response.Dumper

	if config.IsBodyDump {
		// request
		if request.Body != nil {
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

		// response
		respDumper = response.NewDumper(c.Response().Writer)
		c.Response().Writer = respDumper
	}

	return respDumper
}

func dumpResp(c echo.Context, config OtelConfig, span oteltrace.Span, respDumper *response.Dumper, skipRespBody bool) {
	status := c.Response().Status

	switch {
	case status >= 400:
		span.SetStatus(codes.Error, "")
	case status >= 200:
		span.SetStatus(codes.Ok, "")
	default:
		span.SetStatus(codes.Unset, "")
	}

	if status > 0 {
		setAttr(span, config, semconv.HTTPResponseStatusCode(status))
	}

	// Dump response headers
	if config.AreHeadersDump {
		setAttr(span, config, dumpHeaders("http.response.headers", c.Response().Header())...)
	}

	// Dump response body
	if config.IsBodyDump {
		respBody := respDumper.GetResponse()

		if respBody != "" && skipRespBody {
			respBody = "[excluded]"
		}

		setAttr(span, config, attribute.String("http.response.body", respBody))
	}
}

func createSpan(c echo.Context, config OtelConfig) (*http.Request, oteltrace.Span, context.Context, func()) {
	tracer := config.TracerProvider.Tracer(tracerName)
	c.Set(tracerKey, tracer)

	request := c.Request()
	savedCtx := request.Context()

	opName := "HTTP " + request.Method + " URL: " + c.Path()
	if c.Path() != request.RequestURI {
		opName = opName + " URI: " + request.RequestURI
	}

	realIP := c.RealIP()
	requestID := getRequestID(c) // request-id generated by reverse-proxy

	var span oteltrace.Span

	if request.URL == nil {
		request.URL = &url.URL{}
	}

	ctx := config.Propagator.Extract(savedCtx, propagation.HeaderCarrier(request.Header))
	opts := []oteltrace.SpanStartOption{
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
	ctx, span = tracer.Start(ctx, opName, opts...)

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

func dumpHeaders(prefix string, h http.Header) []attribute.KeyValue {
	attrs := make([]attribute.KeyValue, 0, len(h))
	for k, v := range h {
		attrs = append(attrs, key(k, prefix).StringSlice(v))
	}

	return attrs
}
