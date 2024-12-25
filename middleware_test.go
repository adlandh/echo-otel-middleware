package echootelmiddleware

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	b3prop "go.opentelemetry.io/contrib/propagators/b3"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otel/trace/noop"
)

const (
	userID       = "123"
	userEndpoint = "/user/:id"
	userURL      = "/user/" + userID
	defaultHost  = "example.com"
	hostNameTag  = "http.host"
	statusTag    = "http.response.status_code"
	methodTag    = "http.method"
	routeTag     = "http.route"
)

func TestGetSpanNotInstrumented(t *testing.T) {
	router := echo.New()
	router.GET("/ping", func(c echo.Context) error {
		// Assert we don't have a span on the context.
		span := trace.SpanFromContext(c.Request().Context())
		ok := !span.SpanContext().IsValid()
		assert.True(t, ok)
		return c.String(http.StatusOK, "ok")
	})
	r := httptest.NewRequest("GET", "/ping", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, r)
	response := w.Result()
	assert.Equal(t, http.StatusOK, response.StatusCode)
}

func TestPropagationWithGlobalPropagators(t *testing.T) {
	provider := noop.NewTracerProvider()
	otel.SetTextMapPropagator(propagation.TraceContext{})

	r := httptest.NewRequest("GET", userURL, nil)
	w := httptest.NewRecorder()

	ctx := context.Background()
	sc := trace.NewSpanContext(trace.SpanContextConfig{
		TraceID: trace.TraceID{0x01},
		SpanID:  trace.SpanID{0x01},
	})
	ctx = trace.ContextWithRemoteSpanContext(ctx, sc)
	ctx, _ = provider.Tracer(tracerName).Start(ctx, "test")
	otel.GetTextMapPropagator().Inject(ctx, propagation.HeaderCarrier(r.Header))

	router := echo.New()
	router.Use(Middleware())
	router.GET(userEndpoint, func(c echo.Context) error {
		span := trace.SpanFromContext(c.Request().Context())
		assert.Equal(t, sc.TraceID(), span.SpanContext().TraceID())
		assert.Equal(t, sc.SpanID(), span.SpanContext().SpanID())
		return c.NoContent(http.StatusOK)
	})

	router.ServeHTTP(w, r)
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator())
	assert.Equal(t, http.StatusOK, w.Result().StatusCode)
}

func TestPropagationWithCustomPropagators(t *testing.T) {
	provider := noop.NewTracerProvider()

	b3 := b3prop.New()

	r := httptest.NewRequest("GET", userURL, nil)
	w := httptest.NewRecorder()

	ctx := context.Background()
	sc := trace.NewSpanContext(trace.SpanContextConfig{
		TraceID: trace.TraceID{0x01},
		SpanID:  trace.SpanID{0x01},
	})
	ctx = trace.ContextWithRemoteSpanContext(ctx, sc)
	ctx, _ = provider.Tracer(tracerName).Start(ctx, "test")
	b3.Inject(ctx, propagation.HeaderCarrier(r.Header))

	router := echo.New()
	router.Use(MiddlewareWithConfig(OtelConfig{
		TracerProvider: provider,
		Propagator:     b3,
	}))
	router.GET(userEndpoint, func(c echo.Context) error {
		span := trace.SpanFromContext(c.Request().Context())
		assert.Equal(t, sc.TraceID(), span.SpanContext().TraceID())
		assert.Equal(t, sc.SpanID(), span.SpanContext().SpanID())
		return c.NoContent(http.StatusOK)
	})

	router.ServeHTTP(w, r)
	assert.Equal(t, http.StatusOK, w.Result().StatusCode)
}

func TestSkipper(t *testing.T) {
	r := httptest.NewRequest("GET", "/ping", nil)
	w := httptest.NewRecorder()

	skipper := func(c echo.Context) bool {
		return c.Request().RequestURI == "/ping"
	}

	router := echo.New()
	router.Use(MiddlewareWithConfig(OtelConfig{Skipper: skipper}))
	router.GET("/ping", func(c echo.Context) error {
		span := trace.SpanFromContext(c.Request().Context())
		assert.False(t, span.SpanContext().HasSpanID())
		assert.False(t, span.SpanContext().HasTraceID())
		return c.NoContent(http.StatusOK)
	})

	router.ServeHTTP(w, r)
	assert.Equal(t, http.StatusOK, w.Result().StatusCode)
}

func TestChildSpanFromGlobalTracer(t *testing.T) {
	sr := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(sr))
	otel.SetTracerProvider(provider)

	router := echo.New()
	router.Use(Middleware())
	router.GET(userEndpoint, func(c echo.Context) error {
		return c.NoContent(http.StatusOK)
	})

	r := httptest.NewRequest("GET", userURL, nil)
	w := httptest.NewRecorder()

	router.ServeHTTP(w, r)
	assert.Equal(t, http.StatusOK, w.Result().StatusCode)
	assert.Len(t, sr.Ended(), 1)
}

func TestChildSpanFromCustomTracer(t *testing.T) {
	sr := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(sr))

	router := echo.New()
	router.Use(MiddlewareWithConfig(OtelConfig{TracerProvider: provider}))
	router.GET(userEndpoint, func(c echo.Context) error {
		return c.NoContent(http.StatusOK)
	})

	r := httptest.NewRequest("GET", userURL, nil)
	w := httptest.NewRecorder()

	router.ServeHTTP(w, r)
	assert.Equal(t, http.StatusOK, w.Result().StatusCode)
	assert.Len(t, sr.Ended(), 1)
}

func TestTrace200(t *testing.T) {
	sr := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(sr))

	router := echo.New()
	router.Use(MiddlewareWithConfig(OtelConfig{TracerProvider: provider}))
	router.GET(userEndpoint, func(c echo.Context) error {
		id := c.Param("id")
		return c.String(http.StatusOK, id)
	})

	r := httptest.NewRequest("GET", userURL, nil)
	w := httptest.NewRecorder()

	// do and verify the request
	router.ServeHTTP(w, r)
	response := w.Result()
	require.Equal(t, http.StatusOK, response.StatusCode)

	// verify traces look good
	spans := sr.Ended()
	require.Len(t, spans, 1)
	span := spans[0]
	assert.Equal(t, "HTTP GET URL: "+userEndpoint+" URI: "+userURL, span.Name())
	assert.Equal(t, trace.SpanKindServer, span.SpanKind())
	attrs := span.Attributes()
	assert.Contains(t, attrs, attribute.String(hostNameTag, defaultHost))
	assert.Contains(t, attrs, attribute.Int(statusTag, http.StatusOK))
	assert.Contains(t, attrs, attribute.String(methodTag, "GET"))
	assert.Contains(t, attrs, attribute.String(routeTag, userEndpoint))
}

func TestTrace200WithHeadersAndBody(t *testing.T) {
	sr := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(sr))

	router := echo.New()
	router.Use(MiddlewareWithConfig(OtelConfig{TracerProvider: provider, IsBodyDump: true, AreHeadersDump: true}))
	router.GET(userEndpoint, func(c echo.Context) error {
		id := c.Param("id")
		return c.String(http.StatusOK, id)
	})

	r := httptest.NewRequest("GET", userURL, strings.NewReader("test"))
	r.Header.Set(echo.HeaderContentType, "plain/text")
	w := httptest.NewRecorder()

	// do and verify the request
	router.ServeHTTP(w, r)
	response := w.Result()
	require.Equal(t, http.StatusOK, response.StatusCode)

	body, err := io.ReadAll(response.Body)
	require.NoError(t, err)
	require.Equal(t, "123", string(body))

	// verify traces look good
	spans := sr.Ended()
	require.Len(t, spans, 1)
	span := spans[0]
	assert.Equal(t, "HTTP GET URL: /user/:id URI: /user/123", span.Name())
	assert.Equal(t, trace.SpanKindServer, span.SpanKind())
	attrs := span.Attributes()
	assert.Contains(t, attrs, attribute.String(hostNameTag, defaultHost))
	assert.Contains(t, attrs, attribute.Int(statusTag, http.StatusOK))
	assert.Contains(t, attrs, attribute.String(methodTag, "GET"))
	assert.Contains(t, attrs, attribute.String(routeTag, userEndpoint))
	assert.Contains(t, attrs, attribute.String("http.request.body", "test"))
	assert.Contains(t, attrs, attribute.String("http.response.body", userID))
	assert.Contains(t, attrs, attribute.StringSlice("http.request.headers.content_type", []string{"plain/text"}))
}

func TestTrace200WithHeadersAndBodySkipped(t *testing.T) {
	sr := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(sr))

	router := echo.New()
	router.Use(MiddlewareWithConfig(OtelConfig{TracerProvider: provider, IsBodyDump: true, AreHeadersDump: true, BodySkipper: func(echo.Context) (bool, bool) { return true, true }}))
	router.GET(userEndpoint, func(c echo.Context) error {
		id := c.Param("id")
		return c.String(http.StatusOK, id)
	})

	r := httptest.NewRequest("GET", userURL, strings.NewReader("test"))
	r.Header.Set(echo.HeaderContentType, "plain/text")
	w := httptest.NewRecorder()

	// do and verify the request
	router.ServeHTTP(w, r)
	response := w.Result()
	require.Equal(t, http.StatusOK, response.StatusCode)

	body, err := io.ReadAll(response.Body)
	require.NoError(t, err)
	require.Equal(t, "123", string(body))

	// verify traces look good
	spans := sr.Ended()
	require.Len(t, spans, 1)
	span := spans[0]
	assert.Equal(t, "HTTP GET URL: /user/:id URI: /user/123", span.Name())
	assert.Equal(t, trace.SpanKindServer, span.SpanKind())
	attrs := span.Attributes()
	assert.Contains(t, attrs, attribute.String(hostNameTag, defaultHost))
	assert.Contains(t, attrs, attribute.Int(statusTag, http.StatusOK))
	assert.Contains(t, attrs, attribute.String(methodTag, "GET"))
	assert.Contains(t, attrs, attribute.String(routeTag, userEndpoint))
	assert.Contains(t, attrs, attribute.String("http.request.body", "[excluded]"))
	assert.Contains(t, attrs, attribute.String("http.response.body", "[excluded]"))
	assert.Contains(t, attrs, attribute.StringSlice("http.request.headers.content_type", []string{"plain/text"}))
}

func TestError(t *testing.T) {
	sr := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(sr))

	// setup
	router := echo.New()
	router.Use(MiddlewareWithConfig(OtelConfig{TracerProvider: provider}))
	wantErr := errors.New("oh no")
	// configure a handler that returns an error and 5xx status
	// code
	router.GET("/server_err", func(c echo.Context) error {
		return wantErr
	})
	r := httptest.NewRequest("GET", "/server_err", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, r)
	response := w.Result()
	assert.Equal(t, http.StatusInternalServerError, response.StatusCode)

	// verify the errors and status are correct
	spans := sr.Ended()
	require.Len(t, spans, 1)
	span := spans[0]
	assert.Equal(t, "HTTP GET URL: /server_err", span.Name())
	attrs := span.Attributes()
	assert.Contains(t, attrs, attribute.String(hostNameTag, defaultHost))
	assert.Contains(t, attrs, attribute.Int(statusTag, http.StatusInternalServerError))
	assert.Contains(t, attrs, attribute.String("echo.error", "oh no"))
	// server errors set the status
	assert.Equal(t, codes.Error, span.Status().Code)
}

func TestStatusError(t *testing.T) {
	for _, tc := range []struct {
		name       string
		echoError  string
		statusCode int
		spanCode   codes.Code
		handler    func(c echo.Context) error
	}{
		{
			name:       "StandardError",
			echoError:  "oh no",
			statusCode: http.StatusInternalServerError,
			spanCode:   codes.Error,
			handler: func(c echo.Context) error {
				return errors.New("oh no")
			},
		},
		{
			name:       "EchoHTTPServerError",
			echoError:  "code=500, message=my error message",
			statusCode: http.StatusInternalServerError,
			spanCode:   codes.Error,
			handler: func(c echo.Context) error {
				return echo.NewHTTPError(http.StatusInternalServerError, "my error message")
			},
		},
		{
			name:       "EchoHTTPClientError",
			echoError:  "code=400, message=my error message",
			statusCode: http.StatusBadRequest,
			spanCode:   codes.Error,
			handler: func(c echo.Context) error {
				return echo.NewHTTPError(http.StatusBadRequest, "my error message")
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sr := tracetest.NewSpanRecorder()
			provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(sr))

			router := echo.New()
			router.Use(MiddlewareWithConfig(OtelConfig{TracerProvider: provider}))
			router.GET("/err", tc.handler)
			r := httptest.NewRequest("GET", "/err", nil)
			w := httptest.NewRecorder()
			router.ServeHTTP(w, r)

			spans := sr.Ended()
			require.Len(t, spans, 1)
			span := spans[0]
			assert.Equal(t, "HTTP GET URL: /err", span.Name())
			assert.Equal(t, tc.spanCode, span.Status().Code)

			attrs := span.Attributes()
			assert.Contains(t, attrs, attribute.String(hostNameTag, defaultHost))
			assert.Contains(t, attrs, attribute.String(routeTag, "/err"))
			assert.Contains(t, attrs, attribute.String(methodTag, "GET"))
			assert.Contains(t, attrs, attribute.Int(statusTag, tc.statusCode))
			assert.Contains(t, attrs, attribute.String("echo.error", tc.echoError))
		})
	}
}

func TestErrorNotSwallowedByMiddleware(t *testing.T) {
	e := echo.New()
	r := httptest.NewRequest(http.MethodGet, "/err", nil)
	w := httptest.NewRecorder()
	c := e.NewContext(r, w)
	h := Middleware()(func(c echo.Context) error {
		return assert.AnError
	})

	err := h(c)
	assert.Equal(t, assert.AnError, err)
}

func BenchmarkWithMiddleware(b *testing.B) {
	router := echo.New()
	router.Use(Middleware())
	router.GET(userEndpoint, func(c echo.Context) error {
		id := c.Param("id")
		return c.String(http.StatusOK, id)
	})

	r := httptest.NewRequest("GET", userURL, nil)
	w := httptest.NewRecorder()

	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		// do and verify the request
		router.ServeHTTP(w, r)
	}
}

func BenchmarkWithMiddlewareWithNoBodyNoHeaders(b *testing.B) {
	router := echo.New()
	router.Use(MiddlewareWithConfig(OtelConfig{AreHeadersDump: false}))
	router.GET(userEndpoint, func(c echo.Context) error {
		id := c.Param("id")
		return c.String(http.StatusOK, id)
	})

	r := httptest.NewRequest("GET", userURL, strings.NewReader("test"))
	w := httptest.NewRecorder()

	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		// do and verify the request
		router.ServeHTTP(w, r)
	}
}

func BenchmarkWithMiddlewareWithBodyDump(b *testing.B) {
	router := echo.New()
	router.Use(MiddlewareWithConfig(OtelConfig{IsBodyDump: true}))
	router.GET(userEndpoint, func(c echo.Context) error {
		id := c.Param("id")
		return c.String(http.StatusOK, id)
	})

	r := httptest.NewRequest("GET", userURL, strings.NewReader("test"))
	w := httptest.NewRecorder()

	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		// do and verify the request
		router.ServeHTTP(w, r)
	}
}

func BenchmarkWithoutMiddleware(b *testing.B) {
	router := echo.New()
	router.GET(userEndpoint, func(c echo.Context) error {
		id := c.Param("id")
		return c.String(http.StatusOK, id)
	})

	r := httptest.NewRequest("GET", userURL, nil)
	w := httptest.NewRecorder()

	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		// do and verify the request
		router.ServeHTTP(w, r)
	}
}
