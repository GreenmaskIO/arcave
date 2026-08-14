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

	dockermodel "github.com/greenmaskio/arcave/internal/provider/docker/internal/model"
)

// oneShotID is the container ID the mocked arcaved hands back for every
// throwaway container in these tests.
const oneShotID = "oneshot-1"

// The image is pulled only when the arcaved does not already have it, and the
// pull stream is drained: the arcaved does not finish a pull nobody reads.
func TestRunOneShotContainer_PullsMissingImage(t *testing.T) {
	// Arrange
	api := &mockAPI{}
	api.On("ImageInspect", mock.Anything, scratchImage).
		Return(image.InspectResponse{}, cerrdefs.ErrNotFound)
	pulled := &countingReader{ReadCloser: io.NopCloser(strings.NewReader(`{"status":"done"}`))}
	api.On("ImagePull", mock.Anything, scratchImage, image.PullOptions{}).Return(io.ReadCloser(pulled), nil)
	expectOneShotRun(api, 0)

	// Act
	err := runOneShotContainer(context.Background(), api, dockermodel.OneShotSpec{Image: scratchImage})

	// Assert
	require.NoError(t, err)
	api.AssertExpectations(t)
	assert.True(t, pulled.drained, "the pull stream must be read to completion")
}

// A non-zero exit carries the container's own output as a typed error, the way
// the CLI's combined output used to — bounded and demultiplexed, never with the
// stdcopy framing bytes the arcaved wraps a no-TTY stream in.
func TestRunOneShotContainer_NonZeroExitCarriesLogs(t *testing.T) {
	// Arrange
	var framed bytes.Buffer
	// The arcaved frames log output with stdcopy whenever the container has no TTY,
	// which every one-shot run is; feeding raw bytes here would hide exactly the
	// framing bug the demux fixes.
	_, _ = io.WriteString(stdcopy.NewStdWriter(&framed, stdcopy.Stderr), "rm: permission denied\n")

	api := &mockAPI{}
	expectImagePresent(api, scratchImage)
	expectOneShotRun(api, 137)
	api.On("ContainerLogs", mock.Anything, oneShotID, mock.Anything).
		Return(io.NopCloser(bytes.NewReader(framed.Bytes())), nil)

	// Act
	err := runOneShotContainer(context.Background(), api, dockermodel.OneShotSpec{Image: scratchImage})

	// Assert
	require.Error(t, err)
	var exitErr *dockermodel.ContainerExitError
	require.ErrorAs(t, err, &exitErr, "the exit must surface as a typed error errors.As can recover")
	assert.Equal(t, scratchImage, exitErr.Image)
	assert.Equal(t, int64(137), exitErr.StatusCode)
	assert.Equal(t, []string{"rm: permission denied"}, exitErr.Output)

	// The compact message names image, status and a short tail, with no framing byte.
	assert.Equal(t, "busybox container exited with status 137: rm: permission denied", err.Error())
	assert.NotContains(t, err.Error(), "\x01", "the stdcopy header must be demultiplexed away")

	// The read is bounded at the arcaved.
	opts := api.Calls[callIndex(api, "ContainerLogs")].Arguments.Get(2).(container.LogsOptions)
	assert.Equal(t, "200", opts.Tail, "the log read must be bounded at the arcaved")
}

func TestRunOneShotContainer_PropagatesCreateError(t *testing.T) {
	// Arrange
	api := &mockAPI{}
	expectImagePresent(api, scratchImage)
	api.On("ContainerCreate", mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything).
		Return(container.CreateResponse{}, errors.New("boom"))

	// Act
	err := runOneShotContainer(context.Background(), api, dockermodel.OneShotSpec{Image: scratchImage})

	// Assert
	require.Error(t, err)
	assert.Contains(t, err.Error(), "boom")
}

// A container that fails to start still exists, so it is still removed — the
// deferred cleanup covers every path after the create, not just the happy one.
func TestRunOneShotContainer_RemovesContainerOnStartFailure(t *testing.T) {
	// Arrange
	api := &mockAPI{}
	expectImagePresent(api, scratchImage)
	api.On("ContainerCreate", mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything).
		Return(container.CreateResponse{ID: oneShotID}, nil)
	api.On("ContainerStart", mock.Anything, oneShotID, container.StartOptions{}).Return(errors.New("no such image"))
	api.On("ContainerRemove", mock.Anything, oneShotID, container.RemoveOptions{Force: true}).Return(nil)

	// Act
	err := runOneShotContainer(context.Background(), api, dockermodel.OneShotSpec{Image: scratchImage})

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
		spec       dockermodel.OneShotSpec
		wantAttach bool
		wantCfg    container.Config
	}{
		{
			name: "no stdio does not attach at all",
			spec: dockermodel.OneShotSpec{Image: scratchImage},
		},
		{
			name:       "stdin opens and attaches stdin",
			spec:       dockermodel.OneShotSpec{Image: scratchImage, Stdin: strings.NewReader("")},
			wantAttach: true,
			wantCfg:    container.Config{OpenStdin: true, StdinOnce: true, AttachStdin: true},
		},
		{
			name:       "output receivers attach their own streams",
			spec:       dockermodel.OneShotSpec{Image: scratchImage, Stdout: io.Discard, Stderr: io.Discard},
			wantAttach: true,
			wantCfg:    container.Config{AttachStdout: true, AttachStderr: true},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			api := &mockAPI{}
			expectImagePresent(api, scratchImage)
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
	expectImagePresent(api, scratchImage)
	expectOneShotRun(api, 0)
	api.On("ContainerAttach", mock.Anything, oneShotID, mock.Anything).Return(hj.resp, nil)

	var stdout, stderr bytes.Buffer

	// Act
	err := runOneShotContainer(context.Background(), api, dockermodel.OneShotSpec{
		Image:  scratchImage,
		Stdin:  strings.NewReader("to stdin"),
		Stdout: &stdout,
		Stderr: &stderr,
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
	payload := bytes.Repeat([]byte("arcave"), 1<<20) // 2 MiB, far past any pipe buffer

	hj := newFakeHijack()
	go func() {
		// The container echoes as it reads — so an implementation that filled
		// stdin before reading any stdout would deadlock here rather than fail.
		_, _ = io.Copy(hj.stdout, hj.stdinR)
		hj.closeOutput()
	}()

	api := &mockAPI{}
	expectImagePresent(api, scratchImage)
	expectOneShotRun(api, 0)
	api.On("ContainerAttach", mock.Anything, oneShotID, mock.Anything).Return(hj.resp, nil)

	var stdout bytes.Buffer

	// Act
	done := make(chan error, 1)
	go func() {
		done <- runOneShotContainer(context.Background(), api, dockermodel.OneShotSpec{
			Image:  scratchImage,
			Stdin:  bytes.NewReader(payload),
			Stdout: &stdout,
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

// A failure reading the caller's own stdin surfaces as an error — told apart
// from the container merely exiting without draining its input, which is not one.
// This is the single case recordingReader exists for: without it the errgroup
// could not attribute a failed io.Copy to the read side.
func TestRunOneShotContainer_StdinReadError(t *testing.T) {
	// Arrange
	hj := newFakeHijack()
	hj.closeOutput() // the container prints nothing, so the reader half returns at once

	api := &mockAPI{}
	expectImagePresent(api, scratchImage)
	expectOneShotRun(api, 0)
	api.On("ContainerAttach", mock.Anything, oneShotID, mock.Anything).Return(hj.resp, nil)

	// Act
	err := runOneShotContainer(context.Background(), api, dockermodel.OneShotSpec{
		Image: scratchImage,
		Stdin: &errReader{err: errors.New("source gone")},
	})

	// Assert
	require.Error(t, err)
	assert.Contains(t, err.Error(), "container input", "the failure must be attributed to the read side")
	assert.Contains(t, err.Error(), "source gone", "the source's own error must be wrapped")
}

// A cancelled run ends, and takes its container with it: the hijacked stream is
// broken by hand, and the removal travels on a context the cancellation cannot
// refuse. Both halves compile clean when missing, and a quiet container hangs.
func TestRunOneShotContainer_CancellationEndsTheRun(t *testing.T) {
	// Arrange: a container that prints nothing and never exits.
	hj := newFakeHijack()

	api := &mockAPI{}
	expectImagePresent(api, scratchImage)
	api.On("ContainerCreate", mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything).
		Return(container.CreateResponse{ID: oneShotID}, nil)
	api.On("ContainerStart", mock.Anything, oneShotID, container.StartOptions{}).Return(nil)
	api.On("ContainerAttach", mock.Anything, oneShotID, mock.Anything).Return(hj.resp, nil)
	api.On("ContainerRemove", mock.Anything, oneShotID, container.RemoveOptions{Force: true}).Return(nil)

	ctx, cancel := context.WithCancel(context.Background())
	var stdout bytes.Buffer

	// Act
	done := make(chan error, 1)
	go func() {
		done <- runOneShotContainer(ctx, api, dockermodel.OneShotSpec{Image: scratchImage, Stdout: &stdout})
	}()
	cancel()

	// Assert
	select {
	case err := <-done:
		require.ErrorIs(t, err, context.Canceled, "the cause must be the cancellation, not the broken stream")
	case <-time.After(30 * time.Second):
		t.Fatal("a cancelled run must not outlive its context")
	}
	api.AssertExpectations(t)

	removeCtx, ok := api.Calls[callIndex(api, "ContainerRemove")].Arguments.Get(0).(context.Context)
	require.True(t, ok)
	assert.NoError(t, removeCtx.Err(), "the removal must not travel on the cancelled context")
}

// RunOneShot is the exported face of the mechanism, for the runner seams the
// backends declare: the spec's command, environment and binds reach the arcaved
// verbatim.
func TestEngine_RunOneShot(t *testing.T) {
	// Arrange
	const resticImage = "restic/restic:0.19.1"
	api := &mockAPI{}
	expectImagePresent(api, resticImage)
	expectOneShotRun(api, 0)

	spec := dockermodel.OneShotSpec{
		Image: resticImage,
		Cmd:   []string{"init"},
		Env:   []string{"RESTIC_REPOSITORY=rest:http://arcave:token@host.docker.internal:1/repo1/"},
		Binds: []string{"vol:/data"},
	}

	// Act
	err := NewEngine(api).RunOneShot(context.Background(), spec)

	// Assert
	require.NoError(t, err)
	api.AssertExpectations(t)
	cfg := createdConfig(t, api)
	assert.Equal(t, spec.Cmd, []string(cfg.Cmd))
	assert.Equal(t, spec.Env, cfg.Env)
	hostCfg := api.Calls[callIndex(api, "ContainerCreate")].Arguments.Get(2).(*container.HostConfig)
	assert.Equal(t, spec.Binds, hostCfg.Binds)
}

// expectImagePresent makes the arcaved report ref as already pulled.
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

// errReader fails every read with err, standing in for a caller's stdin source
// that goes bad mid-stream.
type errReader struct{ err error }

func (r *errReader) Read([]byte) (int, error) { return 0, r.err }

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

// fakeHijack stands in for the arcaved side of an attached container, with no
// arcaved in sight: whatever runOneShotContainer writes as stdin comes out of stdinR, and
// whatever the test writes to stdout/stderr is framed exactly the way the arcaved
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
	r  *io.PipeReader
	w  *io.PipeWriter
	hj *fakeHijack
}

func (c *fakeConn) Read(p []byte) (int, error)  { return c.r.Read(p) }
func (c *fakeConn) Write(p []byte) (int, error) { return c.w.Write(p) }

// Close breaks both directions, as closing a real conn does — which is what a
// cancelled run relies on to unblock its reader.
func (c *fakeConn) Close() error {
	_ = c.r.CloseWithError(net.ErrClosed)
	return c.w.Close()
}

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
