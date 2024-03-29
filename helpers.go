package echootelmiddleware

import (
	"strings"

	"github.com/labstack/echo/v4"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

func prepareTagValue(str string, removeNewLine bool) string {
	if !removeNewLine {
		return str
	}

	return strings.ReplaceAll(str, "\n", " ") // no \n in strings
}

func prepareTagName(str string, size int) string {
	if size <= 0 {
		return str
	}

	result := []rune(str)

	if len(result) <= size {
		return str
	}

	return string(result[:size])
}

func getRequestID(ctx echo.Context) string {
	requestID := ctx.Request().Header.Get(echo.HeaderXRequestID) // request-id generated by reverse-proxy
	if requestID == "" {
		// missed request-id from proxy,got generated one by middleware.RequestID()
		requestID = ctx.Response().Header().Get(echo.HeaderXRequestID)
	}

	return requestID
}

func setAttr(span trace.Span, limitNameSize int, removeNewLines bool, attrs ...attribute.KeyValue) {
	span.SetAttributes(prepareAttrs(limitNameSize, removeNewLines, attrs...)...)
}

func prepareAttrs(limitNameSize int, removeNewLines bool, attrs ...attribute.KeyValue) []attribute.KeyValue {
	for i := range attrs {
		attrs[i].Key = attribute.Key(prepareTagName(string(attrs[i].Key), limitNameSize))
		if attrs[i].Value.Type() == attribute.STRING {
			attrs[i].Value = attribute.StringValue(prepareTagValue(attrs[i].Value.AsString(), removeNewLines))
		}
	}

	return attrs
}
