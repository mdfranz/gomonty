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
// The Backend API it implements is experimental (see monty.Backend). Scripts
// can call host functions, read names from the host, make OS calls through the
// host's OSHandler and wait on futures, as they can locally. This version
// supports REPLs only: runners, Dump and snapshots are not supported yet, and a
// script that uses a method or attribute of a host object is stopped with
// ErrUnsupported.
//
// The vendored protocol definition is in proto/ and the Go code generated from
// it is in internal/montyv1; see proto/UPSTREAM for the pinned upstream
// version.
package remote
