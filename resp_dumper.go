package echootelmiddleware

import (
	"bufio"
	"bytes"
	"fmt"
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
	nBytes, err := d.mw.Write(b)
	return nBytes, fmt.Errorf("error writing response: %w", err)
}

func (d *responseDumper) GetResponse() string {
	return d.buf.String()
}

func (d *responseDumper) Flush() {
	if flusher, ok := d.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}

func (d *responseDumper) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	if hijacker, ok := d.ResponseWriter.(http.Hijacker); ok {
		conn, rw, err := hijacker.Hijack()
		return conn, rw, fmt.Errorf("error hijacking response: %w", err)
	}

	return nil, nil, nil
}
