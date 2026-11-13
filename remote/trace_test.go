package remote_test

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	monty "github.com/mdfranz/gomonty"
	"github.com/mdfranz/gomonty/remote"
	"github.com/mdfranz/gomonty/remote/internal/fakeworker"
	montyv1 "github.com/mdfranz/gomonty/remote/internal/montyv1"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
)

type recordedTrace struct {
	kind        string
	traceParent *string
}

func recordTraceParent(dst *[]recordedTrace, mu *sync.Mutex, kind string, req *montyv1.ClientRequest) {
	mu.Lock()
	defer mu.Unlock()
	var tp *string
	if req.TraceParent != nil {
		v := *req.TraceParent
		tp = &v
	}
	*dst = append(*dst, recordedTrace{kind: kind, traceParent: tp})
}

func TestTraceParentOmittedWithoutSpan(t *testing.T) {
	var mu sync.Mutex
	var got []recordedTrace

	conn := fakeworker.Start(t, func(s *fakeworker.Stream) {
		for {
			req := s.Recv()
			if req == nil {
				return
			}
			switch req.Kind.(type) {
			case *montyv1.ClientRequest_Configure:
				recordTraceParent(&got, &mu, "configure", req)
				s.Send(fakeworker.Ok())
			case *montyv1.ClientRequest_Feed:
				recordTraceParent(&got, &mu, "feed", req)
				s.Send(fakeworker.Complete(0, fakeworker.Int(1)))
			case *montyv1.ClientRequest_Dump:
				recordTraceParent(&got, &mu, "dump", req)
				s.Send(fakeworker.Dumped([]byte("snap")))
			case *montyv1.ClientRequest_Reset_:
				recordTraceParent(&got, &mu, "reset", req)
				s.Send(fakeworker.Ok())
			}
		}
	})

	repl, err := monty.NewRepl(monty.ReplOptions{
		ScriptName: "trace_test.py",
		Backend:    remote.NewBackend(remote.NewGRPCTransport(conn, token)),
	})
	if err != nil {
		t.Fatalf("NewRepl: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	val, err := repl.FeedRun(ctx, "1", monty.FeedOptions{})
	wantInt(t, val, err, 1)

	if _, err := repl.Dump(); err != nil {
		t.Fatalf("Dump: %v", err)
	}
	if err := repl.Reset(ctx); err != nil {
		t.Fatalf("Reset: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	// NewRepl configures once, then FeedRun, Dump, Reset (+ reconfigure).
	if len(got) != 5 {
		t.Fatalf("recorded %d requests, want 5: %+v", len(got), got)
	}
	for _, r := range got {
		if r.traceParent != nil {
			t.Errorf("%s trace_parent = %q, want nil without span in context", r.kind, *r.traceParent)
		}
	}
}

func TestTraceParentPropagatedFromSpanContext(t *testing.T) {
	exporter := tracetest.NewInMemoryExporter()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter))
	defer func() { _ = tp.Shutdown(context.Background()) }()
	tracer := tp.Tracer("remote-test")

	var mu sync.Mutex
	var got []recordedTrace

	conn := fakeworker.Start(t, func(s *fakeworker.Stream) {
		for {
			req := s.Recv()
			if req == nil {
				return
			}
			switch req.Kind.(type) {
			case *montyv1.ClientRequest_Configure:
				recordTraceParent(&got, &mu, "configure", req)
				s.Send(fakeworker.Ok())
			case *montyv1.ClientRequest_Feed:
				recordTraceParent(&got, &mu, "feed", req)
				s.Send(fakeworker.CallsHost("ping"))
			case *montyv1.ClientRequest_ResumeCall:
				recordTraceParent(&got, &mu, "resume_call", req)
				s.Send(fakeworker.Complete(0, fakeworker.Int(42)))
			case *montyv1.ClientRequest_Reset_:
				recordTraceParent(&got, &mu, "reset", req)
				s.Send(fakeworker.Ok())
			}
		}
	})

	repl, err := monty.NewRepl(monty.ReplOptions{
		ScriptName: "trace_test.py",
		Backend:    remote.NewBackend(remote.NewGRPCTransport(conn, token)),
	})
	if err != nil {
		t.Fatalf("NewRepl: %v", err)
	}

	parentCtx, parentSpan := tracer.Start(context.Background(), "caller")
	sc := parentSpan.SpanContext()
	wantTP := fmt.Sprintf("00-%s-%s-%s", sc.TraceID(), sc.SpanID(), sc.TraceFlags())

	var hostChildTraceID trace.TraceID
	var hostChildParentID trace.SpanID
	val, err := repl.FeedRun(parentCtx, "ping()", monty.FeedOptions{
		Functions: map[string]monty.ExternalFunction{
			"ping": func(callCtx context.Context, _ monty.Call) (monty.Result, error) {
				_, child := tracer.Start(callCtx, "host.ping")
				hostChildTraceID = child.SpanContext().TraceID()
				hostChildParentID = trace.SpanContextFromContext(callCtx).SpanID()
				child.End()
				return monty.Return(monty.None()), nil
			},
		},
	})
	wantInt(t, val, err, 42)

	if err := repl.Reset(parentCtx); err != nil {
		t.Fatalf("Reset: %v", err)
	}
	parentSpan.End()

	mu.Lock()
	defer mu.Unlock()
	// NewRepl configures once, then FeedRun, Dump, Reset (+ reconfigure).
	if len(got) != 5 {
		t.Fatalf("recorded %d requests, want 5: %+v", len(got), got)
	}
	if got[0].kind != "configure" || got[0].traceParent != nil {
		t.Errorf("configure request = %+v, want nil trace_parent (NewRepl takes no ctx)", got[0])
	}
	for _, r := range got[1:] {
		if r.traceParent == nil {
			t.Fatalf("%s trace_parent is nil, want %q", r.kind, wantTP)
		}
		if *r.traceParent != wantTP {
			t.Errorf("%s trace_parent = %q, want %q", r.kind, *r.traceParent, wantTP)
		}
		parts := strings.Split(*r.traceParent, "-")
		if len(parts) != 4 || parts[0] != "00" || len(parts[1]) != 32 || len(parts[2]) != 16 || len(parts[3]) != 2 {
			t.Errorf("%s trace_parent = %q, invalid W3C format", r.kind, *r.traceParent)
		}
	}

	if hostChildTraceID != sc.TraceID() {
		t.Errorf("host child TraceID = %s, want %s", hostChildTraceID, sc.TraceID())
	}
	if hostChildParentID != sc.SpanID() {
		t.Errorf("host callback parent SpanID = %s, want %s", hostChildParentID, sc.SpanID())
	}

	ended := exporter.GetSpans()
	if len(ended) != 2 {
		t.Fatalf("recorded %d spans, want 2 (caller + host.ping)", len(ended))
	}
	if ended[0].Name != "host.ping" || ended[0].Parent.SpanID() != sc.SpanID() {
		t.Errorf("host span = %+v, want host.ping under parent %s", ended[0].Name, sc.SpanID())
	}
}
