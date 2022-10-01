package echo_otel_middleware

import (
	"math/rand"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/require"
)

func Test_limitString(t *testing.T) {
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
			want: "05Kj7\n---- skipped ----\n3z2sD",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, limitString(tt.args.str, tt.args.size))
		})
	}
}

func Test_generateToken(t *testing.T) {
	rand.Seed(time.Now().UnixMicro())
	count := rand.Intn(20)
	for tt := 0; tt < count; tt++ {
		require.Equal(t, 32, len(generateToken()))
	}
}

func Test_getRequestID(t *testing.T) {
	e := echo.New()

	t.Run("token in header", func(t *testing.T) {
		req := httptest.NewRequest(echo.GET, "/", nil)
		req.Header.Set(echo.HeaderXRequestID, "test")
		rec := httptest.NewRecorder()
		c := e.NewContext(req, rec)
		require.Equal(t, "test", getRequestID(c))
	})

	t.Run("generate token", func(t *testing.T) {
		req := httptest.NewRequest(echo.GET, "/", nil)
		rec := httptest.NewRecorder()
		c := e.NewContext(req, rec)
		require.Equal(t, 32, len(getRequestID(c)))
	})
}
