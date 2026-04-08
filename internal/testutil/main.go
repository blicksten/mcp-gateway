// Package main implements a minimal MCP server for testing.
// It registers echo, add, and fail tools, then serves via stdio, HTTP, or SSE.
//
// Usage:
//
//	mock-server                           # stdio (default)
//	mock-server --transport=http --port=0 # HTTP on random port
//	mock-server --transport=sse  --port=0 # SSE on random port
//
// When serving over HTTP/SSE, prints "READY port=<N>" to stdout once the
// listener is bound so that test harnesses can detect readiness.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type EchoInput struct {
	Message string `json:"message"`
}

type AddInput struct {
	A float64 `json:"a"`
	B float64 `json:"b"`
}

type TextOutput struct {
	Result string `json:"result"`
}

func buildServer() *mcp.Server {
	server := mcp.NewServer(&mcp.Implementation{
		Name:    "mock-server",
		Version: "1.0.0",
	}, nil)

	// Tool: echo — returns the message as-is
	mcp.AddTool(server, &mcp.Tool{Name: "echo", Description: "Echo the message back"},
		func(ctx context.Context, req *mcp.CallToolRequest, input EchoInput) (*mcp.CallToolResult, TextOutput, error) {
			return nil, TextOutput{Result: input.Message}, nil
		},
	)

	// Tool: add — adds two numbers
	mcp.AddTool(server, &mcp.Tool{Name: "add", Description: "Add two numbers"},
		func(ctx context.Context, req *mcp.CallToolRequest, input AddInput) (*mcp.CallToolResult, TextOutput, error) {
			return nil, TextOutput{Result: fmt.Sprintf("%g", input.A+input.B)}, nil
		},
	)

	// Tool: fail — always returns an error
	type FailInput struct {
		Reason string `json:"reason"`
	}
	mcp.AddTool(server, &mcp.Tool{Name: "fail", Description: "Always fails"},
		func(ctx context.Context, req *mcp.CallToolRequest, input FailInput) (*mcp.CallToolResult, any, error) {
			return nil, nil, fmt.Errorf("intentional failure: %s", input.Reason)
		},
	)

	return server
}

func main() {
	fs := flag.NewFlagSet("mock-server", flag.ContinueOnError)
	fs.SetOutput(io.Discard) // suppress usage on unknown flags
	transport := fs.String("transport", "stdio", "Transport type: stdio, http, or sse")
	port := fs.Int("port", 0, "Port for HTTP/SSE transport (0 = random)")
	authToken := fs.String("auth-token", "", "If set, require Authorization: Bearer <token> header")
	// Ignore unknown flags — tests may pass arbitrary args (e.g., --old, --new)
	// to trigger config-change detection in Reconcile tests.
	_ = fs.Parse(os.Args[1:])

	server := buildServer()

	switch *transport {
	case "stdio":
		if err := server.Run(context.Background(), &mcp.StdioTransport{}); err != nil {
			log.Fatal(err)
		}

	case "http":
		serveHTTP(server, *port, *authToken)

	case "sse":
		serveSSE(server, *port, *authToken)

	default:
		log.Fatalf("unknown transport %q (must be stdio, http, or sse)", *transport)
	}
}

func serveHTTP(server *mcp.Server, port int, authToken string) {
	handler := mcp.NewStreamableHTTPHandler(
		func(r *http.Request) *mcp.Server { return server }, nil,
	)

	mux := http.NewServeMux()
	var h http.Handler = handler
	if authToken != "" {
		h = requireAuth(authToken, handler)
	}
	mux.Handle("/mcp", h)
	mux.Handle("/mcp/", h)

	listenAndServe(mux, port)
}

func serveSSE(server *mcp.Server, port int, authToken string) {
	handler := mcp.NewSSEHandler(
		func(r *http.Request) *mcp.Server { return server }, nil,
	)

	mux := http.NewServeMux()
	var h http.Handler = handler
	if authToken != "" {
		h = requireAuth(authToken, handler)
	}
	mux.Handle("/sse", h)
	mux.Handle("/sse/", h)

	listenAndServe(mux, port)
}

// requireAuth returns middleware that rejects requests without a valid
// Authorization: Bearer <token> header.
func requireAuth(token string, next http.Handler) http.Handler {
	expected := "Bearer " + token
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != expected {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func listenAndServe(handler http.Handler, port int) {
	listener, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		log.Fatal(err)
	}

	actualPort := listener.Addr().(*net.TCPAddr).Port
	// Readiness signal — tests parse this line to discover the bound port.
	fmt.Fprintf(os.Stdout, "READY port=%d\n", actualPort)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	srv := &http.Server{
		Handler:      handler,
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 60 * time.Second,
		IdleTimeout:  60 * time.Second,
	}
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
	}()

	if err := srv.Serve(listener); err != nil && err != http.ErrServerClosed {
		log.Fatal(err)
	}
}
