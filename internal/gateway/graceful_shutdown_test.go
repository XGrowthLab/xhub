package gateway

import (
	"bufio"
	"context"
	"io"
	"net"
	"net/http"
	"sync"
	"testing"
	"time"
)

// TestGracefulGatewayStream 使用真实回环 SSE 等待信号。取消监听后继续收到完整终态，再退出网关；测试关闭监听器和响应流。
func TestGracefulGatewayStream(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	finish := make(chan struct{})
	var once sync.Once
	release := func() { once.Do(func() { close(finish) }) }
	t.Cleanup(release)
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: partial\n\n")
		w.(http.Flusher).Flush()
		select {
		case <-finish:
			_, _ = io.WriteString(w, "data: [DONE]\n\n")
		case <-r.Context().Done():
		}
	})}
	result := make(chan error, 1)
	go func() { result <- serveHTTPWithShutdown(ctx, server, listener, 3*time.Second) }()
	client := &http.Client{Timeout: 5 * time.Second}
	response, err := client.Get("http://" + listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	reader := bufio.NewReader(response.Body)
	if line, err := reader.ReadString('\n'); err != nil || line != "data: partial\n" {
		t.Fatalf("initial SSE: %q %v", line, err)
	}
	cancel()
	select {
	case err := <-result:
		t.Fatalf("gateway exited while SSE was open: %v", err)
	case <-time.After(40 * time.Millisecond):
	}
	release()
	remaining, err := io.ReadAll(reader)
	if err != nil || string(remaining) != "\ndata: [DONE]\n\n" {
		t.Fatalf("incomplete SSE: %q %v", remaining, err)
	}
	if err := <-result; err != nil {
		t.Fatalf("graceful shutdown failed: %v", err)
	}
}
