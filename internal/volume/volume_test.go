package volume

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeReader is a volume.Reader over an in-memory stream. openErr, if set, fails
// Open; closeErr is reported by the returned stream's Close.
type fakeReader struct {
	data     string
	openErr  error
	closeErr error
	closed   bool
}

func (r *fakeReader) Open(context.Context) (io.ReadCloser, error) {
	if r.openErr != nil {
		return nil, r.openErr
	}
	return &fakeStream{Reader: strings.NewReader(r.data), onClose: func() { r.closed = true }, err: r.closeErr}, nil
}

func (r *fakeReader) DebugInfo() map[string]any { return map[string]any{"kind": "fake"} }

type fakeStream struct {
	*strings.Reader
	onClose func()
	err     error
}

func (s *fakeStream) Close() error {
	s.onClose()
	return s.err
}

// fakeWriter is a volume.Writer that drains the stream into a buffer. writeErr,
// if set, fails after (optionally) consuming the stream.
type fakeWriter struct {
	got      string
	writeErr error
	drain    bool // read the stream even when returning writeErr
}

func (w *fakeWriter) Write(_ context.Context, r io.Reader) error {
	if w.writeErr != nil && !w.drain {
		return w.writeErr
	}
	data, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	w.got = string(data)
	return w.writeErr
}

func (w *fakeWriter) DebugInfo() map[string]any { return map[string]any{"kind": "fake"} }

func TestCopy(t *testing.T) {
	tests := []struct {
		name        string
		reader      *fakeReader
		writer      *fakeWriter
		wantErr     string
		wantContent string
	}{
		{
			name:        "clean copy streams content and closes source",
			reader:      &fakeReader{data: "hello world"},
			writer:      &fakeWriter{},
			wantContent: "hello world",
		},
		{
			name:    "source open error propagates",
			reader:  &fakeReader{openErr: errors.New("open boom")},
			writer:  &fakeWriter{},
			wantErr: "open source",
		},
		{
			name:    "writer error propagates and source still closes",
			reader:  &fakeReader{data: "payload"},
			writer:  &fakeWriter{writeErr: errors.New("write boom"), drain: true},
			wantErr: "write archive",
		},
		{
			name:    "source close error surfaces on clean copy",
			reader:  &fakeReader{data: "payload", closeErr: errors.New("close boom")},
			writer:  &fakeWriter{},
			wantErr: "close source",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Act
			err := Copy(context.Background(), tt.reader, tt.writer)

			// Assert
			if tt.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)
			} else {
				require.NoError(t, err)
				assert.Equal(t, tt.wantContent, tt.writer.got)
			}
			// The source is always closed once opened, even on a write error.
			if tt.reader.openErr == nil {
				assert.True(t, tt.reader.closed, "source stream must be closed")
			}
		})
	}
}
