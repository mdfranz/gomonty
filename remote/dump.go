package remote

import (
	"bytes"
	"errors"

	monty "github.com/mdfranz/gomonty"
)

// A remote dump is the worker's opaque snapshot behind a short envelope, so a
// dump written by the native library, or by some other backend, is refused here
// by name instead of being sent to a worker as garbage.
const (
	dumpMagic   = "GMONTYR\x00"
	dumpVersion = byte(1)
)

func sealDump(state []byte) []byte {
	out := make([]byte, 0, len(dumpMagic)+1+len(state))
	out = append(out, dumpMagic...)
	out = append(out, dumpVersion)
	return append(out, state...)
}

func openDump(data []byte) ([]byte, error) {
	if !bytes.HasPrefix(data, []byte(dumpMagic)) {
		return nil, errors.Join(monty.ErrIncompatibleDump, errors.New("remote: not a dump taken from a remote worker"))
	}
	if len(data) < len(dumpMagic)+1 || data[len(dumpMagic)] != dumpVersion {
		return nil, errors.Join(monty.ErrIncompatibleDump, errors.New("remote: unsupported remote dump version"))
	}
	return data[len(dumpMagic)+1:], nil
}
