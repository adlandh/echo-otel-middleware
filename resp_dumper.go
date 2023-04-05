package echo_otel_middleware

import (
	"bufio"
	"bytes"
	"io"
	"net"
	"net/http"

	"github.com/labstack/echo/v4"
)

type responseDumper struct {
	http.ResponseWriter

	mw  io.Writer
	buf *bytes.Buffer
}

func newResponseDumper(resp *echo.Response) *responseDumper {
	buf := new(bytes.Buffer)
	return &responseDumper{
		ResponseWriter: resp.Writer,

		mw:  io.MultiWriter(resp.Writer, buf),
		buf: buf,
	}
}

func (d *responseDumper) Write(b []byte) (int, error) {
	return d.mw.Write(b)
}

func (d *responseDumper) GetResponse() string {
	return d.buf.String()
}

func (d *responseDumper) Flush() {
	d.ResponseWriter.(http.Flusher).Flush()
}

func (d *responseDumper) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	return d.ResponseWriter.(http.Hijacker).Hijack()
}
