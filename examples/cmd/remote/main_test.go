package main

import (
	"context"
	"net"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/mdfranz/gomonty/remote"
)

func TestDialGRPCRejectsTokenWhenInsecure(t *testing.T) {
	if _, err := remote.DialGRPC("127.0.0.1:50051", remote.GRPCConfig{
		Token:    "secret",
		Insecure: true,
	}); err == nil {
		t.Fatal("DialGRPC unexpectedly allowed Token with Insecure=true")
	}
}

func TestDialRefusedWithoutWorker(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	if _, err := run(ctx, addr, "", true); err == nil {
		t.Fatal("expected error when no worker is listening")
	}
}

func TestRunAgainstRealWorker(t *testing.T) {
	endpoint := os.Getenv("MONTY_REMOTE_ENDPOINT")
	if endpoint == "" {
		t.Skip("MONTY_REMOTE_ENDPOINT not set")
	}

	insecure, _ := strconv.ParseBool(os.Getenv("MONTY_REMOTE_INSECURE"))
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	got, err := run(ctx, endpoint, os.Getenv("MONTY_REMOTE_TOKEN"), insecure)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if !strings.Contains(got, "result: 42") {
		t.Fatalf("output = %q, want result: 42", got)
	}
}
