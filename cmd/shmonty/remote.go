package main

import (
	"fmt"
	"os"

	monty "github.com/mdfranz/gomonty"
	"github.com/mdfranz/gomonty/remote"
)

// replBackend is what newRepl hands to monty.NewRepl. It is nil (the native
// library) unless --remote was given.
var replBackend monty.Backend

// remoteEndpoint is the --remote host:port, or "" when running locally.
var remoteEndpoint string

// workerKind and workerAttrs say where the interpreter runs, for telemetry.
// The endpoint is host:port only; the token is never included.
func workerKind() string {
	if remoteEndpoint != "" {
		return "remote"
	}
	return "local"
}

func workerAttrs() []any {
	attrs := []any{"worker", workerKind()}
	if remoteEndpoint != "" {
		attrs = append(attrs, "worker_endpoint", remoteEndpoint)
	}
	return attrs
}

// remoteTransport is closed on exit when --remote is in use.
var remoteTransport *remote.GRPCTransport

// parseRemoteFlag removes "--remote host:port" or "--remote=host:port" from
// args and returns the endpoint, or "" if the flag is absent.
func parseRemoteFlag(args []string) (endpoint string, rest []string, err error) {
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--remote":
			if i+1 >= len(args) {
				return "", nil, fmt.Errorf("--remote needs a host:port")
			}
			endpoint = args[i+1]
			i++
		case len(a) > len("--remote=") && a[:len("--remote=")] == "--remote=":
			endpoint = a[len("--remote="):]
		default:
			rest = append(rest, a)
		}
	}
	return endpoint, rest, nil
}

// useRemote points every later newRepl at a remote worker. The token comes
// from MONTY_REMOTE_TOKEN and is sent only over TLS; MONTY_REMOTE_INSECURE=1
// dials without TLS for a local worker (and refuses a token).
func useRemote(endpoint string) error {
	t, err := remote.DialGRPC(endpoint, remote.GRPCConfig{
		Token:    os.Getenv("MONTY_REMOTE_TOKEN"),
		Insecure: os.Getenv("MONTY_REMOTE_INSECURE") == "1",
	})
	if err != nil {
		return err
	}
	remoteTransport = t
	remoteEndpoint = endpoint
	// Sessions are opened with type checking available, because it can only be
	// set when a session starts. Every feed skips it unless /typecheck is on,
	// so nothing changes until it is asked for.
	replBackend = remote.NewBackend(t, remote.WithTypeCheck(remote.TypeCheckOptions{Format: remote.TypeCheckConcise}))
	return nil
}

func newRepl() (*monty.Repl, error) {
	return newNamedRepl("shmonty.py")
}

func newNamedRepl(scriptName string) (*monty.Repl, error) {
	return monty.NewRepl(monty.ReplOptions{ScriptName: scriptName, Backend: replBackend})
}
