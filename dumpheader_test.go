package monty

import (
	"errors"
	"os"
	"regexp"
	"testing"
)

func TestDumpHeaderRevisionMatchesCargoPin(t *testing.T) {
	cargo, err := os.ReadFile("Cargo.toml")
	if err != nil {
		t.Fatal(err)
	}
	matches := regexp.MustCompile(`rev = "([0-9a-f]{40})"`).FindAllSubmatch(cargo, -1)
	if len(matches) == 0 {
		t.Fatal("no monty rev found in Cargo.toml")
	}
	for _, m := range matches {
		if string(m[1]) != montyRevision {
			t.Fatalf("Cargo.toml rev %s != montyRevision %s; update dumpheader.go", m[1], montyRevision)
		}
	}
}

func TestDumpHeaderPresent(t *testing.T) {
	runner, err := New("1 + 1", CompileOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer closeTestRunner(runner)
	data, err := runner.Dump()
	if err != nil {
		t.Fatal(err)
	}
	if string(data[:len(dumpMagic)]) != dumpMagic {
		t.Fatalf("dump does not start with magic: %q", data[:len(dumpMagic)])
	}
}

func TestLoadRejectsIncompatibleDumps(t *testing.T) {
	runner, err := New("1 + 1", CompileOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer closeTestRunner(runner)
	good, err := runner.Dump()
	if err != nil {
		t.Fatal(err)
	}
	raw := good[dumpHeaderLen:]

	badVersion := append([]byte(nil), good...)
	badVersion[len(dumpMagic)]++
	badRev := append([]byte(nil), good...)
	badRev[dumpHeaderLen-1] ^= 0xff

	cases := map[string][]byte{
		"empty":            nil,
		"legacy unversion": raw,
		"truncated header": good[:len(dumpMagic)+2],
		"bad version":      badVersion,
		"bad revision":     badRev,
	}
	for name, data := range cases {
		t.Run(name, func(t *testing.T) {
			if r, err := LoadRunner(data); !errors.Is(err, ErrIncompatibleDump) {
				if r != nil {
					closeTestRunner(r)
				}
				t.Fatalf("LoadRunner err = %v, want ErrIncompatibleDump", err)
			}
			if _, err := LoadSnapshot(data); !errors.Is(err, ErrIncompatibleDump) {
				t.Fatalf("LoadSnapshot err = %v, want ErrIncompatibleDump", err)
			}
			if _, _, err := LoadReplSnapshot(data); !errors.Is(err, ErrIncompatibleDump) {
				t.Fatalf("LoadReplSnapshot err = %v, want ErrIncompatibleDump", err)
			}
		})
	}

	t.Run("corrupted payload keeps a classified native error", func(t *testing.T) {
		bad := append(append([]byte(nil), good[:dumpHeaderLen]...), 0xff, 0xff, 0xff)
		r, err := LoadRunner(bad)
		if r != nil {
			closeTestRunner(r)
		}
		if err == nil || errors.Is(err, ErrIncompatibleDump) {
			t.Fatalf("err = %v, want a non-header load error", err)
		}
	})
}
