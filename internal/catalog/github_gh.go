package catalog

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
)

type boundedOutput struct {
	mu       sync.Mutex
	buf      bytes.Buffer
	limit    int64
	overflow bool
	cancel   context.CancelFunc
}

func (w *boundedOutput) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.overflow {
		return 0, ErrContentTooLarge
	}
	if int64(w.buf.Len())+int64(len(p)) > w.limit {
		w.overflow = true
		w.cancel()
		return 0, ErrContentTooLarge
	}
	return w.buf.Write(p)
}

func (r ExecGH) Run(ctx context.Context, argv []string, stdout, _ io.Writer) error {
	ctx, cancel := context.WithTimeout(ctx, FetchTimeout)
	defer cancel()
	bounded := &boundedOutput{limit: MaxAPIBytes + MaxHeaderBytes + 1, cancel: cancel}
	cmd := exec.CommandContext(ctx, "gh", argv...)
	cmd.Stdout = bounded
	cmd.Stderr = io.Discard
	cmd.WaitDelay = time.Second
	for _, v := range os.Environ() {
		if !strings.HasPrefix(v, "GH_DEBUG=") && !strings.HasPrefix(v, "GH_PROMPT_DISABLED=") && !strings.HasPrefix(v, "GH_PAGER=") {
			cmd.Env = append(cmd.Env, v)
		}
	}
	cmd.Env = append(cmd.Env, "GH_PROMPT_DISABLED=1", "GH_PAGER=cat")
	err := cmd.Run()
	if bounded.overflow {
		return ErrContentTooLarge
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if _, writeErr := stdout.Write(bounded.buf.Bytes()); writeErr != nil {
		return writeErr
	}
	return err
}

func (a *GHAPI) Get(ctx context.Context, endpoint string) (APIResponse, error) {
	if err := validateEndpoint(endpoint); err != nil {
		return APIResponse{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, FetchTimeout)
	defer cancel()
	out := &boundedOutput{limit: MaxAPIBytes + MaxHeaderBytes + 1, cancel: cancel}
	runner := a.Runner
	if runner == nil {
		runner = ExecGH{}
	}
	err := runner.Run(ctx, []string{"api", "--hostname", "github.com", "--method", "GET", "--include", "-H", "Accept: application/vnd.github+json", "-H", "X-GitHub-Api-Version: 2026-03-10", endpoint}, out, io.Discard)
	if out.overflow || errors.Is(err, ErrContentTooLarge) {
		return APIResponse{}, ErrContentTooLarge
	}
	if ctx.Err() != nil {
		return APIResponse{}, ctx.Err()
	}
	raw := out.buf.Bytes()
	end := bytes.Index(raw, []byte("\r\n\r\n"))
	delimiter := 4
	if end < 0 {
		end = bytes.Index(raw, []byte("\n\n"))
		delimiter = 2
	}
	if end < 0 || int64(end+delimiter) > MaxHeaderBytes {
		if int64(len(raw)) > MaxHeaderBytes {
			return APIResponse{}, ErrContentTooLarge
		}
		if errors.Is(err, exec.ErrNotFound) || errors.Is(err, os.ErrNotExist) {
			return APIResponse{}, ErrGHRequired
		}
		return APIResponse{}, ErrUnauthorized
	}
	resp, parseErr := http.ReadResponse(bufio.NewReader(bytes.NewReader(raw)), nil)
	if parseErr != nil {
		return APIResponse{}, ErrUnauthorized
	}
	defer resp.Body.Close()
	if resp.Proto != "HTTP/1.1" && resp.Proto != "HTTP/2.0" {
		return APIResponse{}, ErrContentInvalid
	}
	// gh --include prints the decoded payload after the headers. Bound the
	// actual bytes independently of any Content-Length in those headers.
	body := raw[end+delimiter:]
	if int64(len(body)) > MaxAPIBytes {
		return APIResponse{}, ErrContentTooLarge
	}
	return APIResponse{Status: resp.StatusCode, Header: resp.Header.Clone(), Body: body}, nil
}
