package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"strconv"
	"strings"
	"time"

	monty "github.com/mdfranz/gomonty"
	"github.com/mdfranz/gomonty/remote"
)

func main() {
	endpoint := os.Getenv("MONTY_REMOTE_ENDPOINT")
	if endpoint == "" {
		fmt.Println("Set MONTY_REMOTE_ENDPOINT (and optional MONTY_REMOTE_TOKEN, MONTY_REMOTE_INSECURE=1) to run against a remote worker.")
		return
	}

	insecure, _ := strconv.ParseBool(os.Getenv("MONTY_REMOTE_INSECURE"))
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	out, err := run(ctx, endpoint, os.Getenv("MONTY_REMOTE_TOKEN"), insecure)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Print(out)
}

func run(ctx context.Context, endpoint, token string, insecure bool) (string, error) {
	transport, err := remote.DialGRPC(endpoint, remote.GRPCConfig{
		Token:    token,
		Insecure: insecure,
	})
	if err != nil {
		return "", err
	}
	defer func() { _ = transport.Close() }()

	openTimeout := 5 * time.Second
	if dl, ok := ctx.Deadline(); ok {
		if d := time.Until(dl); d > 0 && d < openTimeout {
			openTimeout = d
		}
	}

	backend := remote.NewBackend(
		transport,
		remote.WithOpenTimeout(openTimeout),
		remote.WithTypeCheck(remote.TypeCheckOptions{Format: remote.TypeCheckConcise}),
	)

	repl, err := monty.NewRepl(monty.ReplOptions{
		ScriptName: "remote_example.py",
		Backend:    backend,
	})
	if err != nil {
		var refused *remote.SessionRefusedError
		if errors.As(err, &refused) {
			return "", fmt.Errorf("worker refused session (%s): %w", refused.Reason, err)
		}
		return "", err
	}

	var out strings.Builder
	val, err := repl.FeedRun(ctx, `print("from remote:", add(20, 22))
add(20, 22)`, monty.FeedOptions{
		Print: func(_, text string) { out.WriteString(text) },
		Functions: map[string]monty.ExternalFunction{
			"add": func(_ context.Context, call monty.Call) (monty.Result, error) {
				if len(call.Args) != 2 {
					return monty.Raise(monty.Exception{Type: "TypeError"}), nil
				}
				a, okA := call.Args[0].Raw().(int64)
				b, okB := call.Args[1].Raw().(int64)
				if !okA || !okB {
					return monty.Raise(monty.Exception{Type: "TypeError"}), nil
				}
				return monty.Return(monty.Int(a + b)), nil
			},
		},
	})
	if err != nil {
		var pyErr *remote.PythonError
		var lost *remote.ConnectionLostError
		switch {
		case errors.As(err, &pyErr):
			return "", fmt.Errorf("python error:\n%s", pyErr.TracebackString())
		case errors.As(err, &lost):
			return "", fmt.Errorf("worker stream lost during %s: %w", lost.Op, lost.Err)
		default:
			return "", err
		}
	}

	fmt.Fprintf(&out, "result: %v\n", val.Raw())
	return out.String(), nil
}
