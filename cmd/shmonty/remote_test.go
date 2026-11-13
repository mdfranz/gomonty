package main

import (
	"reflect"
	"testing"
)

func TestParseRemoteFlag(t *testing.T) {
	cases := []struct {
		in       []string
		endpoint string
		rest     []string
		wantErr  bool
	}{
		{nil, "", nil, false},
		{[]string{"a.py"}, "", []string{"a.py"}, false},
		{[]string{"--remote", "h:443"}, "h:443", nil, false},
		{[]string{"--remote=h:443"}, "h:443", nil, false},
		{[]string{"a.py", "--remote", "h:443"}, "h:443", []string{"a.py"}, false},
		{[]string{"--remote"}, "", nil, true},
	}
	for _, c := range cases {
		endpoint, rest, err := parseRemoteFlag(c.in)
		if (err != nil) != c.wantErr || endpoint != c.endpoint || !reflect.DeepEqual(rest, c.rest) {
			t.Errorf("parseRemoteFlag(%q) = %q, %q, %v", c.in, endpoint, rest, err)
		}
	}
}

func TestUseRemoteRefusesTokenWithoutTLS(t *testing.T) {
	t.Setenv("MONTY_REMOTE_INSECURE", "1")
	t.Setenv("MONTY_REMOTE_TOKEN", "secret")
	if err := useRemote("localhost:1"); err == nil {
		t.Fatal("useRemote sent a token without TLS")
	}
}

func TestWorkerAttrsSayWhereItRuns(t *testing.T) {
	old := remoteEndpoint
	t.Cleanup(func() { remoteEndpoint = old })

	remoteEndpoint = ""
	if got := workerAttrs(); !reflect.DeepEqual(got, []any{"worker", "local"}) {
		t.Errorf("local attrs = %v", got)
	}
	remoteEndpoint = "h:443"
	if got := workerAttrs(); !reflect.DeepEqual(got, []any{"worker", "remote", "worker_endpoint", "h:443"}) {
		t.Errorf("remote attrs = %v", got)
	}
}
