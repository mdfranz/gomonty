package monty

import (
	"bytes"
	"encoding/hex"
	"errors"
	"fmt"
)

// montyRevision is the upstream Monty commit this build is pinned to. It must
// match the `rev` of the monty dependencies in Cargo.toml (checked by
// TestDumpHeaderRevisionMatchesCargoPin). Dumps are only portable between
// builds with the same revision, because Monty's serialized layout changes
// without notice.
const montyRevision = "e8db9aef41aa73f4f709c15a4bc8a2d198be5ead"

const (
	dumpMagic         = "GMONTY\x00"
	dumpHeaderVersion = byte(1)
	dumpRevisionLen   = 20 // montyRevision as raw bytes
	dumpHeaderLen     = len(dumpMagic) + 1 + dumpRevisionLen
)

// ErrIncompatibleDump reports that a dump was not written by this gomonty
// release: it has no gomonty header, an unsupported header version, or was
// produced against a different pinned Monty revision.
var ErrIncompatibleDump = errors.New("dump was written by an incompatible gomonty release; re-create it from source")

var dumpHeader = buildDumpHeader()

func buildDumpHeader() []byte {
	rev, err := hex.DecodeString(montyRevision)
	if err != nil || len(rev) != dumpRevisionLen {
		panic("gomonty: montyRevision must be a 40-character hex commit hash")
	}
	h := make([]byte, 0, dumpHeaderLen)
	h = append(h, dumpMagic...)
	h = append(h, dumpHeaderVersion)
	h = append(h, rev...)
	return h
}

// addDumpHeader prefixes a raw native dump with the gomonty header.
func addDumpHeader(raw []byte) []byte {
	out := make([]byte, 0, len(dumpHeader)+len(raw))
	out = append(out, dumpHeader...)
	return append(out, raw...)
}

// stripDumpHeader validates the gomonty header and returns the raw native dump.
func stripDumpHeader(data []byte) ([]byte, error) {
	if !bytes.HasPrefix(data, []byte(dumpMagic)) {
		return nil, fmt.Errorf("%w: missing gomonty header", ErrIncompatibleDump)
	}
	if len(data) < dumpHeaderLen {
		return nil, fmt.Errorf("%w: truncated header", ErrIncompatibleDump)
	}
	if v := data[len(dumpMagic)]; v != dumpHeaderVersion {
		return nil, fmt.Errorf("%w: header version %d, want %d", ErrIncompatibleDump, v, dumpHeaderVersion)
	}
	if !bytes.Equal(data[len(dumpMagic)+1:dumpHeaderLen], dumpHeader[len(dumpMagic)+1:]) {
		return nil, fmt.Errorf("%w: written against a different Monty revision", ErrIncompatibleDump)
	}
	return data[dumpHeaderLen:], nil
}
