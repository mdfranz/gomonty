package remote

import (
	"fmt"
	"time"

	monty "github.com/mdfranz/gomonty"
	montyv1 "github.com/mdfranz/gomonty/remote/internal/montyv1"
)

// localAnswer says whether the backend answers an OS call itself.
type localAnswer int

const (
	// askHost passes the call to the host's OS handler.
	askHost localAnswer = iota
	// answerNotHandled tells the worker no handler accepted the call. It raises
	// the call's own default error in the script (a PermissionError naming the
	// path for filesystem calls, a RuntimeError for the rest). It is the answer
	// for calls gomonty has no handler vocabulary for.
	answerNotHandled
	// answerAfterSleep waits, then answers with None. The worker expects the
	// host to wait for a blocking sleep without involving its OS handler.
	answerAfterSleep
)

// osPlan is an OS call from the worker, in the shape gomonty's host layer
// expects: the same name, arguments and keyword arguments the native library
// produces, so that vfs.Handler and any other OSHandler work unchanged.
type osPlan struct {
	name   string
	args   []monty.Value
	kwargs monty.Dict

	local localAnswer
	// sleep is how long answerAfterSleep waits.
	sleep time.Duration
}

func pathValue(p string) monty.Value { return monty.PathValue(monty.Path(p)) }

func stringList(items []string) monty.Value {
	values := make([]monty.Value, len(items))
	for i, s := range items {
		values[i] = monty.String(s)
	}
	return monty.List(values...)
}

func kw(name string, value monty.Value) monty.Pair {
	return monty.Pair{Key: monty.String(name), Value: value}
}

// planOSCall projects a typed OsCall onto gomonty's name-and-arguments form.
// values is the call's decoded arena, for the arms that carry value indexes.
//
// Not supported, so answered with "not handled": appending, open() and
// exclusive creation (they need file handles, tracked in #58), date, time and
// random calls (the worker answers those itself under the default policy), and
// asyncio sleeps (they need futures resolved on a timer).
func planOSCall(call *montyv1.OsCall, values []monty.Value) (osPlan, error) {
	onePath := func(name, p string) (osPlan, error) {
		return osPlan{name: name, args: []monty.Value{pathValue(p)}}, nil
	}
	switch c := call.GetCall().(type) {
	case *montyv1.OsCall_Exists:
		return onePath("Path.exists", c.Exists)
	case *montyv1.OsCall_IsFile:
		return onePath("Path.is_file", c.IsFile)
	case *montyv1.OsCall_IsDir:
		return onePath("Path.is_dir", c.IsDir)
	case *montyv1.OsCall_IsSymlink:
		return onePath("Path.is_symlink", c.IsSymlink)
	case *montyv1.OsCall_ReadText:
		return onePath("Path.read_text", c.ReadText)
	case *montyv1.OsCall_ReadBytes:
		return onePath("Path.read_bytes", c.ReadBytes)
	case *montyv1.OsCall_Stat:
		return onePath("Path.stat", c.Stat)
	case *montyv1.OsCall_Iterdir:
		return onePath("Path.iterdir", c.Iterdir)
	case *montyv1.OsCall_Resolve:
		return onePath("Path.resolve", c.Resolve)
	case *montyv1.OsCall_Absolute:
		return onePath("Path.absolute", c.Absolute)
	case *montyv1.OsCall_Unlink:
		return onePath("Path.unlink", c.Unlink)
	case *montyv1.OsCall_Rmdir:
		return onePath("Path.rmdir", c.Rmdir)

	case *montyv1.OsCall_WriteText:
		return osPlan{name: "Path.write_text", args: []monty.Value{
			pathValue(c.WriteText.GetPath()), monty.String(c.WriteText.GetData()),
		}}, nil
	case *montyv1.OsCall_WriteBytes:
		return osPlan{name: "Path.write_bytes", args: []monty.Value{
			pathValue(c.WriteBytes.GetPath()), monty.Bytes(append([]byte(nil), c.WriteBytes.GetData()...)),
		}}, nil
	case *montyv1.OsCall_Mkdir_:
		return osPlan{
			name: "Path.mkdir",
			args: []monty.Value{pathValue(c.Mkdir.GetPath())},
			kwargs: monty.Dict{
				kw("parents", monty.Bool(c.Mkdir.GetParents())),
				kw("exist_ok", monty.Bool(c.Mkdir.GetExistOk())),
			},
		}, nil
	case *montyv1.OsCall_Rename_:
		return osPlan{name: "Path.rename", args: []monty.Value{
			pathValue(c.Rename.GetSrc()), pathValue(c.Rename.GetDst()),
		}}, nil
	case *montyv1.OsCall_Scan_:
		scan := c.Scan
		kwargs := monty.Dict{kw("follow_symlinks", monty.Bool(scan.GetFollowSymlinks()))}
		if scan.MaxDepth != nil {
			kwargs = append(kwargs, kw("max_depth", monty.Int(int64(scan.GetMaxDepth()))))
		}
		if len(scan.GetPattern()) > 0 {
			kwargs = append(kwargs, kw("pattern", stringList(scan.GetPattern())))
		}
		if scan.CaseSensitive != nil {
			kwargs = append(kwargs, kw("case_sensitive", monty.Bool(scan.GetCaseSensitive())))
		}
		kwargs = append(kwargs, kw("recurse_symlinks", monty.Bool(scan.GetRecurseSymlinks())))
		return osPlan{name: "Path.scan", args: []monty.Value{pathValue(scan.GetPath())}, kwargs: kwargs}, nil

	case *montyv1.OsCall_Getenv_:
		def, err := pick(values, c.Getenv.GetDefault())
		if err != nil {
			return osPlan{}, fmt.Errorf("os.getenv default: %w", err)
		}
		return osPlan{name: "os.getenv", args: []monty.Value{monty.String(c.Getenv.GetKey()), def}}, nil
	case *montyv1.OsCall_GetEnviron:
		return osPlan{name: "os.environ"}, nil

	case *montyv1.OsCall_Import_:
		return osPlan{name: "import", args: []monty.Value{
			monty.String(c.Import.GetModule()), stringList(c.Import.GetSearchPath()),
		}}, nil
	case *montyv1.OsCall_ImportStubs_:
		return osPlan{name: "import_stubs", args: []monty.Value{stringList(c.ImportStubs.GetModules())}}, nil
	case *montyv1.OsCall_GetTypeHints_:
		switch target := c.GetTypeHints.GetTarget().(type) {
		case *montyv1.OsCall_GetTypeHints_Function:
			return osPlan{name: "typing.get_type_hints", args: []monty.Value{monty.String("function"), monty.String(target.Function)}}, nil
		case *montyv1.OsCall_GetTypeHints_ClassId:
			return osPlan{name: "typing.get_type_hints", args: []monty.Value{monty.String("class"), monty.String(uuidString(target.ClassId.GetData()))}}, nil
		}
		return osPlan{local: answerNotHandled}, nil

	case *montyv1.OsCall_Sleep_:
		return osPlan{local: answerAfterSleep, sleep: seconds(c.Sleep.GetSeconds())}, nil
	case *montyv1.OsCall_SystemSleep:
		return osPlan{local: answerAfterSleep, sleep: seconds(c.SystemSleep.GetSeconds())}, nil

	default:
		return osPlan{local: answerNotHandled}, nil
	}
}

// seconds converts a sleep length, treating negative and NaN lengths as zero.
func seconds(s float64) time.Duration {
	if !(s > 0) {
		return 0
	}
	const max = float64(1<<62) / float64(time.Second)
	if s > max {
		s = max
	}
	return time.Duration(s * float64(time.Second))
}

// uuidString formats 16 bytes the way Python prints a UUID.
func uuidString(b []byte) string {
	if len(b) != 16 {
		return fmt.Sprintf("%x", b)
	}
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}
