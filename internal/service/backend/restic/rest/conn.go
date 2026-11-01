// Copyright 2026 Greenmask Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package rest

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/rs/zerolog"

	"github.com/greenmaskio/arcave/internal/service/backend/restic/rest/model"
)

// The HTTP/2 flow-control windows of one served stream, at net/http's ceilings:
// the defaults stall a restic pack upload on every window.
const (
	maxReceiveBufferPerConnection = 4<<20 - 1
	maxReceiveBufferPerStream     = 4<<20 - 1
	maxReadFrameSize              = 1 << 20
)

// ConnServer serves one repository per stream: restic's REST protocol over
// HTTP/2 with prior knowledge, the way restic speaks it to an rclone child.
type ConnServer struct {
	repos model.RepositoryResolver
	echo  *echo.Echo
}

// NewConnServer routes every stream it serves into repos. The context is
// NewEcho's: its logger is the one echo writes through.
func NewConnServer(ctx context.Context, repos model.RepositoryResolver) *ConnServer {
	return &ConnServer{
		repos: repos,
		echo:  NewEcho(ctx, repos),
	}
}

// ServeRepository serves objectID's repository over stream until the client
// hangs up or ctx ends, and closes stream. Every request path is taken relative
// to the repository: the client addresses "/config", the routes see
// "/<objectID>/config". An objectID the resolver refuses is returned unserved.
func (s *ConnServer) ServeRepository(ctx context.Context, objectID string, stream io.ReadWriteCloser) error {
	conn := newStreamConn(stream)
	defer logStreamClose(ctx, conn)

	if _, err := s.repos.Lookup(objectID); err != nil {
		return fmt.Errorf("serve restic repository %q: %w", objectID, err)
	}

	var protocols http.Protocols
	protocols.SetUnencryptedHTTP2(true)
	server := &http.Server{
		Handler:     newRepositoryHandler(s.echo, objectID),
		Protocols:   &protocols,
		BaseContext: func(net.Listener) context.Context { return ctx },
		ErrorLog:    s.echo.StdLogger,
		HTTP2: &http.HTTP2Config{
			MaxReadFrameSize:              maxReadFrameSize,
			MaxReceiveBufferPerConnection: maxReceiveBufferPerConnection,
			MaxReceiveBufferPerStream:     maxReceiveBufferPerStream,
		},
	}
	listener := newConnListener(conn)
	served := make(chan error, 1)
	go func() { served <- server.Serve(listener) }()

	select {
	case <-conn.closed:
	case <-ctx.Done():
	}
	if err := server.Close(); err != nil {
		zerolog.Ctx(ctx).Debug().Err(err).Msg("close restic repository server")
	}
	if err := <-served; err != nil && !errors.Is(err, http.ErrServerClosed) {
		zerolog.Ctx(ctx).Debug().Err(err).Msg("restic repository server stopped")
	}
	if ctx.Err() != nil {
		return context.Cause(ctx)
	}
	return nil
}

// newRepositoryHandler roots every request of h at objectID's repository.
func newRepositoryHandler(h http.Handler, objectID string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rooted := r.Clone(r.Context())
		rooted.URL.Path = "/" + objectID + r.URL.Path
		rooted.URL.RawPath = ""
		h.ServeHTTP(w, rooted)
	})
}

// logStreamClose closes conn; its failure changes no outcome.
func logStreamClose(ctx context.Context, conn *streamConn) {
	if err := conn.Close(); err != nil {
		zerolog.Ctx(ctx).Debug().Err(err).Msg("close restic repository stream")
	}
}

// pipeAddr is both ends' address of a stream: there is no network under it.
type pipeAddr struct{}

func (pipeAddr) Network() string { return "pipe" }
func (pipeAddr) String() string  { return "pipe" }

// streamConn is a stream as a net.Conn. Deadlines are accepted and ignored: the
// stream's end is its owner's to decide.
type streamConn struct {
	stream io.ReadWriteCloser
	once   sync.Once
	err    error
	closed chan struct{}
}

var _ net.Conn = (*streamConn)(nil)

func newStreamConn(stream io.ReadWriteCloser) *streamConn {
	return &streamConn{
		stream: stream,
		closed: make(chan struct{}),
	}
}

func (c *streamConn) Read(p []byte) (int, error)  { return c.stream.Read(p) }
func (c *streamConn) Write(p []byte) (int, error) { return c.stream.Write(p) }

// Close closes the stream once, whoever calls it first.
func (c *streamConn) Close() error {
	c.once.Do(func() {
		c.err = c.stream.Close()
		close(c.closed)
	})
	return c.err
}

func (c *streamConn) LocalAddr() net.Addr              { return pipeAddr{} }
func (c *streamConn) RemoteAddr() net.Addr             { return pipeAddr{} }
func (c *streamConn) SetDeadline(time.Time) error      { return nil }
func (c *streamConn) SetReadDeadline(time.Time) error  { return nil }
func (c *streamConn) SetWriteDeadline(time.Time) error { return nil }

// connListener accepts its one conn, then blocks until it is closed.
type connListener struct {
	conns chan net.Conn
	once  sync.Once
	done  chan struct{}
}

func newConnListener(conn net.Conn) *connListener {
	conns := make(chan net.Conn, 1)
	conns <- conn
	return &connListener{
		conns: conns,
		done:  make(chan struct{}),
	}
}

func (l *connListener) Accept() (net.Conn, error) {
	select {
	case conn := <-l.conns:
		return conn, nil
	case <-l.done:
		return nil, net.ErrClosed
	}
}

func (l *connListener) Close() error {
	l.once.Do(func() { close(l.done) })
	return nil
}

func (l *connListener) Addr() net.Addr { return pipeAddr{} }
