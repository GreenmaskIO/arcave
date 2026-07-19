package docker

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"slices"
	"strings"
	"testing"
	"time"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/pkg/stdcopy"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

// oneShotID is the container ID the mocked daemon hands back for every
// throwaway container in these tests.
const oneShotID = "oneshot-1"

// The image is pulled only when the daemon does not already have it, and the
// pull stream is drained: the daemon does not finish a pull nobody reads.
func TestRunOneShotContainer_PullsMissingImage(t *testing.T) {
	// Arrange
	api := &mockAPI{}
	api.On("ImageInspect", mock.Anything, wipeImage).
		Return(image.InspectResponse{}, cerrdefs.ErrNotFound)
	pulled := &countingReader{ReadCloser: io.NopCloser(strings.NewReader(`{"status":"done"}`))}
	api.On("ImagePull", mock.Anything, wipeImage, image.PullOptions{}).Return(io.ReadCloser(pulled), nil)
	expectOneShotRun(api, 0)

	// Act
	err := runOneShotContainer(context.Background(), api, oneShotSpec{image: wipeImage})

	// Assert
	require.NoError(t, err)
	api.AssertExpectations(t)
	assert.True(t, pulled.drained, "the pull stream must be read to completion")
}

// A non-zero exit carries the container's own output, the way the CLI's
// combined output used to.
func TestRunOneShotContainer_NonZeroExitCarriesLogs(t *testing.T) {
	// Arrange
	api := &mockAPI{}
	expectImagePresent(api, wipeImage)
	expectOneShotRun(api, 137)
	api.On("ContainerLogs", mock.Anything, oneShotID, mock.Anything).
		Return(io.NopCloser(strings.NewReader("rm: permission denied\n")), nil)

	// Act
	err := runOneShotContainer(context.Background(), api, oneShotSpec{image: wipeImage})

	// Assert
	require.Error(t, err)
	assert.Contains(t, err.Error(), "status 137")
	assert.Contains(t, err.Error(), "rm: permission denied")
}

func TestRunOneShotContainer_PropagatesCreateError(t *testing.T) {
	// Arrange
	api := &mockAPI{}
	expectImagePresent(api, wipeImage)
	api.On("ContainerCreate", mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything).
		Return(container.CreateResponse{}, errors.New("boom"))

	// Act
	err := runOneShotContainer(context.Background(), api, oneShotSpec{image: wipeImage})

	// Assert
	require.Error(t, err)
	assert.Contains(t, err.Error(), "boom")
}

// A container that fails to start still exists, so it is still removed — the
// deferred cleanup covers every path after the create, not just the happy one.
func TestRunOneShotContainer_RemovesContainerOnStartFailure(t *testing.T) {
	// Arrange
	api := &mockAPI{}
	expectImagePresent(api, wipeImage)
	api.On("ContainerCreate", mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything).
		Return(container.CreateResponse{ID: oneShotID}, nil)
	api.On("ContainerStart", mock.Anything, oneShotID, container.StartOptions{}).Return(errors.New("no such image"))
	api.On("ContainerRemove", mock.Anything, oneShotID, container.RemoveOptions{Force: true}).Return(nil)

	// Act
	err := runOneShotContainer(context.Background(), api, oneShotSpec{image: wipeImage})

	// Assert
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no such image")
	api.AssertExpectations(t)
}

// The stdio fields alone decide whether the container is created attachable —
// which is what keeps a wipe run (no stdio) exactly as it was.
func TestRunOneShotContainer_AttachConfig(t *testing.T) {
	tests := []struct {
		name       string
		spec       oneShotSpec
		wantAttach bool
		wantCfg    container.Config
	}{
		{
			name: "no stdio does not attach at all",
			spec: oneShotSpec{image: wipeImage},
		},
		{
			name:       "stdin opens and attaches stdin",
			spec:       oneShotSpec{image: wipeImage, stdin: strings.NewReader("")},
			wantAttach: true,
			wantCfg:    container.Config{OpenStdin: true, StdinOnce: true, AttachStdin: true},
		},
		{
			name:       "output receivers attach their own streams",
			spec:       oneShotSpec{image: wipeImage, stdout: io.Discard, stderr: io.Discard},
			wantAttach: true,
			wantCfg:    container.Config{AttachStdout: true, AttachStderr: true},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			api := &mockAPI{}
			expectImagePresent(api, wipeImage)
			expectOneShotRun(api, 0)
			if tt.wantAttach {
				hj := newFakeHijack()
				hj.closeOutput()
				api.On("ContainerAttach", mock.Anything, oneShotID, mock.Anything).Return(hj.resp, nil)
			}

			// Act
			err := runOneShotContainer(context.Background(), api, tt.spec)

			// Assert
			require.NoError(t, err)
			cfg := createdConfig(t, api)
			assert.Equal(t, tt.wantCfg.OpenStdin, cfg.OpenStdin)
			assert.Equal(t, tt.wantCfg.StdinOnce, cfg.StdinOnce)
			assert.Equal(t, tt.wantCfg.AttachStdin, cfg.AttachStdin)
			assert.Equal(t, tt.wantCfg.AttachStdout, cfg.AttachStdout)
			assert.Equal(t, tt.wantCfg.AttachStderr, cfg.AttachStderr)
			assert.False(t, cfg.Tty, "a tty would collapse stdout and stderr into one stream")
			if !tt.wantAttach {
				api.AssertNotCalled(t, "ContainerAttach")
			}
		})
	}
}

// The caller's stdin reaches the container verbatim and is half-closed after,
// and the container's framed output is demultiplexed back into the caller's own
// receivers.
func TestRunOneShotContainer_Stdio(t *testing.T) {
	// Arrange
	hj := newFakeHijack()
	stdinGot := hj.collectStdin()
	go func() {
		_, _ = io.WriteString(hj.stdout, "to stdout")
		_, _ = io.WriteString(hj.stderr, "to stderr")
		hj.closeOutput()
	}()

	api := &mockAPI{}
	expectImagePresent(api, wipeImage)
	expectOneShotRun(api, 0)
	api.On("ContainerAttach", mock.Anything, oneShotID, mock.Anything).Return(hj.resp, nil)

	var stdout, stderr bytes.Buffer

	// Act
	err := runOneShotContainer(context.Background(), api, oneShotSpec{
		image:  wipeImage,
		stdin:  strings.NewReader("to stdin"),
		stdout: &stdout,
		stderr: &stderr,
	})

	// Assert
	require.NoError(t, err)
	assert.Equal(t, "to stdout", stdout.String())
	assert.Equal(t, "to stderr", stderr.String())
	assert.Equal(t, "to stdin", string(<-stdinGot))
	assertClosedWrite(t, hj, "stdin must be half-closed so the process sees EOF")

	// The attach has to precede the start, or the first output is written before
	// anyone is listening and is lost.
	attachAt := callIndex(api, "ContainerAttach")
	startAt := callIndex(api, "ContainerStart")
	require.NotEqual(t, -1, attachAt)
	assert.Less(t, attachAt, startAt, "attach must happen before start")
}

// A payload larger than any pipe buffer completes: the writer and the reader
// run concurrently, so neither side waits on the other to finish first.
func TestRunOneShotContainer_StdioLargePayload(t *testing.T) {
	// Arrange
	payload := bytes.Repeat([]byte("gm"), 1<<20) // 2 MiB, far past any pipe buffer

	hj := newFakeHijack()
	go func() {
		// The container echoes as it reads — so an implementation that filled
		// stdin before reading any stdout would deadlock here rather than fail.
		_, _ = io.Copy(hj.stdout, hj.stdinR)
		hj.closeOutput()
	}()

	api := &mockAPI{}
	expectImagePresent(api, wipeImage)
	expectOneShotRun(api, 0)
	api.On("ContainerAttach", mock.Anything, oneShotID, mock.Anything).Return(hj.resp, nil)

	var stdout bytes.Buffer

	// Act
	done := make(chan error, 1)
	go func() {
		done <- runOneShotContainer(context.Background(), api, oneShotSpec{
			image:  wipeImage,
			stdin:  bytes.NewReader(payload),
			stdout: &stdout,
		})
	}()

	// Assert
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(30 * time.Second):
		t.Fatal("runOneShotContainer deadlocked on a payload larger than the pipe buffer")
	}
	assert.Equal(t, len(payload), stdout.Len())
	assert.True(t, bytes.Equal(payload, stdout.Bytes()), "the echoed payload must come back byte for byte")
}

// expectImagePresent makes the daemon report ref as already pulled.
func expectImagePresent(api *mockAPI, ref string) {
	api.On("ImageInspect", mock.Anything, ref).Return(image.InspectResponse{}, nil)
}

// expectOneShotRun drives one throwaway container through to the given exit status.
func expectOneShotRun(api *mockAPI, exitCode int64) {
	api.On("ContainerCreate", mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything).
		Return(container.CreateResponse{ID: oneShotID}, nil)
	api.On("ContainerStart", mock.Anything, oneShotID, container.StartOptions{}).Return(nil)

	status := make(chan container.WaitResponse, 1)
	status <- container.WaitResponse{StatusCode: exitCode}
	api.On("ContainerWait", mock.Anything, oneShotID, container.WaitConditionNotRunning).
		Return((<-chan container.WaitResponse)(status), (<-chan error)(make(chan error)))

	api.On("ContainerRemove", mock.Anything, oneShotID, container.RemoveOptions{Force: true}).Return(nil)
}

// createdConfig returns the container.Config the run handed to ContainerCreate.
func createdConfig(t *testing.T, api *mockAPI) *container.Config {
	t.Helper()

	i := callIndex(api, "ContainerCreate")
	require.NotEqual(t, -1, i, "ContainerCreate was never called")
	return api.Calls[i].Arguments.Get(1).(*container.Config)
}

// callIndex reports where method first appears in the recorded calls, or -1.
func callIndex(api *mockAPI, method string) int {
	return slices.IndexFunc(api.Calls, func(c mock.Call) bool { return c.Method == method })
}

// assertClosedWrite waits for the run to half-close the container's stdin.
func assertClosedWrite(t *testing.T, hj *fakeHijack, msg string) {
	t.Helper()

	select {
	case <-hj.closedWrite:
	case <-time.After(5 * time.Second):
		t.Fatal(msg)
	}
}

// countingReader records whether its stream was read to EOF.
type countingReader struct {
	io.ReadCloser
	drained bool
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.ReadCloser.Read(p)
	if errors.Is(err, io.EOF) {
		c.drained = true
	}
	return n, err
}

// fakeHijack stands in for the daemon side of an attached container, with no
// daemon in sight: whatever runOneShotContainer writes as stdin comes out of stdinR, and
// whatever the test writes to stdout/stderr is framed exactly the way the daemon
// multiplexes the two back over one connection.
type fakeHijack struct {
	resp types.HijackedResponse

	// stdinR is the container's end of stdin — what the "process" reads.
	stdinR *io.PipeReader
	// stdout and stderr frame what the "process" prints.
	stdout io.Writer
	stderr io.Writer

	outW        *io.PipeWriter
	closedWrite chan struct{}
}

func newFakeHijack() *fakeHijack {
	stdinR, stdinW := io.Pipe()
	outR, outW := io.Pipe()

	f := &fakeHijack{
		stdinR:      stdinR,
		stdout:      stdcopy.NewStdWriter(outW, stdcopy.Stdout),
		stderr:      stdcopy.NewStdWriter(outW, stdcopy.Stderr),
		outW:        outW,
		closedWrite: make(chan struct{}),
	}
	f.resp = types.NewHijackedResponse(&fakeConn{r: outR, w: stdinW, hj: f}, "")
	return f
}

// collectStdin drains the container's stdin in the background and delivers all
// of it once the stream ends.
func (f *fakeHijack) collectStdin() <-chan []byte {
	got := make(chan []byte, 1)
	go func() {
		b, _ := io.ReadAll(f.stdinR)
		got <- b
	}()
	return got
}

// closeOutput ends the container's output stream, which is what lets the run's
// reader half return.
func (f *fakeHijack) closeOutput() { _ = f.outW.Close() }

// fakeConn is the net.Conn under a fake hijacked response: reads deliver the
// container's framed output, writes are its stdin. It is hand-rolled rather than
// taken from net.Pipe because the run half-closes stdin, and only a conn with
// its own CloseWrite can keep the read direction open across that.
type fakeConn struct {
	r  io.Reader
	w  *io.PipeWriter
	hj *fakeHijack
}

func (c *fakeConn) Read(p []byte) (int, error)  { return c.r.Read(p) }
func (c *fakeConn) Write(p []byte) (int, error) { return c.w.Write(p) }
func (c *fakeConn) Close() error                { return c.w.Close() }

func (c *fakeConn) CloseWrite() error {
	select {
	case <-c.hj.closedWrite:
	default:
		close(c.hj.closedWrite)
	}
	return c.w.Close()
}

func (c *fakeConn) LocalAddr() net.Addr              { return fakeAddr{} }
func (c *fakeConn) RemoteAddr() net.Addr             { return fakeAddr{} }
func (c *fakeConn) SetDeadline(time.Time) error      { return nil }
func (c *fakeConn) SetReadDeadline(time.Time) error  { return nil }
func (c *fakeConn) SetWriteDeadline(time.Time) error { return nil }

type fakeAddr struct{}

func (fakeAddr) Network() string { return "fake" }
func (fakeAddr) String() string  { return "fake" }
