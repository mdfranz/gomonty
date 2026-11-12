// Package remote runs gomonty REPL sessions on a remote Monty worker over the
// monty.v1 protocol, instead of in-process through the native library.
//
//	transport, err := remote.DialGRPC("worker.example:443", remote.GRPCConfig{Token: token})
//	if err != nil { ... }
//	defer transport.Close()
//
//	repl, err := monty.NewRepl(monty.ReplOptions{Backend: remote.NewBackend(transport)})
//	value, err := repl.FeedRun(ctx, "1 + 1", monty.FeedOptions{})
//
// Each REPL is one session on its own stream. Closing the transport ends every
// session; a REPL that becomes unreachable is also released by a finalizer.
//
// The Backend API it implements is experimental (see monty.Backend). This
// version supports REPLs only, and runs scripts that need nothing from the
// host: a script that calls a host function, reads a name from the host or
// makes an OS call is stopped with ErrUnsupported. Runners, Dump and snapshots
// are not supported yet.
//
// The vendored protocol definition is in proto/ and the Go code generated from
// it is in internal/montyv1; see proto/UPSTREAM for the pinned upstream
// version.
package remote
