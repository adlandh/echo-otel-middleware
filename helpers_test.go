package echootelmiddleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/labstack/echo/v5"
	"github.com/labstack/echo/v5/middleware"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/attribute"
)

func TestPrepareTagName(t *testing.T) {
	type args struct {
		str  string
		size int
	}
	tests := []struct {
		name string
		args args
		want string
	}{
		{
			name: "Short string - large limit",
			args: args{
				str:  "05Kj7",
				size: 100,
			},
			want: "05Kj7",
		},
		{
			name: "Long string - small limit",
			args: args{
				str:  "05Kj7z2AXCl603gMJu6B23z2sD",
				size: 10,
			},
			want: "05Kj7z2AXC",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, prepareTagName(tt.args.str, tt.args.size))
		})
	}
}

func TestPrepareTagValue(t *testing.T) {
	type args struct {
		str string
	}
	tests := []struct {
		name string
		args args
		want string
	}{
		{
			name: "String with no \\n",
			args: args{
				str: "05Kj7z2AXCl603gMJu6B23z2sD",
			},
			want: "05Kj7z2AXCl603gMJu6B23z2sD",
		},
		{
			name: "String with \\n",
			args: args{
				str: "05\nKj7z2AXCl603gMJu6B23z2sD",
			},
			want: "05 Kj7z2AXCl603gMJu6B23z2sD",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, prepareTagValue(tt.args.str, 200, true))
		})
	}
}

func TestPrepareTagValueRemoveNewLinesFalse(t *testing.T) {
	t.Run("keeps newlines", func(t *testing.T) {
		require.Equal(t, "a\nb", prepareTagValue("a\nb", 10, false))
	})

	t.Run("applies limit with newline", func(t *testing.T) {
		require.Equal(t, "ab\ncdefg...", prepareTagValue("ab\ncdefghijk", 11, false))
	})
}

func TestLimitStringWithDots(t *testing.T) {
	t.Run("no truncation", func(t *testing.T) {
		require.Equal(t, "abcdefghij", limitStringWithDots("abcdefghij", 20))
	})

	t.Run("truncation with dots", func(t *testing.T) {
		require.Equal(t, "abcdefgh...", limitStringWithDots("abcdefghijk", 11))
	})

	t.Run("short limit no dots", func(t *testing.T) {
		require.Equal(t, "abcde", limitStringWithDots("abcdefghijk", 5))
	})
}

func TestLimitStringValidUTF8(t *testing.T) {
	// "ab" + euro sign + "cd", euro sign is 3 bytes.
	input := "ab" + string([]byte{0xe2, 0x82, 0xac}) + "cd"
	require.Equal(t, "ab", limitString(input, 3))
}

func TestPrepareAttrs(t *testing.T) {
	cfg := OtelConfig{
		LimitNameSize:  4,
		LimitValueSize: 5,
		RemoveNewLines: true,
	}

	attrs := prepareAttrs(cfg,
		attribute.String("keyName", "a\nbcdef"),
		attribute.Int("intkey", 3),
	)

	require.Len(t, attrs, 2)
	require.Equal(t, attribute.Key("keyN"), attrs[0].Key)
	require.Equal(t, "a bcd", attrs[0].Value.AsString())
	require.Equal(t, attribute.Key("intk"), attrs[1].Key)
	require.Equal(t, int64(3), attrs[1].Value.AsInt64())
}

func TestGetRequestID(t *testing.T) {
	e := echo.New()

	t.Run("token in header", func(t *testing.T) {
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		r.Header.Set(echo.HeaderXRequestID, "test")
		w := httptest.NewRecorder()
		c := e.NewContext(r, w)
		e.ServeHTTP(w, r)
		require.Equal(t, "test", getRequestID(c))
	})

	t.Run("generate token", func(t *testing.T) {
		e.Use(middleware.RequestID())
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		w := httptest.NewRecorder()
		c := e.NewContext(r, w)
		e.ServeHTTP(w, r)
		require.Equal(t, 32, len(getRequestID(c)))
	})
}
