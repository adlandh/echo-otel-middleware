package echootelmiddleware

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/labstack/echo/v5"
	"github.com/labstack/echo/v5/middleware"
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
	hostNameTag  = "server.address"
	statusTag    = "http.response.status_code"
	methodTag    = "http.request.method"
	routeTag     = "http.route"
)

func hasAttrPrefix(attrs []attribute.KeyValue, prefix string) bool {
	for _, attr := range attrs {
		if strings.HasPrefix(string(attr.Key), prefix) {
			return true
		}
	}

	return false
}

func TestGetSpanNotInstrumented(t *testing.T) {
	router := echo.New()
	router.GET("/ping", func(c *echo.Context) error {
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
	router.GET(userEndpoint, func(c *echo.Context) error {
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
	router.GET(userEndpoint, func(c *echo.Context) error {
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

	skipper := func(c *echo.Context) bool {
		return c.Request().RequestURI == "/ping"
	}

	router := echo.New()
	router.Use(MiddlewareWithConfig(OtelConfig{Skipper: skipper}))
	router.GET("/ping", func(c *echo.Context) error {
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
	router.GET(userEndpoint, func(c *echo.Context) error {
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
	router.GET(userEndpoint, func(c *echo.Context) error {
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
	router.GET(userEndpoint, func(c *echo.Context) error {
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
	assert.Equal(t, "GET "+userEndpoint, span.Name())
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
	router.GET(userEndpoint, func(c *echo.Context) error {
		id := c.Param("id")
		return c.String(http.StatusOK, id)
	})

	r := httptest.NewRequest("GET", userURL, strings.NewReader("test"))
	r.Header.Set(echo.HeaderContentType, "text/plain")
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
	assert.Equal(t, "GET /user/:id", span.Name())
	assert.Equal(t, trace.SpanKindServer, span.SpanKind())
	attrs := span.Attributes()
	assert.Contains(t, attrs, attribute.String(hostNameTag, defaultHost))
	assert.Contains(t, attrs, attribute.Int(statusTag, http.StatusOK))
	assert.Contains(t, attrs, attribute.String(methodTag, "GET"))
	assert.Contains(t, attrs, attribute.String(routeTag, userEndpoint))
	assert.Contains(t, attrs, attribute.String("http.request.body", "test"))
	assert.Contains(t, attrs, attribute.String("http.response.body", userID))
	assert.Contains(t, attrs, attribute.StringSlice("http.request.headers.content_type", []string{"text/plain"}))
}

func TestTrace200WithHeadersAndBodySkipped(t *testing.T) {
	sr := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(sr))

	router := echo.New()
	router.Use(MiddlewareWithConfig(OtelConfig{TracerProvider: provider, IsBodyDump: true, AreHeadersDump: true, BodySkipper: func(*echo.Context) (bool, bool) { return true, true }}))
	router.GET(userEndpoint, func(c *echo.Context) error {
		id := c.Param("id")
		return c.String(http.StatusOK, id)
	})

	r := httptest.NewRequest("GET", userURL, strings.NewReader("test"))
	r.Header.Set(echo.HeaderContentType, "text/plain")
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
	assert.Equal(t, "GET /user/:id", span.Name())
	assert.Equal(t, trace.SpanKindServer, span.SpanKind())
	attrs := span.Attributes()
	assert.Contains(t, attrs, attribute.String(hostNameTag, defaultHost))
	assert.Contains(t, attrs, attribute.Int(statusTag, http.StatusOK))
	assert.Contains(t, attrs, attribute.String(methodTag, "GET"))
	assert.Contains(t, attrs, attribute.String(routeTag, userEndpoint))
	assert.Contains(t, attrs, attribute.String("http.request.body", "[excluded]"))
	assert.Contains(t, attrs, attribute.String("http.response.body", "[excluded]"))
	assert.Contains(t, attrs, attribute.StringSlice("http.request.headers.content_type", []string{"text/plain"}))
}

func TestCreateSpanName(t *testing.T) {
	t.Run("with route", func(t *testing.T) {
		r := httptest.NewRequest("GET", "/ping", nil)
		assert.Equal(t, "GET /ping", createSpanName(r, "/ping"))
	})

	t.Run("route differs from URI - URI not leaked", func(t *testing.T) {
		r := httptest.NewRequest("GET", "/user/123?active=true", nil)
		assert.Equal(t, "GET /user/:id", createSpanName(r, "/user/:id"))
	})

	t.Run("no route falls back to method", func(t *testing.T) {
		r := httptest.NewRequest("POST", "/anything", http.NoBody)
		assert.Equal(t, "HTTP POST", createSpanName(r, ""))
	})
}

func TestSetSpanStatus(t *testing.T) {
	for _, tc := range []struct {
		name       string
		statusCode int
		wantCode   codes.Code
	}{
		{
			name:       "1xx",
			statusCode: http.StatusSwitchingProtocols,
			wantCode:   codes.Unset,
		},
		{
			name:       "2xx",
			statusCode: http.StatusOK,
			wantCode:   codes.Ok,
		},
		{
			name:       "4xx",
			statusCode: http.StatusBadRequest,
			wantCode:   codes.Error,
		},
		{
			name:       "5xx",
			statusCode: http.StatusInternalServerError,
			wantCode:   codes.Error,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sr := tracetest.NewSpanRecorder()
			provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(sr))
			tracer := provider.Tracer(tracerName)

			_, span := tracer.Start(context.Background(), "test")
			setSpanStatus(span, tc.statusCode, nil)
			span.End()

			spans := sr.Ended()
			require.Len(t, spans, 1)
			assert.Equal(t, tc.wantCode, spans[0].Status().Code)
		})
	}
}

func TestShouldSkipMiddleware(t *testing.T) {
	e := echo.New()

	t.Run("nil request", func(t *testing.T) {
		c := e.NewContext(httptest.NewRequest("GET", "/", nil), httptest.NewRecorder())
		c.SetRequest(nil)
		assert.True(t, shouldSkipMiddleware(c, OtelConfig{Skipper: middleware.DefaultSkipper}))
	})

	t.Run("nil response", func(t *testing.T) {
		c := e.NewContext(httptest.NewRequest("GET", "/", nil), httptest.NewRecorder())
		c.SetResponse(nil)
		assert.True(t, shouldSkipMiddleware(c, OtelConfig{Skipper: middleware.DefaultSkipper}))
	})

	t.Run("skipper true", func(t *testing.T) {
		c := e.NewContext(httptest.NewRequest("GET", "/", nil), httptest.NewRecorder())
		cfg := OtelConfig{
			Skipper: func(*echo.Context) bool {
				return true
			},
		}
		assert.True(t, shouldSkipMiddleware(c, cfg))
	})

	t.Run("not skipped", func(t *testing.T) {
		c := e.NewContext(httptest.NewRequest("GET", "/", nil), httptest.NewRecorder())
		assert.False(t, shouldSkipMiddleware(c, OtelConfig{Skipper: middleware.DefaultSkipper}))
	})
}

func TestHeadersDumpDisabled(t *testing.T) {
	sr := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(sr))

	router := echo.New()
	router.Use(MiddlewareWithConfig(OtelConfig{TracerProvider: provider, AreHeadersDump: false}))
	router.GET(userEndpoint, func(c *echo.Context) error {
		return c.String(http.StatusOK, userID)
	})

	r := httptest.NewRequest("GET", userURL, strings.NewReader("test"))
	r.Header.Set(echo.HeaderContentType, "text/plain")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, r)

	spans := sr.Ended()
	require.Len(t, spans, 1)
	attrs := spans[0].Attributes()
	assert.False(t, hasAttrPrefix(attrs, "http.request.headers."))
	assert.False(t, hasAttrPrefix(attrs, "http.response.headers."))
}

func TestBodySkipperAsymmetric(t *testing.T) {
	for _, tc := range []struct {
		name         string
		skipReqBody  bool
		skipRespBody bool
		wantReqBody  string
		wantRespBody string
	}{
		{
			name:         "skip request only",
			skipReqBody:  true,
			skipRespBody: false,
			wantReqBody:  "[excluded]",
			wantRespBody: userID,
		},
		{
			name:         "skip response only",
			skipReqBody:  false,
			skipRespBody: true,
			wantReqBody:  "test",
			wantRespBody: "[excluded]",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sr := tracetest.NewSpanRecorder()
			provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(sr))

			router := echo.New()
			router.Use(MiddlewareWithConfig(OtelConfig{
				TracerProvider: provider,
				IsBodyDump:     true,
				BodySkipper: func(*echo.Context) (bool, bool) {
					return tc.skipReqBody, tc.skipRespBody
				},
			}))
			router.GET(userEndpoint, func(c *echo.Context) error {
				return c.String(http.StatusOK, userID)
			})

			r := httptest.NewRequest("GET", userURL, strings.NewReader("test"))
			w := httptest.NewRecorder()
			router.ServeHTTP(w, r)

			spans := sr.Ended()
			require.Len(t, spans, 1)
			attrs := spans[0].Attributes()
			assert.Contains(t, attrs, attribute.String("http.request.body", tc.wantReqBody))
			assert.Contains(t, attrs, attribute.String("http.response.body", tc.wantRespBody))
		})
	}
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
	router.GET("/server_err", func(c *echo.Context) error {
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
	assert.Equal(t, "GET /server_err", span.Name())
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
		handler    func(c *echo.Context) error
	}{
		{
			name:       "StandardError",
			echoError:  "oh no",
			statusCode: http.StatusInternalServerError,
			spanCode:   codes.Error,
			handler: func(c *echo.Context) error {
				return errors.New("oh no")
			},
		},
		{
			name:       "EchoHTTPServerError",
			echoError:  "code=500, message=my error message",
			statusCode: http.StatusInternalServerError,
			spanCode:   codes.Error,
			handler: func(c *echo.Context) error {
				return echo.NewHTTPError(http.StatusInternalServerError, "my error message")
			},
		},
		{
			name:       "EchoHTTPClientError",
			echoError:  "code=400, message=my error message",
			statusCode: http.StatusBadRequest,
			spanCode:   codes.Error,
			handler: func(c *echo.Context) error {
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
			assert.Equal(t, "GET /err", span.Name())
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
	h := Middleware()(func(c *echo.Context) error {
		return assert.AnError
	})

	err := h(c)
	assert.Equal(t, assert.AnError, err)
}

func TestNilRequestBodyWithBodyDump(t *testing.T) {
	sr := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(sr))

	router := echo.New()
	router.Use(MiddlewareWithConfig(OtelConfig{TracerProvider: provider, IsBodyDump: true}))
	router.GET(userEndpoint, func(c *echo.Context) error {
		return c.String(http.StatusOK, userID)
	})

	r := httptest.NewRequest("GET", userURL, http.NoBody)
	r.Body = nil // ensure nil body
	w := httptest.NewRecorder()

	require.NotPanics(t, func() {
		router.ServeHTTP(w, r)
	})

	spans := sr.Ended()
	require.Len(t, spans, 1)
	attrs := spans[0].Attributes()
	// No http.request.body attribute should be set for a nil request body.
	for _, attr := range attrs {
		assert.NotEqual(t, attribute.Key("http.request.body"), attr.Key)
	}
}

func TestEmptyResponseBodyExcluded(t *testing.T) {
	sr := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(sr))

	router := echo.New()
	router.Use(MiddlewareWithConfig(OtelConfig{
		TracerProvider: provider,
		IsBodyDump:     true,
		BodySkipper: func(*echo.Context) (bool, bool) {
			return false, true
		},
	}))
	router.GET(userEndpoint, func(c *echo.Context) error {
		return c.NoContent(http.StatusOK)
	})

	r := httptest.NewRequest("GET", userURL, http.NoBody)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, r)

	spans := sr.Ended()
	require.Len(t, spans, 1)
	attrs := spans[0].Attributes()
	assert.Contains(t, attrs, attribute.String("http.response.body", "[excluded]"))
}

func TestNonRecordingSpanBranch(t *testing.T) {
	router := echo.New()
	// noop tracer provider produces non-recording spans.
	router.Use(MiddlewareWithConfig(OtelConfig{TracerProvider: noop.NewTracerProvider()}))

	var handlerCalled bool

	router.GET(userEndpoint, func(c *echo.Context) error {
		handlerCalled = true
		return c.String(http.StatusOK, userID)
	})

	r := httptest.NewRequest("GET", userURL, http.NoBody)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, r)

	assert.True(t, handlerCalled)
	assert.Equal(t, http.StatusOK, w.Result().StatusCode)
}

func TestNonRecordingSpanErrorPropagated(t *testing.T) {
	router := echo.New()
	router.Use(MiddlewareWithConfig(OtelConfig{TracerProvider: noop.NewTracerProvider()}))
	router.GET("/err", func(_ *echo.Context) error {
		return echo.NewHTTPError(http.StatusBadRequest, "bad request")
	})

	r := httptest.NewRequest("GET", "/err", http.NoBody)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, r)

	assert.Equal(t, http.StatusBadRequest, w.Result().StatusCode)
}

func TestResponseStatusNoDumperNoError(t *testing.T) {
	e := echo.New()
	c := e.NewContext(httptest.NewRequest("GET", "/", http.NoBody), httptest.NewRecorder())
	// Response has not been written; echo.UnwrapResponse should still succeed
	// and return the current status (0 before any write).
	status := responseStatus(c, nil, nil)
	assert.Equal(t, 0, status)
}

func TestPathParameterAttribute(t *testing.T) {
	sr := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(sr))

	router := echo.New()
	router.Use(MiddlewareWithConfig(OtelConfig{TracerProvider: provider}))
	router.GET(userEndpoint, func(c *echo.Context) error {
		return c.String(http.StatusOK, c.Param("id"))
	})

	r := httptest.NewRequest("GET", userURL, http.NoBody)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, r)

	spans := sr.Ended()
	require.Len(t, spans, 1)
	attrs := spans[0].Attributes()
	assert.Contains(t, attrs, attribute.String("http.path.id", userID))
}

// failingReader is an io.ReadCloser that always returns an error on Read.
type failingReader struct{}

func (failingReader) Read(_ []byte) (int, error) { return 0, errors.New("boom") }
func (failingReader) Close() error               { return nil }

func TestDumpRequestBodyReadError(t *testing.T) {
	sr := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(sr))

	router := echo.New()
	router.Use(MiddlewareWithConfig(OtelConfig{TracerProvider: provider, IsBodyDump: true}))
	router.GET(userEndpoint, func(c *echo.Context) error {
		return c.String(http.StatusOK, userID)
	})

	r := httptest.NewRequest("GET", userURL, http.NoBody)
	r.Header.Set(echo.HeaderContentType, "text/plain")
	r.Body = failingReader{}
	w := httptest.NewRecorder()
	router.ServeHTTP(w, r)

	spans := sr.Ended()
	require.Len(t, spans, 1)
	span := spans[0]
	attrs := span.Attributes()
	assert.Contains(t, attrs, attribute.String("http.request.body", "[read-error]"))

	// The read error should be recorded as a span event.
	var found bool

	for _, ev := range span.Events() {
		if ev.Name == "exception" {
			found = true
			break
		}
	}

	assert.True(t, found, "expected an exception event for the read error")
}

func TestSensitiveHeadersRedacted(t *testing.T) {
	sr := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(sr))

	router := echo.New()
	router.Use(MiddlewareWithConfig(OtelConfig{TracerProvider: provider, AreHeadersDump: true}))
	router.GET(userEndpoint, func(c *echo.Context) error {
		return c.String(http.StatusOK, userID)
	})

	r := httptest.NewRequest("GET", userURL, http.NoBody)
	r.Header.Set("Authorization", "Bearer secret-token")
	r.Header.Set("Cookie", "session=abc")
	r.Header.Set("X-Trace-Flavor", "vanilla")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, r)

	spans := sr.Ended()
	require.Len(t, spans, 1)
	attrs := spans[0].Attributes()

	assert.Contains(t, attrs, attribute.String("http.request.headers.authorization", "[redacted]"))
	assert.Contains(t, attrs, attribute.String("http.request.headers.cookie", "[redacted]"))
	assert.Contains(t, attrs, attribute.StringSlice("http.request.headers.x_trace_flavor", []string{"vanilla"}))

	for _, attr := range attrs {
		if attr.Key == "http.request.headers.authorization" {
			assert.NotContains(t, attr.Value.AsString(), "secret-token")
		}
	}
}

func TestCustomHeaderSkipper(t *testing.T) {
	sr := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(sr))

	router := echo.New()
	router.Use(MiddlewareWithConfig(OtelConfig{
		TracerProvider: provider,
		AreHeadersDump: true,
		HeaderSkipper: func(name string) bool {
			return strings.EqualFold(name, "X-Internal")
		},
	}))
	router.GET(userEndpoint, func(c *echo.Context) error {
		return c.String(http.StatusOK, userID)
	})

	r := httptest.NewRequest("GET", userURL, http.NoBody)
	r.Header.Set("X-Internal", "shh")
	r.Header.Set("Authorization", "Bearer leak-me") // not in custom skipper, but not redacted either
	w := httptest.NewRecorder()
	router.ServeHTTP(w, r)

	attrs := sr.Ended()[0].Attributes()
	assert.Contains(t, attrs, attribute.String("http.request.headers.x_internal", "[redacted]"))
	// Custom skipper fully replaces the default; Authorization is not redacted here.
	assert.Contains(t, attrs, attribute.StringSlice("http.request.headers.authorization", []string{"Bearer leak-me"}))
}

func TestPanicIsRecordedAndPropagated(t *testing.T) {
	sr := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(sr))

	router := echo.New()
	router.Use(MiddlewareWithConfig(OtelConfig{TracerProvider: provider}))
	router.GET("/boom", func(_ *echo.Context) error {
		panic("kaboom")
	})

	r := httptest.NewRequest("GET", "/boom", http.NoBody)
	w := httptest.NewRecorder()

	require.Panics(t, func() {
		router.ServeHTTP(w, r)
	})

	spans := sr.Ended()
	require.Len(t, spans, 1)
	span := spans[0]
	assert.Equal(t, codes.Error, span.Status().Code)

	var foundException bool

	for _, ev := range span.Events() {
		if ev.Name == "exception" {
			foundException = true
			break
		}
	}

	assert.True(t, foundException, "expected an exception event for the panic")
}

func TestResponseRestoredAfterMiddleware(t *testing.T) {
	sr := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(sr))

	e := echo.New()
	r := httptest.NewRequest("GET", "/x", strings.NewReader("hello"))
	r.Header.Set(echo.HeaderContentType, "text/plain")
	w := httptest.NewRecorder()
	c := e.NewContext(r, w)

	origResp := c.Response()

	h := MiddlewareWithConfig(OtelConfig{TracerProvider: provider, IsBodyDump: true})(func(c *echo.Context) error {
		return c.String(http.StatusOK, "ok")
	})
	require.NoError(t, h(c))

	assert.Same(t, origResp, c.Response(), "outer middleware should observe the original *echo.Response after the otel middleware returns")
}

func BenchmarkWithMiddleware(b *testing.B) {
	router := echo.New()
	router.Use(Middleware())
	router.GET(userEndpoint, func(c *echo.Context) error {
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
	router.GET(userEndpoint, func(c *echo.Context) error {
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
	router.GET(userEndpoint, func(c *echo.Context) error {
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
	router.GET(userEndpoint, func(c *echo.Context) error {
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
