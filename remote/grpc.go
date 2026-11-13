package remote

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"strings"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"

	montyv1 "github.com/mdfranz/gomonty/remote/internal/montyv1"
)

// GRPCTransport opens sessions over the monty.v1.MontySession gRPC service.
type GRPCTransport struct {
	conn  *grpc.ClientConn
	owned bool
	token string
	meta  []string
}

// GRPCConfig configures DialGRPC.
type GRPCConfig struct {
	// Token, when set, is sent as "authorization: Bearer <token>" on every
	// session. It is never logged or included in errors.
	Token string
	// Metadata is sent as headers on every session, for example routing or
	// tenant headers. Keys are lower-cased. It must not carry a token over a
	// connection without TLS, and the "authorization" key is refused with
	// Insecure for the same reason.
	Metadata map[string]string
	// Insecure turns TLS off. It is for local workers and tests, and cannot be
	// combined with a Token, which would then cross the network in the clear.
	Insecure bool
	// DialOptions are added to the connection's options.
	DialOptions []grpc.DialOption
}

// DialGRPC connects to a worker. TLS is on unless cfg.Insecure is set.
func DialGRPC(target string, cfg GRPCConfig) (*GRPCTransport, error) {
	if cfg.Insecure && cfg.Token != "" {
		return nil, errors.New("remote: refusing to send a token without TLS")
	}
	var creds credentials.TransportCredentials
	if cfg.Insecure {
		creds = insecure.NewCredentials()
	} else {
		creds = credentials.NewTLS(&tls.Config{MinVersion: tls.VersionTLS12})
	}
	var meta []string
	for k, v := range cfg.Metadata {
		if cfg.Insecure && strings.EqualFold(k, "authorization") {
			return nil, errors.New("remote: refusing to send an authorization header without TLS")
		}
		meta = append(meta, strings.ToLower(k), v)
	}
	options := append([]grpc.DialOption{grpc.WithTransportCredentials(creds)}, cfg.DialOptions...)
	conn, err := grpc.NewClient(target, options...)
	if err != nil {
		return nil, fmt.Errorf("remote: dial %s: %w", target, err)
	}
	return &GRPCTransport{conn: conn, owned: true, token: cfg.Token, meta: meta}, nil
}

// NewGRPCTransport uses a connection the caller owns and secures. It does not
// close the connection. token may be empty.
func NewGRPCTransport(conn *grpc.ClientConn, token string) *GRPCTransport {
	return &GRPCTransport{conn: conn, token: token}
}

// Close closes the connection if DialGRPC opened it.
func (t *GRPCTransport) Close() error {
	if !t.owned {
		return nil
	}
	return t.conn.Close()
}

// Open starts a session stream. The worker sends its response headers when it
// admits the stream, and a client must not send anything before seeing them,
// so Open waits for them.
func (t *GRPCTransport) Open(ctx context.Context) (Stream, error) {
	if len(t.meta) > 0 {
		ctx = metadata.AppendToOutgoingContext(ctx, t.meta...)
	}
	if t.token != "" {
		ctx = metadata.AppendToOutgoingContext(ctx, "authorization", "Bearer "+t.token)
	}
	stream, err := montyv1.NewMontySessionClient(t.conn).Session(ctx)
	if err != nil {
		return nil, fmt.Errorf("remote: open session: %w", err)
	}
	if _, err := stream.Header(); err != nil {
		return nil, fmt.Errorf("remote: session was not admitted: %w", err)
	}
	return stream, nil
}
