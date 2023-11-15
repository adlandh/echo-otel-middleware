package echootelmiddleware

import (
	"net/http/httptest"
	"testing"

	"github.com/labstack/echo/v4"
	"github.com/labstack/echo/v4/middleware"
	"github.com/stretchr/testify/require"
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
			require.Equal(t, tt.want, prepareTagValue(tt.args.str, true))
		})
	}
}

func TestGetRequestID(t *testing.T) {
	e := echo.New()

	t.Run("token in header", func(t *testing.T) {
		r := httptest.NewRequest(echo.GET, "/", nil)
		r.Header.Set(echo.HeaderXRequestID, "test")
		w := httptest.NewRecorder()
		c := e.NewContext(r, w)
		e.ServeHTTP(w, r)
		require.Equal(t, "test", getRequestID(c))
	})

	t.Run("generate token", func(t *testing.T) {
		e.Use(middleware.RequestID())
		r := httptest.NewRequest(echo.GET, "/", nil)
		w := httptest.NewRecorder()
		c := e.NewContext(r, w)
		e.ServeHTTP(w, r)
		require.Equal(t, 32, len(getRequestID(c)))
	})
}
