package mcpclient

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestBlockedStdioWriteCanceled(t *testing.T) {
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	defer writer.Close()
	tr := &pipeTransport{IOTransport: &mcp.IOTransport{Reader: reader, Writer: writer}, writer: writer}
	conn, err := tr.Connect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- conn.Write(ctx, &jsonrpc.Request{Method: "tools/call", Params: []byte(`{"text":"` + strings.Repeat("x", 1024*1024) + `"}`)})
	}()
	// Reading the prefix proves the writer entered the pipe before cancellation.
	var prefix [1]byte
	if _, err := reader.Read(prefix[:]); err != nil {
		t.Fatal(err)
	}
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("write succeeded")
		}
	case <-time.After(time.Second):
		writer.Close()
		<-done
		t.Fatal("cancellation did not interrupt pipe write")
	}
}
