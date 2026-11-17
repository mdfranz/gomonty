//! C ABI for Go bindings to Monty.
//!
//! The ABI deliberately keeps compiled runners, REPL sessions, and in-flight
//! snapshots opaque. Structured values cross the boundary as versioned binary
//! payloads defined in [`wire`], while suspend/resume state stays in Rust-owned
//! handles that the Go wrapper drives explicitly.

// Every exported function takes raw pointers because that is the C ABI. The only
// caller is the Go wrapper (through purego), which neither reads Rust's `unsafe`
// markers nor could act on them, so marking the ~60 exports `unsafe extern "C"`
// would only force `unsafe` blocks onto this crate's own tests. The pointer
// contract is stated per function instead, in the `// SAFETY:` comments at each
// dereference. Every dereference is null-checked first.
#![expect(
    clippy::not_unsafe_ptr_arg_deref,
    reason = "C ABI exports are called only from Go via purego; contracts are in SAFETY comments"
)]

mod wire;

use std::{
    any::Any,
    borrow::Cow,
    collections::BTreeMap,
    ffi::{CStr, c_char},
    mem,
    panic::{AssertUnwindSafe, catch_unwind},
    ptr,
};

use monty::{
    Dump, MontyRepl, MontyRun, ReplProgress, ReplStartError, RunProgress, Session, SessionRef, dump,
};
use monty_type_checking::{SourceFile, TypeChecker};
use monty_types::{
    CompileOptions, ExcType, ExtFunctionResult, ModuleFile, MontyException, MontyObject,
    NameLookupResult, PrintWriter, PrintWriterCallback, ResourceTracker, TypeCheckingConfig,
    TypeCheckingFormat,
};
use wire::{
    WIRE_CALL_RESULT_EXCEPTION, WIRE_CALL_RESULT_MODULE_FILE, WIRE_CALL_RESULT_NOT_FOUND,
    WIRE_CALL_RESULT_PENDING, WIRE_CALL_RESULT_RETURN, WIRE_LOOKUP_RESULT_UNDEFINED,
    WIRE_LOOKUP_RESULT_VALUE, WIRE_PROGRESS_COMPLETE, WIRE_PROGRESS_FUNCTION_CALL,
    WIRE_PROGRESS_FUTURE, WIRE_PROGRESS_NAME_LOOKUP, WireCallResult, WireCompileOptions,
    WireErrorSummary, WireFeedOptions, WireFutureResults, WireLookupResult, WireProgressPayload,
    WireReplOptions, WireStartOptions, WireValue,
};

/// Opaque runner handle for the Go bindings.
pub struct MontyGoRunner {
    runner: MontyRun,
    script_name: String,
    input_names: Vec<String>,
}

/// Opaque REPL handle for the Go bindings.
pub struct MontyGoRepl {
    script_name: String,
    inner: Option<StoredRepl>,
}

/// Opaque progress handle for the Go bindings.
pub struct MontyGoProgress {
    inner: Option<StoredProgress>,
}

/// Opaque error handle for the Go bindings.
pub struct MontyGoError {
    inner: FfiError,
}

/// Inputs to a type check, kept around so a typing failure can be re-rendered
/// on demand in whatever format/color the caller later asks for.
///
/// Upstream's `TypeCheckingDiagnostics` borrows the `TypeChecker` that
/// produced it and is created with a fixed render config, so it cannot be
/// stored in a long-lived opaque error handle and reformatted later the way
/// gomonty's `monty_go_error_display` API allows. Re-running the (cheap,
/// already-known-to-fail) type check on each display call preserves that API
/// instead of restricting it to the format chosen at check time.
#[derive(Debug, Clone)]
struct TypingFailure {
    source_code: String,
    script_name: String,
    stubs: Option<String>,
}

impl TypingFailure {
    fn render(&self, config: TypeCheckingConfig) -> Result<String, String> {
        let mut checker = TypeChecker::default();
        let source = SourceFile::new(&self.source_code, &self.script_name);
        let stubs = self
            .stubs
            .as_deref()
            .map(|stubs| SourceFile::new(stubs, "type_stubs.py"));
        match checker.run(&source, stubs.as_ref(), config) {
            Ok(Some(diagnostics)) => Ok(diagnostics.to_string()),
            Ok(None) => Err("type check no longer reports any errors".to_owned()),
            Err(error) => Err(error),
        }
    }
}

#[derive(Debug)]
enum FfiError {
    Exception(MontyException),
    Typing(TypingFailure),
    Api(String),
}

fn ffi_panic_error(payload: &(dyn Any + Send)) -> FfiError {
    let message = if let Some(message) = payload.downcast_ref::<&str>() {
        (*message).to_owned()
    } else if let Some(message) = payload.downcast_ref::<String>() {
        message.clone()
    } else {
        "panic payload was not a string".to_owned()
    };
    FfiError::Api(format!("monty-go ffi panicked: {message}"))
}

/// Runs `f`, returning `fallback()` if it panics.
///
/// For exports that write their result through an out pointer and cannot hand
/// back an error handle. A panic must never unwind out of an `extern "C"`
/// function: that aborts the whole host process.
fn catch_or<T>(fallback: impl FnOnce() -> T, f: impl FnOnce() -> T) -> T {
    catch_unwind(AssertUnwindSafe(f)).unwrap_or_else(|_| fallback())
}

/// Runs a destructor, swallowing a panic from a `Drop` impl.
///
/// The value being dropped may leak if its destructor panics partway through,
/// which is preferable to aborting the host process.
fn catch_drop(f: impl FnOnce()) {
    let _ = catch_unwind(AssertUnwindSafe(f));
}

const PANIC_RENDERING_ERROR: &str = "monty-go ffi panicked while rendering an error";

fn catch_runner_result<F>(f: F) -> MontyGoRunnerResult
where
    F: FnOnce() -> MontyGoRunnerResult,
{
    match catch_unwind(AssertUnwindSafe(f)) {
        Ok(result) => result,
        Err(payload) => MontyGoRunnerResult::err(ffi_panic_error(&*payload)),
    }
}

fn catch_repl_result<F>(f: F) -> MontyGoReplResult
where
    F: FnOnce() -> MontyGoReplResult,
{
    match catch_unwind(AssertUnwindSafe(f)) {
        Ok(result) => result,
        Err(payload) => MontyGoReplResult::err(ffi_panic_error(&*payload)),
    }
}

fn catch_op_result<F>(f: F) -> MontyGoOpResult
where
    F: FnOnce() -> MontyGoOpResult,
{
    match catch_unwind(AssertUnwindSafe(f)) {
        Ok(result) => result,
        Err(payload) => MontyGoOpResult::err(ffi_panic_error(&*payload), None, String::new()),
    }
}

fn catch_error_result<F>(f: F) -> *mut MontyGoError
where
    F: FnOnce() -> *mut MontyGoError,
{
    match catch_unwind(AssertUnwindSafe(f)) {
        Ok(result) => result,
        Err(payload) => Box::into_raw(Box::new(MontyGoError {
            inner: ffi_panic_error(&*payload),
        })),
    }
}

fn catch_bytes_result<F>(error_out: *mut *mut MontyGoError, f: F) -> MontyGoBytes
where
    F: FnOnce() -> MontyGoBytes,
{
    match catch_unwind(AssertUnwindSafe(f)) {
        Ok(result) => result,
        Err(payload) => {
            if !error_out.is_null() {
                // SAFETY: the caller owns `error_out`, and it was null-checked just above
                unsafe {
                    *error_out = Box::into_raw(Box::new(MontyGoError {
                        inner: ffi_panic_error(&*payload),
                    }));
                };
            }
            MontyGoBytes::empty()
        }
    }
}

#[derive(Debug)]
struct StoredRepl {
    repl: MontyRepl,
    suspensions_seen: usize,
}

const SUSPENSION_HEADER_MAGIC: &[u8; 8] = b"GMSUSP\x00\x01";
const SUSPENSION_HEADER_LEN: usize = SUSPENSION_HEADER_MAGIC.len() + std::mem::size_of::<u64>();

fn encode_dump_with_suspensions(raw: &[u8], suspensions_seen: usize) -> Vec<u8> {
    let mut out = Vec::with_capacity(SUSPENSION_HEADER_LEN + raw.len());
    out.extend_from_slice(SUSPENSION_HEADER_MAGIC);
    out.extend_from_slice(&(suspensions_seen as u64).to_le_bytes());
    out.extend_from_slice(raw);
    out
}

fn decode_dump_with_suspensions(
    data: &[u8],
    default_suspensions: usize,
) -> Result<(usize, &[u8]), FfiError> {
    if let Some(rest) = data.strip_prefix(SUSPENSION_HEADER_MAGIC) {
        if rest.len() < std::mem::size_of::<u64>() {
            return Err(FfiError::Api(
                "truncated suspension header in dump".to_owned(),
            ));
        }
        let (count_bytes, payload) = rest.split_at(std::mem::size_of::<u64>());
        let mut buf = [0_u8; 8];
        buf.copy_from_slice(count_bytes);
        let count = usize::try_from(u64::from_le_bytes(buf)).unwrap_or(usize::MAX);
        return Ok((count, payload));
    }
    Ok((default_suspensions, data))
}

// `MontyRun` is compiled code, not a session, so upstream has no `dump` framing
// for it: it is encoded directly as self-describing CBOR, in these two shapes.
#[derive(serde::Deserialize)]
struct StoredRunner {
    runner: MontyRun,
    script_name: String,
    input_names: Vec<String>,
}

#[derive(serde::Serialize)]
struct StoredRunnerRef<'a> {
    runner: &'a MontyRun,
    script_name: &'a str,
    input_names: &'a [String],
}

// Each handle is a single heap allocation (`Box::into_raw` of `MontyGoProgress`),
// so the size gap between the variants costs nothing worth the churn of boxing
// the payload at every construction and match site.
#[expect(
    clippy::large_enum_variant,
    reason = "always held behind one Box per handle"
)]
#[derive(Debug)]
enum StoredProgress {
    Run {
        progress: RunProgress,
        script_name: String,
        suspensions_seen: usize,
    },
    Repl {
        progress: ReplProgress,
        script_name: String,
        suspensions_seen: usize,
    },
}

/// Heap-allocated bytes returned across the C ABI.
#[repr(C)]
pub struct MontyGoBytes {
    /// Pointer to the byte buffer.
    pub ptr: *mut u8,
    /// Buffer length in bytes.
    pub len: usize,
}

impl MontyGoBytes {
    fn empty() -> Self {
        Self {
            ptr: ptr::null_mut(),
            len: 0,
        }
    }

    fn from_vec(bytes: Vec<u8>) -> Self {
        if bytes.is_empty() {
            return Self::empty();
        }
        let mut bytes = bytes.into_boxed_slice();
        let result = Self {
            ptr: bytes.as_mut_ptr(),
            len: bytes.len(),
        };
        mem::forget(bytes);
        result
    }
}

/// Result of runner construction or loading.
#[repr(C)]
pub struct MontyGoRunnerResult {
    /// Created runner handle on success.
    pub runner: *mut MontyGoRunner,
    /// Error handle on failure.
    pub error: *mut MontyGoError,
}

/// Result of REPL construction or loading.
#[repr(C)]
pub struct MontyGoReplResult {
    /// Created REPL handle on success.
    pub repl: *mut MontyGoRepl,
    /// Error handle on failure.
    pub error: *mut MontyGoError,
}

/// Result of start/resume/feed operations.
#[repr(C)]
pub struct MontyGoOpResult {
    /// Progress handle on success.
    pub progress: *mut MontyGoProgress,
    /// Decoded payload for the current progress state.
    pub progress_payload: MontyGoBytes,
    /// Error handle on failure.
    pub error: *mut MontyGoError,
    /// Recovered REPL handle for REPL runtime errors.
    pub repl: *mut MontyGoRepl,
    /// Captured `print()` output from this step.
    pub prints: MontyGoBytes,
}

impl MontyGoRunnerResult {
    fn ok(runner: MontyGoRunner) -> Self {
        Self {
            runner: Box::into_raw(Box::new(runner)),
            error: ptr::null_mut(),
        }
    }

    fn err(error: FfiError) -> Self {
        Self {
            runner: ptr::null_mut(),
            error: Box::into_raw(Box::new(MontyGoError { inner: error })),
        }
    }
}

impl MontyGoReplResult {
    fn ok(repl: MontyGoRepl) -> Self {
        Self {
            repl: Box::into_raw(Box::new(repl)),
            error: ptr::null_mut(),
        }
    }

    fn err(error: FfiError) -> Self {
        Self {
            repl: ptr::null_mut(),
            error: Box::into_raw(Box::new(MontyGoError { inner: error })),
        }
    }
}

impl MontyGoOpResult {
    fn ok(progress: StoredProgress, prints: String) -> Self {
        match encode_wire(&progress.describe()) {
            Ok(progress_payload) => Self {
                progress: Box::into_raw(Box::new(MontyGoProgress {
                    inner: Some(progress),
                })),
                progress_payload,
                error: ptr::null_mut(),
                repl: ptr::null_mut(),
                prints: MontyGoBytes::from_vec(prints.into_bytes()),
            },
            Err(error) => Self::err(error, None, prints),
        }
    }

    fn err(error: FfiError, repl: Option<MontyGoRepl>, prints: String) -> Self {
        Self {
            progress: ptr::null_mut(),
            progress_payload: MontyGoBytes::empty(),
            error: Box::into_raw(Box::new(MontyGoError { inner: error })),
            repl: repl.map_or(ptr::null_mut(), |repl| Box::into_raw(Box::new(repl))),
            prints: MontyGoBytes::from_vec(prints.into_bytes()),
        }
    }
}

struct PrintCollector {
    buffer: String,
}

impl PrintCollector {
    fn new() -> Self {
        Self {
            buffer: String::new(),
        }
    }

    fn into_string(self) -> String {
        self.buffer
    }
}

impl PrintWriterCallback for PrintCollector {
    fn stdout_write(&mut self, output: Cow<'_, str>) -> Result<(), MontyException> {
        self.buffer.push_str(&output);
        Ok(())
    }

    fn stdout_push(&mut self, end: char) -> Result<(), MontyException> {
        self.buffer.push(end);
        Ok(())
    }
}

/// Summary returned by `monty_go_error_json` when building the real one panics.
fn panic_summary() -> WireErrorSummary {
    WireErrorSummary {
        version: wire::WIRE_VERSION,
        kind: "api".to_owned(),
        type_name: "RuntimeError".to_owned(),
        message: PANIC_RENDERING_ERROR.to_owned(),
        traceback: Vec::new(),
    }
}

impl FfiError {
    fn summary(&self) -> WireErrorSummary {
        match self {
            Self::Exception(error) => WireErrorSummary::from_exception(error),
            Self::Typing(failure) => WireErrorSummary {
                version: wire::WIRE_VERSION,
                kind: "typing".to_owned(),
                type_name: "TypeError".to_owned(),
                message: failure
                    .render(TypeCheckingConfig::default())
                    .unwrap_or_else(|error| error),
                traceback: Vec::new(),
            },
            Self::Api(message) => WireErrorSummary {
                version: wire::WIRE_VERSION,
                kind: "api".to_owned(),
                type_name: "RuntimeError".to_owned(),
                message: message.clone(),
                traceback: Vec::new(),
            },
        }
    }

    fn display(&self, format: &str, color: bool) -> Result<String, String> {
        match self {
            Self::Exception(error) => match format {
                "traceback" => Ok(error.to_string()),
                "type-msg" => Ok(error.summary()),
                "msg" => Ok(error.message().unwrap_or_default().to_owned()),
                _ => Err(format!(
                    "invalid display format '{format}', expected 'traceback', 'type-msg', or 'msg'"
                )),
            },
            Self::Typing(failure) => {
                let format = TypeCheckingFormat::from_name(format)?;
                failure.render(TypeCheckingConfig { format, color })
            }
            Self::Api(message) => match format {
                "msg" | "type-msg" | "traceback" => Ok(message.clone()),
                _ => Err(format!(
                    "invalid display format '{format}', expected 'traceback', 'type-msg', or 'msg'"
                )),
            },
        }
    }
}

impl StoredProgress {
    fn describe(&self) -> WireProgressPayload {
        match self {
            Self::Run {
                progress,
                script_name,
                ..
            } => describe_run_progress(progress, script_name, false),
            Self::Repl {
                progress,
                script_name,
                ..
            } => describe_repl_progress(progress, script_name, true),
        }
    }

    fn into_repl(self) -> Result<MontyGoRepl, FfiError> {
        match self {
            Self::Repl {
                progress,
                script_name,
                suspensions_seen,
            } => Ok(MontyGoRepl {
                script_name,
                inner: Some(StoredRepl {
                    repl: progress.into_repl(),
                    suspensions_seen,
                }),
            }),
            Self::Run { .. } => Err(FfiError::Api(
                "progress handle does not own a REPL session".to_owned(),
            )),
        }
    }

    /// Serializes the suspended state with upstream's versioned dump framing,
    /// prefixed with gomonty's suspension counter.
    fn dump(&self) -> Result<Vec<u8>, FfiError> {
        let (script_name, session, suspensions_seen) = match self {
            Self::Run {
                progress,
                script_name,
                suspensions_seen,
            } => (
                script_name,
                SessionRef::Running(progress),
                *suspensions_seen,
            ),
            Self::Repl {
                progress,
                script_name,
                suspensions_seen,
            } => (
                script_name,
                SessionRef::Suspended(progress),
                *suspensions_seen,
            ),
        };
        let raw =
            dump(script_name, None, &[], session).map_err(|e| FfiError::Api(e.to_string()))?;
        Ok(encode_dump_with_suspensions(&raw, suspensions_seen))
    }
}

fn describe_run_progress(
    progress: &RunProgress,
    script_name: &str,
    is_repl: bool,
) -> WireProgressPayload {
    match progress {
        RunProgress::FunctionCall(call) => WireProgressPayload {
            variant: WIRE_PROGRESS_FUNCTION_CALL,
            version: wire::WIRE_VERSION,
            script_name: script_name.to_owned(),
            is_repl,
            is_os_function: false,
            is_method_call: call.object_id.is_some(),
            object_id: call.object_id.map(|id| id.to_string()),
            function_name: call.function_name.clone(),
            args: call.args.args().map(WireValue::from_object_ref).collect(),
            kwargs: call
                .args
                .kwargs()
                .map(|(key, value)| wire::WirePair {
                    key: WireValue::from_object_ref(key),
                    value: WireValue::from_object_ref(value),
                })
                .collect(),
            call_id: call.call_id,
            ..WireProgressPayload::default()
        },
        RunProgress::OsCall(call) => describe_os_call(
            call.function_call.clone(),
            call.call_id,
            script_name,
            is_repl,
        ),
        RunProgress::NameLookup(lookup) => WireProgressPayload {
            variant: WIRE_PROGRESS_NAME_LOOKUP,
            version: wire::WIRE_VERSION,
            script_name: script_name.to_owned(),
            is_repl,
            object_id: lookup.object_id().map(|id| id.to_string()),
            variable_name: lookup.name.clone(),
            ..WireProgressPayload::default()
        },
        RunProgress::ResolveFutures(state) => WireProgressPayload {
            variant: WIRE_PROGRESS_FUTURE,
            version: wire::WIRE_VERSION,
            script_name: script_name.to_owned(),
            is_repl,
            pending_call_ids: state.pending_call_ids().to_vec(),
            ..WireProgressPayload::default()
        },
        RunProgress::Complete(output) => WireProgressPayload {
            variant: WIRE_PROGRESS_COMPLETE,
            version: wire::WIRE_VERSION,
            script_name: script_name.to_owned(),
            is_repl,
            output: Some(WireValue::from_monty(output)),
            ..WireProgressPayload::default()
        },
    }
}

fn describe_repl_progress(
    progress: &ReplProgress,
    script_name: &str,
    is_repl: bool,
) -> WireProgressPayload {
    match progress {
        ReplProgress::FunctionCall(call) => WireProgressPayload {
            variant: WIRE_PROGRESS_FUNCTION_CALL,
            version: wire::WIRE_VERSION,
            script_name: script_name.to_owned(),
            is_repl,
            is_os_function: false,
            is_method_call: call.object_id.is_some(),
            object_id: call.object_id.map(|id| id.to_string()),
            function_name: call.function_name.clone(),
            args: call.args.args().map(WireValue::from_object_ref).collect(),
            kwargs: call
                .args
                .kwargs()
                .map(|(key, value)| wire::WirePair {
                    key: WireValue::from_object_ref(key),
                    value: WireValue::from_object_ref(value),
                })
                .collect(),
            call_id: call.call_id,
            ..WireProgressPayload::default()
        },
        ReplProgress::OsCall(call) => describe_os_call(
            call.function_call.clone(),
            call.call_id,
            script_name,
            is_repl,
        ),
        ReplProgress::NameLookup(lookup) => WireProgressPayload {
            variant: WIRE_PROGRESS_NAME_LOOKUP,
            version: wire::WIRE_VERSION,
            script_name: script_name.to_owned(),
            is_repl,
            object_id: lookup.object_id().map(|id| id.to_string()),
            variable_name: lookup.name.clone(),
            ..WireProgressPayload::default()
        },
        ReplProgress::ResolveFutures(state) => WireProgressPayload {
            variant: WIRE_PROGRESS_FUTURE,
            version: wire::WIRE_VERSION,
            script_name: script_name.to_owned(),
            is_repl,
            pending_call_ids: state.pending_call_ids().to_vec(),
            ..WireProgressPayload::default()
        },
        ReplProgress::Complete { value, .. } => WireProgressPayload {
            variant: WIRE_PROGRESS_COMPLETE,
            version: wire::WIRE_VERSION,
            script_name: script_name.to_owned(),
            is_repl,
            output: Some(WireValue::from_monty(value)),
            ..WireProgressPayload::default()
        },
    }
}

/// Shared by [`describe_run_progress`] and [`describe_repl_progress`]: an
/// `OsCall`'s typed [`OsFunctionCall`] dispatch value projects onto the same
/// generic `(name, args, kwargs)` shape the Go side already expects for
/// `FunctionCall`, via [`OsFunctionCall::name`]/[`OsFunctionCall::to_args`].
fn describe_os_call(
    function_call: monty_types::OsFunctionCall,
    call_id: u32,
    script_name: &str,
    is_repl: bool,
) -> WireProgressPayload {
    let function_name = function_call.name().to_owned();
    let call_args = function_call.to_args();
    WireProgressPayload {
        variant: WIRE_PROGRESS_FUNCTION_CALL,
        version: wire::WIRE_VERSION,
        script_name: script_name.to_owned(),
        is_repl,
        is_os_function: true,
        is_method_call: false,
        function_name,
        args: call_args.args().map(WireValue::from_object_ref).collect(),
        kwargs: call_args
            .kwargs()
            .map(|(key, value)| wire::WirePair {
                key: WireValue::from_object_ref(key),
                value: WireValue::from_object_ref(value),
            })
            .collect(),
        call_id,
        ..WireProgressPayload::default()
    }
}

fn extract_inputs(
    input_names: &[String],
    inputs: &BTreeMap<String, WireValue>,
) -> Result<Vec<MontyObject>, FfiError> {
    input_names
        .iter()
        .map(|name| {
            let value = inputs
                .get(name)
                .cloned()
                .ok_or_else(|| FfiError::Api(format!("missing required input '{name}'")))?;
            value.into_monty().map_err(FfiError::Api)
        })
        .collect()
}

fn extract_feed_inputs(
    inputs: BTreeMap<String, WireValue>,
) -> Result<Vec<(String, MontyObject)>, FfiError> {
    inputs
        .into_iter()
        .map(|(name, value)| {
            value
                .into_monty()
                .map(|value| (name, value))
                .map_err(FfiError::Api)
        })
        .collect()
}

fn decode_wire<T: serde::de::DeserializeOwned>(bytes: &[u8]) -> Result<T, FfiError> {
    rmp_serde::from_slice(bytes).map_err(|e| FfiError::Api(format!("invalid wire payload: {e}")))
}

fn encode_wire<T: serde::Serialize>(value: &T) -> Result<MontyGoBytes, FfiError> {
    rmp_serde::to_vec_named(value)
        .map(MontyGoBytes::from_vec)
        .map_err(|e| FfiError::Api(format!("failed to encode wire payload: {e}")))
}

unsafe fn slice_from_raw<'a>(ptr: *const u8, len: usize) -> &'a [u8] {
    if ptr.is_null() || len == 0 {
        &[]
    } else {
        // SAFETY: caller guarantees ptr/len point to a valid byte buffer for the duration
        unsafe { std::slice::from_raw_parts(ptr, len) }
    }
}

unsafe fn string_from_cstr<'a>(ptr: *const c_char) -> Result<&'a str, FfiError> {
    if ptr.is_null() {
        return Err(FfiError::Api("expected non-null C string".to_owned()));
    }
    // SAFETY: caller guarantees ptr is NUL-terminated
    unsafe { CStr::from_ptr(ptr) }
        .to_str()
        .map_err(|e| FfiError::Api(format!("invalid UTF-8 string: {e}")))
}

fn decode_utf8<'a>(bytes: &'a [u8], field_name: &str) -> Result<&'a str, FfiError> {
    std::str::from_utf8(bytes)
        .map_err(|error| FfiError::Api(format!("{field_name} is not valid UTF-8: {error}")))
}

fn decode_call_result(result: WireCallResult) -> Result<ExtFunctionResult, FfiError> {
    match result.kind {
        WIRE_CALL_RESULT_RETURN => result
            .value
            .ok_or_else(|| FfiError::Api("missing return value".to_owned()))?
            .into_monty()
            .map(ExtFunctionResult::Return)
            .map_err(FfiError::Api),
        WIRE_CALL_RESULT_EXCEPTION => Ok(ExtFunctionResult::Error(MontyException::new(
            parse_exc_type(&result.exc_type)?,
            result.arg,
        ))),
        WIRE_CALL_RESULT_PENDING => Ok(ExtFunctionResult::Future(0)),
        WIRE_CALL_RESULT_NOT_FOUND => {
            Ok(ExtFunctionResult::NotFound(result.arg.unwrap_or_default()))
        }
        WIRE_CALL_RESULT_MODULE_FILE => Ok(ExtFunctionResult::ModuleFile(ModuleFile {
            path: result.module_path,
            source: result.module_source,
        })),
        other => Err(FfiError::Api(format!("unknown call result kind: {other}"))),
    }
}

fn decode_lookup_result(result: WireLookupResult) -> Result<NameLookupResult, FfiError> {
    match result.kind {
        WIRE_LOOKUP_RESULT_VALUE => result
            .value
            .ok_or_else(|| FfiError::Api("missing lookup value".to_owned()))?
            .into_monty()
            .map(NameLookupResult::Value)
            .map_err(FfiError::Api),
        WIRE_LOOKUP_RESULT_UNDEFINED => Ok(NameLookupResult::Undefined),
        other => Err(FfiError::Api(format!(
            "unknown lookup result kind: {other}"
        ))),
    }
}

fn decode_future_results(
    results: WireFutureResults,
) -> Result<Vec<(u32, ExtFunctionResult)>, FfiError> {
    results
        .results
        .into_iter()
        .map(|(call_id, result)| {
            let result = match decode_call_result(result)? {
                ExtFunctionResult::Future(_) => ExtFunctionResult::Future(call_id),
                other => other,
            };
            Ok((call_id, result))
        })
        .collect()
}

fn parse_exc_type(exc_type: &str) -> Result<ExcType, FfiError> {
    exc_type
        .parse()
        .map_err(|_| FfiError::Api(format!("unknown exception type: {exc_type}")))
}

fn wrap_repl_handle(script_name: &str, inner: StoredRepl) -> MontyGoRepl {
    MontyGoRepl {
        script_name: script_name.to_owned(),
        inner: Some(inner),
    }
}

fn suspension_limit_exceeded(limit: usize) -> MontyException {
    MontyException::new(
        ExcType::RuntimeError,
        Some(format!("suspension limit {limit} exceeded")),
    )
}

fn run_progress_suspension_limit(progress: &RunProgress) -> Option<usize> {
    match progress {
        RunProgress::FunctionCall(call) => Some(call.tracker().max_suspensions()),
        RunProgress::OsCall(call) => Some(call.tracker().max_suspensions()),
        RunProgress::NameLookup(lookup) => Some(lookup.tracker().max_suspensions()),
        RunProgress::ResolveFutures(state) => Some(state.tracker().max_suspensions()),
        RunProgress::Complete(_) => None,
    }
}

const fn repl_progress_is_suspension(progress: &ReplProgress) -> bool {
    matches!(
        progress,
        ReplProgress::FunctionCall(_)
            | ReplProgress::OsCall(_)
            | ReplProgress::NameLookup(_)
            | ReplProgress::ResolveFutures(_)
    )
}

fn abort_run_progress(
    progress: RunProgress,
    exc: MontyException,
    prints: &mut PrintCollector,
) -> FfiError {
    let res = match progress {
        RunProgress::FunctionCall(call) => call.abort(exc, PrintWriter::Callback(prints)),
        RunProgress::OsCall(call) => call.abort(exc, PrintWriter::Callback(prints)),
        RunProgress::NameLookup(lookup) => lookup.abort(exc, PrintWriter::Callback(prints)),
        RunProgress::ResolveFutures(state) => state.abort(exc, PrintWriter::Callback(prints)),
        RunProgress::Complete(_) => {
            return FfiError::Api("completed execution cannot be aborted".to_owned());
        }
    };
    match res {
        Err(error) => FfiError::Exception(error),
        Ok(_) => FfiError::Api("abort unexpectedly returned a progress state".to_owned()),
    }
}

fn abort_repl_progress(
    progress: ReplProgress,
    exc: MontyException,
    prints: &mut PrintCollector,
) -> (FfiError, MontyRepl) {
    let res = match progress {
        ReplProgress::FunctionCall(call) => call.abort(exc, PrintWriter::Callback(prints)),
        ReplProgress::OsCall(call) => call.abort(exc, PrintWriter::Callback(prints)),
        ReplProgress::NameLookup(lookup) => lookup.abort(exc, PrintWriter::Callback(prints)),
        ReplProgress::ResolveFutures(state) => state.abort(exc, PrintWriter::Callback(prints)),
        ReplProgress::Complete { repl, .. } => {
            return (
                FfiError::Api("completed execution cannot be aborted".to_owned()),
                repl,
            );
        }
    };
    match res {
        Err(error) => {
            let ReplStartError { repl, error } = *error;
            (FfiError::Exception(error), repl)
        }
        Ok(other) => (
            FfiError::Api("abort unexpectedly returned a progress state".to_owned()),
            other.into_repl(),
        ),
    }
}

fn finish_run_step(
    progress: RunProgress,
    script_name: String,
    mut suspensions_seen: usize,
    mut prints: PrintCollector,
) -> Result<(StoredProgress, String), (FfiError, String)> {
    if let Some(limit) = run_progress_suspension_limit(&progress) {
        suspensions_seen = suspensions_seen.saturating_add(1);
        if suspensions_seen > limit {
            let err = abort_run_progress(progress, suspension_limit_exceeded(limit), &mut prints);
            return Err((err, prints.into_string()));
        }
    }
    Ok((
        StoredProgress::Run {
            progress,
            script_name,
            suspensions_seen,
        },
        prints.into_string(),
    ))
}

#[expect(
    clippy::result_large_err,
    reason = "the error carries the REPL session back to the caller so it is not lost"
)]
fn finish_repl_step(
    progress: ReplProgress,
    script_name: String,
    mut suspensions_seen: usize,
    mut prints: PrintCollector,
) -> Result<(StoredProgress, String), (FfiError, Option<MontyGoRepl>, String)> {
    if repl_progress_is_suspension(&progress) {
        suspensions_seen = suspensions_seen.saturating_add(1);
        let limit = progress.tracker().max_suspensions();
        if suspensions_seen > limit {
            let (err, repl) =
                abort_repl_progress(progress, suspension_limit_exceeded(limit), &mut prints);
            return Err((
                err,
                Some(wrap_repl_handle(
                    &script_name,
                    StoredRepl {
                        repl,
                        suspensions_seen,
                    },
                )),
                prints.into_string(),
            ));
        }
    }
    Ok((
        StoredProgress::Repl {
            progress,
            script_name,
            suspensions_seen,
        },
        prints.into_string(),
    ))
}

fn start_runner_internal(
    handle: &MontyGoRunner,
    options: WireStartOptions,
) -> Result<(StoredProgress, String), (FfiError, String)> {
    let inputs = extract_inputs(&handle.input_names, &options.inputs)
        .map_err(|error| (error, String::new()))?;
    let mut prints = PrintCollector::new();
    let tracker = ResourceTracker::new(options.limits.map_or_default(Into::into));

    let start_result =
        handle
            .runner
            .clone()
            .start(inputs, tracker, PrintWriter::Callback(&mut prints));

    match start_result {
        Ok(progress) => finish_run_step(progress, handle.script_name.clone(), 0, prints),
        Err(error) => Err((FfiError::Exception(error), prints.into_string())),
    }
}

#[expect(
    clippy::result_large_err,
    reason = "the error carries the REPL session back to the caller so it is not lost"
)]
fn feed_start_internal(
    repl_handle: &mut MontyGoRepl,
    code: &[u8],
    options: WireFeedOptions,
) -> Result<(StoredProgress, String), (FfiError, MontyGoRepl, String)> {
    let Some(stored) = repl_handle.inner.take() else {
        return Err((
            FfiError::Api("repl handle is not available for execution".to_owned()),
            MontyGoRepl {
                script_name: repl_handle.script_name.clone(),
                inner: None,
            },
            String::new(),
        ));
    };

    let code = match decode_utf8(code, "code") {
        Ok(code) => code,
        Err(error) => {
            return Err((
                error,
                wrap_repl_handle(&repl_handle.script_name, stored),
                String::new(),
            ));
        }
    };

    let inputs = match extract_feed_inputs(options.inputs) {
        Ok(inputs) => inputs,
        Err(error) => {
            return Err((
                error,
                wrap_repl_handle(&repl_handle.script_name, stored),
                String::new(),
            ));
        }
    };

    let mut prints = PrintCollector::new();
    let script_name = repl_handle.script_name.clone();
    let StoredRepl {
        repl,
        suspensions_seen,
    } = stored;

    match repl.feed_start(code, inputs, PrintWriter::Callback(&mut prints)) {
        Ok(progress) => finish_repl_step(progress, script_name, suspensions_seen, prints).map_err(
            |(error, repl, prints)| {
                (
                    error,
                    repl.unwrap_or_else(|| MontyGoRepl {
                        script_name: repl_handle.script_name.clone(),
                        inner: None,
                    }),
                    prints,
                )
            },
        ),
        Err(error) => {
            let error = *error;
            Err((
                FfiError::Exception(error.error),
                wrap_repl_handle(
                    &script_name,
                    StoredRepl {
                        repl: error.repl,
                        suspensions_seen,
                    },
                ),
                prints.into_string(),
            ))
        }
    }
}

#[expect(
    clippy::result_large_err,
    reason = "the error carries the REPL session back to the caller so it is not lost"
)]
fn resume_call_internal(
    progress: StoredProgress,
    ext_result: ExtFunctionResult,
) -> Result<(StoredProgress, String), (FfiError, Option<MontyGoRepl>, String)> {
    let mut prints = PrintCollector::new();
    match progress {
        StoredProgress::Run {
            progress: RunProgress::FunctionCall(call),
            script_name,
            suspensions_seen,
        } => {
            let call_id = call.call_id;
            match call.resume(
                adjust_pending_result(ext_result, call_id),
                PrintWriter::Callback(&mut prints),
            ) {
                Ok(progress) => finish_run_step(progress, script_name, suspensions_seen, prints)
                    .map_err(|(error, prints)| (error, None, prints)),
                Err(error) => Err((FfiError::Exception(error), None, prints.into_string())),
            }
        }
        StoredProgress::Run {
            progress: RunProgress::OsCall(call),
            script_name,
            suspensions_seen,
        } => {
            let call_id = call.call_id;
            match call.resume(
                adjust_pending_result(ext_result, call_id),
                PrintWriter::Callback(&mut prints),
            ) {
                Ok(progress) => finish_run_step(progress, script_name, suspensions_seen, prints)
                    .map_err(|(error, prints)| (error, None, prints)),
                Err(error) => Err((FfiError::Exception(error), None, prints.into_string())),
            }
        }
        StoredProgress::Repl {
            progress: ReplProgress::FunctionCall(call),
            script_name,
            suspensions_seen,
        } => {
            let call_id = call.call_id;
            match call.resume(
                adjust_pending_result(ext_result, call_id),
                PrintWriter::Callback(&mut prints),
            ) {
                Ok(progress) => finish_repl_step(progress, script_name, suspensions_seen, prints),
                Err(error) => {
                    let error = *error;
                    Err((
                        FfiError::Exception(error.error),
                        Some(wrap_repl_handle(
                            &script_name,
                            StoredRepl {
                                repl: error.repl,
                                suspensions_seen,
                            },
                        )),
                        prints.into_string(),
                    ))
                }
            }
        }
        StoredProgress::Repl {
            progress: ReplProgress::OsCall(call),
            script_name,
            suspensions_seen,
        } => {
            let call_id = call.call_id;
            match call.resume(
                adjust_pending_result(ext_result, call_id),
                PrintWriter::Callback(&mut prints),
            ) {
                Ok(progress) => finish_repl_step(progress, script_name, suspensions_seen, prints),
                Err(error) => {
                    let error = *error;
                    Err((
                        FfiError::Exception(error.error),
                        Some(wrap_repl_handle(
                            &script_name,
                            StoredRepl {
                                repl: error.repl,
                                suspensions_seen,
                            },
                        )),
                        prints.into_string(),
                    ))
                }
            }
        }
        _ => Err((
            FfiError::Api("progress is not a function or os-call snapshot".to_owned()),
            None,
            prints.into_string(),
        )),
    }
}

fn adjust_pending_result(result: ExtFunctionResult, call_id: u32) -> ExtFunctionResult {
    match result {
        ExtFunctionResult::Future(_) => ExtFunctionResult::Future(call_id),
        other => other,
    }
}

#[expect(
    clippy::result_large_err,
    reason = "the error carries the REPL session back to the caller so it is not lost"
)]
fn resume_lookup_internal(
    progress: StoredProgress,
    lookup_result: NameLookupResult,
) -> Result<(StoredProgress, String), (FfiError, Option<MontyGoRepl>, String)> {
    let mut prints = PrintCollector::new();
    match progress {
        StoredProgress::Run {
            progress: RunProgress::NameLookup(lookup),
            script_name,
            suspensions_seen,
        } => match lookup.resume(lookup_result, PrintWriter::Callback(&mut prints)) {
            Ok(progress) => finish_run_step(progress, script_name, suspensions_seen, prints)
                .map_err(|(error, prints)| (error, None, prints)),
            Err(error) => Err((FfiError::Exception(error), None, prints.into_string())),
        },
        StoredProgress::Repl {
            progress: ReplProgress::NameLookup(lookup),
            script_name,
            suspensions_seen,
        } => match lookup.resume(lookup_result, PrintWriter::Callback(&mut prints)) {
            Ok(progress) => finish_repl_step(progress, script_name, suspensions_seen, prints),
            Err(error) => {
                let error = *error;
                Err((
                    FfiError::Exception(error.error),
                    Some(wrap_repl_handle(
                        &script_name,
                        StoredRepl {
                            repl: error.repl,
                            suspensions_seen,
                        },
                    )),
                    prints.into_string(),
                ))
            }
        },
        _ => Err((
            FfiError::Api("progress is not a name-lookup snapshot".to_owned()),
            None,
            prints.into_string(),
        )),
    }
}

#[expect(
    clippy::result_large_err,
    reason = "the error carries the REPL session back to the caller so it is not lost"
)]
fn resume_futures_internal(
    progress: StoredProgress,
    decoded: Vec<(u32, ExtFunctionResult)>,
) -> Result<(StoredProgress, String), (FfiError, Option<MontyGoRepl>, String)> {
    let mut prints = PrintCollector::new();
    match progress {
        StoredProgress::Run {
            progress: RunProgress::ResolveFutures(state),
            script_name,
            suspensions_seen,
        } => match state.resume(decoded, PrintWriter::Callback(&mut prints)) {
            Ok(progress) => finish_run_step(progress, script_name, suspensions_seen, prints)
                .map_err(|(error, prints)| (error, None, prints)),
            Err(error) => Err((FfiError::Exception(error), None, prints.into_string())),
        },
        StoredProgress::Repl {
            progress: ReplProgress::ResolveFutures(state),
            script_name,
            suspensions_seen,
        } => match state.resume(decoded, PrintWriter::Callback(&mut prints)) {
            Ok(progress) => finish_repl_step(progress, script_name, suspensions_seen, prints),
            Err(error) => {
                let error = *error;
                Err((
                    FfiError::Exception(error.error),
                    Some(wrap_repl_handle(
                        &script_name,
                        StoredRepl {
                            repl: error.repl,
                            suspensions_seen,
                        },
                    )),
                    prints.into_string(),
                ))
            }
        },
        _ => Err((
            FfiError::Api("progress is not a future snapshot".to_owned()),
            None,
            prints.into_string(),
        )),
    }
}

#[expect(
    clippy::result_large_err,
    reason = "the error carries the REPL session back to the caller so it is not lost"
)]
fn abort_internal(
    progress: StoredProgress,
    exc: MontyException,
) -> Result<(StoredProgress, String), (FfiError, Option<MontyGoRepl>, String)> {
    let mut prints = PrintCollector::new();
    match progress {
        StoredProgress::Run { progress, .. } => {
            let err = abort_run_progress(progress, exc, &mut prints);
            Err((err, None, prints.into_string()))
        }
        StoredProgress::Repl {
            progress,
            script_name,
            suspensions_seen,
        } => {
            let (err, repl) = abort_repl_progress(progress, exc, &mut prints);
            Err((
                err,
                Some(wrap_repl_handle(
                    &script_name,
                    StoredRepl {
                        repl,
                        suspensions_seen,
                    },
                )),
                prints.into_string(),
            ))
        }
    }
}

#[unsafe(no_mangle)]
pub extern "C" fn monty_go_bytes_free(ptr: *mut u8, len: usize) {
    if !ptr.is_null() && len > 0 {
        // SAFETY: ptr/len came from `MontyGoBytes::from_vec`, which leaks a
        // `Box<[u8]>`, so rebuilding that box frees exactly the same allocation.
        catch_drop(|| unsafe {
            drop(Box::from_raw(ptr::slice_from_raw_parts_mut(ptr, len)));
        });
    }
}

#[unsafe(no_mangle)]
pub extern "C" fn monty_go_runner_free(runner: *mut MontyGoRunner) {
    if !runner.is_null() {
        // SAFETY: pointer was created by `Box::into_raw`
        catch_drop(|| unsafe { drop(Box::from_raw(runner)) });
    }
}

#[unsafe(no_mangle)]
pub extern "C" fn monty_go_repl_free(repl: *mut MontyGoRepl) {
    if !repl.is_null() {
        // SAFETY: pointer was created by `Box::into_raw`
        catch_drop(|| unsafe { drop(Box::from_raw(repl)) });
    }
}

#[unsafe(no_mangle)]
pub extern "C" fn monty_go_progress_free(progress: *mut MontyGoProgress) {
    if !progress.is_null() {
        // SAFETY: pointer was created by `Box::into_raw`
        catch_drop(|| unsafe { drop(Box::from_raw(progress)) });
    }
}

#[unsafe(no_mangle)]
pub extern "C" fn monty_go_error_free(error: *mut MontyGoError) {
    if !error.is_null() {
        // SAFETY: pointer was created by `Box::into_raw`
        catch_drop(|| unsafe { drop(Box::from_raw(error)) });
    }
}

#[unsafe(no_mangle)]
pub extern "C" fn monty_go_error_json(error: *const MontyGoError, out: *mut MontyGoBytes) {
    let bytes = if error.is_null() {
        MontyGoBytes::empty()
    } else {
        // SAFETY: caller passes a valid handle pointer
        let error = unsafe { &*error };
        catch_or(
            || MontyGoBytes::from_vec(serde_json::to_vec(&panic_summary()).unwrap_or_default()),
            || {
                serde_json::to_vec(&error.inner.summary())
                    .map_or_else(|_| MontyGoBytes::empty(), MontyGoBytes::from_vec)
            },
        )
    };
    if !out.is_null() {
        // SAFETY: caller owns the out pointer
        unsafe { *out = bytes };
    }
}

#[unsafe(no_mangle)]
pub extern "C" fn monty_go_error_display(
    error: *const MontyGoError,
    format: *const c_char,
    color: bool,
    out: *mut MontyGoBytes,
) {
    let bytes = if error.is_null() {
        MontyGoBytes::empty()
    } else {
        // SAFETY: caller passes a valid handle pointer
        let error = unsafe { &*error };
        // SAFETY: the caller passes a NUL-terminated string, or null (which returns an error)
        let format = unsafe { string_from_cstr(format) }.unwrap_or("traceback");
        catch_or(
            || MontyGoBytes::from_vec(PANIC_RENDERING_ERROR.as_bytes().to_vec()),
            || {
                MontyGoBytes::from_vec(
                    error
                        .inner
                        .display(format, color)
                        .unwrap_or_else(|display_error| display_error)
                        .into_bytes(),
                )
            },
        )
    };
    if !out.is_null() {
        // SAFETY: caller owns the out pointer
        unsafe { *out = bytes };
    }
}

#[unsafe(no_mangle)]
pub extern "C" fn monty_go_runner_new(
    code_ptr: *const u8,
    code_len: usize,
    options_ptr: *const u8,
    options_len: usize,
    out: *mut MontyGoRunnerResult,
) {
    let result = catch_runner_result(|| {
        // SAFETY: the caller passes a readable (ptr, len) pair; null or zero length yields an empty slice
        let code = unsafe { slice_from_raw(code_ptr, code_len) };
        // SAFETY: the caller passes a readable (ptr, len) pair; null or zero length yields an empty slice
        let options = unsafe { slice_from_raw(options_ptr, options_len) };

        let code = match std::str::from_utf8(code) {
            Ok(code) => code.to_owned(),
            Err(error) => {
                return MontyGoRunnerResult::err(FfiError::Api(format!(
                    "code is not valid UTF-8: {error}"
                )));
            }
        };
        let options: WireCompileOptions = match decode_wire(options) {
            Ok(options) => options,
            Err(error) => return MontyGoRunnerResult::err(error),
        };

        let script_name = options.script_name.unwrap_or_else(|| "main.py".to_owned());
        let input_names = options.inputs.unwrap_or_default();

        if options.type_check {
            let failure = TypingFailure {
                source_code: code.clone(),
                script_name: script_name.clone(),
                stubs: options.type_check_stubs,
            };
            let mut checker = TypeChecker::default();
            let source = SourceFile::new(&code, &script_name);
            let stubs = failure
                .stubs
                .as_deref()
                .map(|stubs| SourceFile::new(stubs, "type_stubs.py"));
            match checker.run(&source, stubs.as_ref(), TypeCheckingConfig::default()) {
                Ok(Some(_)) => return MontyGoRunnerResult::err(FfiError::Typing(failure)),
                Ok(None) => {}
                Err(error) => return MontyGoRunnerResult::err(FfiError::Api(error)),
            }
        }

        match MontyRun::new(
            code,
            &script_name,
            input_names.clone(),
            CompileOptions::default(),
        ) {
            Ok(runner) => MontyGoRunnerResult::ok(MontyGoRunner {
                runner,
                script_name,
                input_names,
            }),
            Err(error) => MontyGoRunnerResult::err(FfiError::Exception(error)),
        }
    });
    if !out.is_null() {
        // SAFETY: caller owns the out pointer
        unsafe { *out = result };
    }
}

#[unsafe(no_mangle)]
pub extern "C" fn monty_go_runner_load(
    data_ptr: *const u8,
    data_len: usize,
    out: *mut MontyGoRunnerResult,
) {
    let result = catch_runner_result(|| {
        // SAFETY: the caller passes a readable (ptr, len) pair; null or zero length yields an empty slice
        let data = unsafe { slice_from_raw(data_ptr, data_len) };
        match minicbor_serde::from_slice::<StoredRunner>(data) {
            Ok(stored) => MontyGoRunnerResult::ok(MontyGoRunner {
                runner: stored.runner,
                script_name: stored.script_name,
                input_names: stored.input_names,
            }),
            Err(error) => MontyGoRunnerResult::err(FfiError::Api(error.to_string())),
        }
    });
    if !out.is_null() {
        // SAFETY: caller owns the out pointer
        unsafe { *out = result };
    }
}

#[unsafe(no_mangle)]
pub extern "C" fn monty_go_runner_dump(
    runner: *const MontyGoRunner,
    out: *mut MontyGoBytes,
    error_out: *mut *mut MontyGoError,
) {
    if !error_out.is_null() {
        // SAFETY: caller owns the out pointer
        unsafe { *error_out = ptr::null_mut() };
    }
    let bytes = catch_bytes_result(error_out, || {
        if runner.is_null() {
            if !error_out.is_null() {
                // SAFETY: the caller owns `error_out`, and it was null-checked just above
                unsafe {
                    *error_out = Box::into_raw(Box::new(MontyGoError {
                        inner: FfiError::Api("runner handle is null".to_owned()),
                    }));
                };
            }
            return MontyGoBytes::empty();
        }
        // SAFETY: caller passes a valid handle pointer
        let runner = unsafe { &*runner };

        match minicbor_serde::to_vec(&StoredRunnerRef {
            runner: &runner.runner,
            script_name: &runner.script_name,
            input_names: &runner.input_names,
        }) {
            Ok(bytes) => MontyGoBytes::from_vec(bytes),
            Err(error) => {
                if !error_out.is_null() {
                    // SAFETY: the caller owns `error_out`, and it was null-checked just above
                    unsafe {
                        *error_out = Box::into_raw(Box::new(MontyGoError {
                            inner: FfiError::Api(error.to_string()),
                        }));
                    };
                }
                MontyGoBytes::empty()
            }
        }
    });
    if !out.is_null() {
        // SAFETY: caller owns the out pointer
        unsafe { *out = bytes };
    }
}

#[unsafe(no_mangle)]
pub extern "C" fn monty_go_runner_type_check(
    runner: *const MontyGoRunner,
    prefix_ptr: *const u8,
    prefix_len: usize,
) -> *mut MontyGoError {
    catch_error_result(|| {
        if runner.is_null() {
            return Box::into_raw(Box::new(MontyGoError {
                inner: FfiError::Api("runner handle is null".to_owned()),
            }));
        }
        // SAFETY: caller passes a valid handle pointer
        let runner = unsafe { &*runner };
        // SAFETY: the caller passes a readable (ptr, len) pair; null or zero length yields an empty slice
        let prefix = unsafe { slice_from_raw(prefix_ptr, prefix_len) };
        let prefix = if prefix.is_empty() {
            None
        } else {
            match std::str::from_utf8(prefix) {
                Ok(prefix) => Some(prefix.to_owned()),
                Err(error) => {
                    return Box::into_raw(Box::new(MontyGoError {
                        inner: FfiError::Api(format!("prefix is not valid UTF-8: {error}")),
                    }));
                }
            }
        };

        let mut checker = TypeChecker::default();
        let source = SourceFile::new(runner.runner.code(), &runner.script_name);
        let stub_source = prefix
            .as_deref()
            .map(|prefix| SourceFile::new(prefix, "type_stubs.py"));
        match checker.run(&source, stub_source.as_ref(), TypeCheckingConfig::default()) {
            Ok(None) => ptr::null_mut(),
            Ok(Some(_)) => Box::into_raw(Box::new(MontyGoError {
                inner: FfiError::Typing(TypingFailure {
                    source_code: runner.runner.code().to_owned(),
                    script_name: runner.script_name.clone(),
                    stubs: prefix,
                }),
            })),
            Err(error) => Box::into_raw(Box::new(MontyGoError {
                inner: FfiError::Api(error),
            })),
        }
    })
}

#[unsafe(no_mangle)]
pub extern "C" fn monty_go_runner_start(
    runner: *const MontyGoRunner,
    options_ptr: *const u8,
    options_len: usize,
    out: *mut MontyGoOpResult,
) {
    let result = catch_op_result(|| {
        if runner.is_null() {
            return MontyGoOpResult::err(
                FfiError::Api("runner handle is null".to_owned()),
                None,
                String::new(),
            );
        }
        // SAFETY: caller passes a valid handle pointer
        let runner = unsafe { &*runner };
        // SAFETY: the caller passes a readable (ptr, len) pair; null or zero length yields an empty slice
        let options = unsafe { slice_from_raw(options_ptr, options_len) };
        let options: WireStartOptions = match decode_wire(options) {
            Ok(options) => options,
            Err(error) => return MontyGoOpResult::err(error, None, String::new()),
        };
        match start_runner_internal(runner, options) {
            Ok((progress, prints)) => MontyGoOpResult::ok(progress, prints),
            Err((error, prints)) => MontyGoOpResult::err(error, None, prints),
        }
    });
    if !out.is_null() {
        // SAFETY: caller owns the out pointer
        unsafe { *out = result };
    }
}

#[unsafe(no_mangle)]
pub extern "C" fn monty_go_repl_new(
    options_ptr: *const u8,
    options_len: usize,
    out: *mut MontyGoReplResult,
) {
    let result = catch_repl_result(|| {
        // SAFETY: the caller passes a readable (ptr, len) pair; null or zero length yields an empty slice
        let options = unsafe { slice_from_raw(options_ptr, options_len) };
        let options: WireReplOptions = match decode_wire(options) {
            Ok(options) => options,
            Err(error) => return MontyGoReplResult::err(error),
        };
        let script_name = options.script_name.unwrap_or_else(|| "main.py".to_owned());
        let tracker = ResourceTracker::new(options.limits.map_or_default(Into::into));
        let inner = StoredRepl {
            repl: MontyRepl::new(&script_name, tracker, CompileOptions::default()),
            suspensions_seen: 0,
        };

        MontyGoReplResult::ok(MontyGoRepl {
            script_name,
            inner: Some(inner),
        })
    });
    if !out.is_null() {
        // SAFETY: caller owns the out pointer
        unsafe { *out = result };
    }
}

#[unsafe(no_mangle)]
pub extern "C" fn monty_go_repl_load(
    data_ptr: *const u8,
    data_len: usize,
    out: *mut MontyGoReplResult,
) {
    let result = catch_repl_result(|| {
        // SAFETY: the caller passes a readable (ptr, len) pair; null or zero length yields an empty slice
        let data = unsafe { slice_from_raw(data_ptr, data_len) };
        let (suspensions_seen, data) = match decode_dump_with_suspensions(data, 0) {
            Ok(decoded) => decoded,
            Err(error) => return MontyGoReplResult::err(error),
        };
        match Dump::load(data) {
            Ok(Dump {
                script_name,
                state: Session::Idle(repl),
                ..
            }) => MontyGoReplResult::ok(MontyGoRepl {
                script_name,
                inner: Some(StoredRepl {
                    repl: *repl,
                    suspensions_seen,
                }),
            }),
            Ok(_) => MontyGoReplResult::err(FfiError::Api(
                "dump does not hold an idle REPL session".to_owned(),
            )),
            Err(error) => MontyGoReplResult::err(FfiError::Api(error.to_string())),
        }
    });
    if !out.is_null() {
        // SAFETY: caller owns the out pointer
        unsafe { *out = result };
    }
}

#[unsafe(no_mangle)]
pub extern "C" fn monty_go_repl_dump(
    repl: *const MontyGoRepl,
    out: *mut MontyGoBytes,
    error_out: *mut *mut MontyGoError,
) {
    if !error_out.is_null() {
        // SAFETY: the caller owns `error_out`, and it was null-checked just above
        unsafe { *error_out = ptr::null_mut() };
    }
    let bytes = catch_bytes_result(error_out, || {
        if repl.is_null() {
            if !error_out.is_null() {
                // SAFETY: the caller owns `error_out`, and it was null-checked just above
                unsafe {
                    *error_out = Box::into_raw(Box::new(MontyGoError {
                        inner: FfiError::Api("repl handle is null".to_owned()),
                    }));
                };
            }
            return MontyGoBytes::empty();
        }
        // SAFETY: the caller passes a valid handle pointer
        let repl = unsafe { &*repl };
        let Some(inner) = &repl.inner else {
            if !error_out.is_null() {
                // SAFETY: the caller owns `error_out`, and it was null-checked just above
                unsafe {
                    *error_out = Box::into_raw(Box::new(MontyGoError {
                        inner: FfiError::Api("repl handle is not currently available".to_owned()),
                    }));
                };
            }
            return MontyGoBytes::empty();
        };
        match dump(&repl.script_name, None, &[], SessionRef::Idle(&inner.repl)) {
            Ok(bytes) => {
                MontyGoBytes::from_vec(encode_dump_with_suspensions(&bytes, inner.suspensions_seen))
            }
            Err(error) => {
                if !error_out.is_null() {
                    // SAFETY: the caller owns `error_out`, and it was null-checked just above
                    unsafe {
                        *error_out = Box::into_raw(Box::new(MontyGoError {
                            inner: FfiError::Api(error.to_string()),
                        }));
                    };
                }
                MontyGoBytes::empty()
            }
        }
    });
    if !out.is_null() {
        // SAFETY: caller owns the out pointer
        unsafe { *out = bytes };
    }
}

#[unsafe(no_mangle)]
pub extern "C" fn monty_go_repl_feed_start(
    repl: *mut MontyGoRepl,
    code_ptr: *const u8,
    code_len: usize,
    options_ptr: *const u8,
    options_len: usize,
    out: *mut MontyGoOpResult,
) {
    let result = catch_op_result(|| {
        if repl.is_null() {
            return MontyGoOpResult::err(
                FfiError::Api("repl handle is null".to_owned()),
                None,
                String::new(),
            );
        }
        // SAFETY: the caller passes a valid handle pointer and does not use it concurrently (the Go wrapper enforces this with borrow())
        let repl = unsafe { &mut *repl };
        // SAFETY: the caller passes a readable (ptr, len) pair; null or zero length yields an empty slice
        let code = unsafe { slice_from_raw(code_ptr, code_len) };
        // SAFETY: the caller passes a readable (ptr, len) pair; null or zero length yields an empty slice
        let options = unsafe { slice_from_raw(options_ptr, options_len) };
        let options: WireFeedOptions = match decode_wire(options) {
            Ok(options) => options,
            Err(error) => return MontyGoOpResult::err(error, None, String::new()),
        };
        match feed_start_internal(repl, code, options) {
            Ok((progress, prints)) => MontyGoOpResult::ok(progress, prints),
            Err((error, repl, prints)) => MontyGoOpResult::err(error, Some(repl), prints),
        }
    });
    if !out.is_null() {
        // SAFETY: caller owns the out pointer
        unsafe { *out = result };
    }
}

#[unsafe(no_mangle)]
pub extern "C" fn monty_go_progress_describe(
    progress: *const MontyGoProgress,
    out: *mut MontyGoBytes,
    error_out: *mut *mut MontyGoError,
) {
    if !error_out.is_null() {
        // SAFETY: the caller owns `error_out`, and it was null-checked just above
        unsafe { *error_out = ptr::null_mut() };
    }
    let bytes = catch_bytes_result(error_out, || {
        if progress.is_null() {
            if !error_out.is_null() {
                // SAFETY: the caller owns `error_out`, and it was null-checked just above
                unsafe {
                    *error_out = Box::into_raw(Box::new(MontyGoError {
                        inner: FfiError::Api("progress handle is null".to_owned()),
                    }));
                };
            }
            return MontyGoBytes::empty();
        }
        // SAFETY: the caller passes a valid handle pointer
        let progress = unsafe { &*progress };
        let Some(inner) = &progress.inner else {
            if !error_out.is_null() {
                // SAFETY: the caller owns `error_out`, and it was null-checked just above
                unsafe {
                    *error_out = Box::into_raw(Box::new(MontyGoError {
                        inner: FfiError::Api("progress handle is no longer available".to_owned()),
                    }));
                };
            }
            return MontyGoBytes::empty();
        };
        match encode_wire(&inner.describe()) {
            Ok(bytes) => bytes,
            Err(error) => {
                if !error_out.is_null() {
                    // SAFETY: the caller owns `error_out`, and it was null-checked just above
                    unsafe { *error_out = Box::into_raw(Box::new(MontyGoError { inner: error })) };
                }
                MontyGoBytes::empty()
            }
        }
    });
    if !out.is_null() {
        // SAFETY: caller owns the out pointer
        unsafe { *out = bytes };
    }
}

#[unsafe(no_mangle)]
pub extern "C" fn monty_go_progress_dump(
    progress: *const MontyGoProgress,
    out: *mut MontyGoBytes,
    error_out: *mut *mut MontyGoError,
) {
    if !error_out.is_null() {
        // SAFETY: the caller owns `error_out`, and it was null-checked just above
        unsafe { *error_out = ptr::null_mut() };
    }
    let bytes = catch_bytes_result(error_out, || {
        if progress.is_null() {
            if !error_out.is_null() {
                // SAFETY: the caller owns `error_out`, and it was null-checked just above
                unsafe {
                    *error_out = Box::into_raw(Box::new(MontyGoError {
                        inner: FfiError::Api("progress handle is null".to_owned()),
                    }));
                };
            }
            return MontyGoBytes::empty();
        }
        // SAFETY: the caller passes a valid handle pointer
        let progress = unsafe { &*progress };
        let Some(inner) = &progress.inner else {
            if !error_out.is_null() {
                // SAFETY: the caller owns `error_out`, and it was null-checked just above
                unsafe {
                    *error_out = Box::into_raw(Box::new(MontyGoError {
                        inner: FfiError::Api("progress handle is no longer available".to_owned()),
                    }));
                };
            }
            return MontyGoBytes::empty();
        };
        match inner.dump() {
            Ok(bytes) => MontyGoBytes::from_vec(bytes),
            Err(error) => {
                if !error_out.is_null() {
                    // SAFETY: the caller owns `error_out`, and it was null-checked just above
                    unsafe { *error_out = Box::into_raw(Box::new(MontyGoError { inner: error })) };
                }
                MontyGoBytes::empty()
            }
        }
    });
    if !out.is_null() {
        // SAFETY: caller owns the out pointer
        unsafe { *out = bytes };
    }
}

#[unsafe(no_mangle)]
pub extern "C" fn monty_go_progress_load(
    data_ptr: *const u8,
    data_len: usize,
    out: *mut MontyGoOpResult,
) {
    let result = catch_op_result(|| {
        // SAFETY: the caller passes a readable (ptr, len) pair; null or zero length yields an empty slice
        let data = unsafe { slice_from_raw(data_ptr, data_len) };
        let (suspensions_seen, data) = match decode_dump_with_suspensions(data, 1) {
            Ok(decoded) => decoded,
            Err(error) => return MontyGoOpResult::err(error, None, String::new()),
        };
        let progress = match Dump::load(data) {
            Ok(Dump {
                script_name,
                state: Session::Running(progress),
                ..
            }) => {
                let progress = *progress;
                let suspensions_seen = if matches!(&progress, RunProgress::Complete(_)) {
                    suspensions_seen
                } else {
                    suspensions_seen.max(1)
                };
                StoredProgress::Run {
                    progress,
                    script_name,
                    suspensions_seen,
                }
            }
            Ok(Dump {
                script_name,
                state: Session::Suspended(progress),
                ..
            }) => {
                let progress = *progress;
                let suspensions_seen = if matches!(&progress, ReplProgress::Complete { .. }) {
                    suspensions_seen
                } else {
                    suspensions_seen.max(1)
                };
                StoredProgress::Repl {
                    progress,
                    script_name,
                    suspensions_seen,
                }
            }
            Ok(_) => {
                return MontyGoOpResult::err(
                    FfiError::Api("dump does not hold a suspended session".to_owned()),
                    None,
                    String::new(),
                );
            }
            Err(error) => {
                return MontyGoOpResult::err(FfiError::Api(error.to_string()), None, String::new());
            }
        };
        MontyGoOpResult::ok(progress, String::new())
    });
    if !out.is_null() {
        // SAFETY: caller owns the out pointer
        unsafe { *out = result };
    }
}

#[unsafe(no_mangle)]
pub extern "C" fn monty_go_progress_take_repl(
    progress: *mut MontyGoProgress,
    out: *mut MontyGoReplResult,
) {
    let result = catch_repl_result(|| {
        if progress.is_null() {
            return MontyGoReplResult::err(FfiError::Api("progress handle is null".to_owned()));
        }
        // SAFETY: the caller passes a valid handle pointer and does not use it concurrently (the Go wrapper enforces this with borrow())
        let progress = unsafe { &mut *progress };
        let Some(inner) = progress.inner.take() else {
            return MontyGoReplResult::err(FfiError::Api(
                "progress handle is no longer available".to_owned(),
            ));
        };
        if !matches!(&inner, StoredProgress::Repl { .. }) {
            progress.inner = Some(inner);
            return MontyGoReplResult::err(FfiError::Api(
                "progress handle does not own a REPL session".to_owned(),
            ));
        }
        match inner.into_repl() {
            Ok(repl) => MontyGoReplResult::ok(repl),
            Err(error) => MontyGoReplResult::err(error),
        }
    });
    if !out.is_null() {
        // SAFETY: caller owns the out pointer
        unsafe { *out = result };
    }
}

#[unsafe(no_mangle)]
pub extern "C" fn monty_go_progress_resume_call(
    progress: *mut MontyGoProgress,
    result_ptr: *const u8,
    result_len: usize,
    out: *mut MontyGoOpResult,
) {
    let result = catch_op_result(|| {
        if progress.is_null() {
            return MontyGoOpResult::err(
                FfiError::Api("progress handle is null".to_owned()),
                None,
                String::new(),
            );
        }
        // SAFETY: the caller passes a valid handle pointer and does not use it concurrently (the Go wrapper enforces this with borrow())
        let progress = unsafe { &mut *progress };
        // SAFETY: the caller passes a readable (ptr, len) pair; null or zero length yields an empty slice
        let result = unsafe { slice_from_raw(result_ptr, result_len) };
        let result: WireCallResult = match decode_wire(result) {
            Ok(result) => result,
            Err(error) => return MontyGoOpResult::err(error, None, String::new()),
        };
        let result = match decode_call_result(result) {
            Ok(result) => result,
            Err(error) => return MontyGoOpResult::err(error, None, String::new()),
        };
        if !matches!(
            progress.inner.as_ref(),
            Some(
                StoredProgress::Run {
                    progress: RunProgress::FunctionCall(_) | RunProgress::OsCall(_),
                    ..
                } | StoredProgress::Repl {
                    progress: ReplProgress::FunctionCall(_) | ReplProgress::OsCall(_),
                    ..
                }
            )
        ) {
            return MontyGoOpResult::err(
                FfiError::Api("progress is not a function or os-call snapshot".to_owned()),
                None,
                String::new(),
            );
        }
        let Some(inner) = progress.inner.take() else {
            return MontyGoOpResult::err(
                FfiError::Api("progress handle is no longer available".to_owned()),
                None,
                String::new(),
            );
        };
        match resume_call_internal(inner, result) {
            Ok((progress, prints)) => MontyGoOpResult::ok(progress, prints),
            Err((error, repl, prints)) => MontyGoOpResult::err(error, repl, prints),
        }
    });
    if !out.is_null() {
        // SAFETY: caller owns the out pointer
        unsafe { *out = result };
    }
}

#[unsafe(no_mangle)]
pub extern "C" fn monty_go_progress_resume_lookup(
    progress: *mut MontyGoProgress,
    result_ptr: *const u8,
    result_len: usize,
    out: *mut MontyGoOpResult,
) {
    let result = catch_op_result(|| {
        if progress.is_null() {
            return MontyGoOpResult::err(
                FfiError::Api("progress handle is null".to_owned()),
                None,
                String::new(),
            );
        }
        // SAFETY: the caller passes a valid handle pointer and does not use it concurrently (the Go wrapper enforces this with borrow())
        let progress = unsafe { &mut *progress };
        // SAFETY: the caller passes a readable (ptr, len) pair; null or zero length yields an empty slice
        let result = unsafe { slice_from_raw(result_ptr, result_len) };
        let result: WireLookupResult = match decode_wire(result) {
            Ok(result) => result,
            Err(error) => return MontyGoOpResult::err(error, None, String::new()),
        };
        let result = match decode_lookup_result(result) {
            Ok(result) => result,
            Err(error) => return MontyGoOpResult::err(error, None, String::new()),
        };
        if !matches!(
            progress.inner.as_ref(),
            Some(
                StoredProgress::Run {
                    progress: RunProgress::NameLookup(_),
                    ..
                } | StoredProgress::Repl {
                    progress: ReplProgress::NameLookup(_),
                    ..
                }
            )
        ) {
            return MontyGoOpResult::err(
                FfiError::Api("progress is not a name-lookup snapshot".to_owned()),
                None,
                String::new(),
            );
        }
        let Some(inner) = progress.inner.take() else {
            return MontyGoOpResult::err(
                FfiError::Api("progress handle is no longer available".to_owned()),
                None,
                String::new(),
            );
        };
        match resume_lookup_internal(inner, result) {
            Ok((progress, prints)) => MontyGoOpResult::ok(progress, prints),
            Err((error, repl, prints)) => MontyGoOpResult::err(error, repl, prints),
        }
    });
    if !out.is_null() {
        // SAFETY: caller owns the out pointer
        unsafe { *out = result };
    }
}

#[unsafe(no_mangle)]
pub extern "C" fn monty_go_progress_resume_futures(
    progress: *mut MontyGoProgress,
    results_ptr: *const u8,
    results_len: usize,
    out: *mut MontyGoOpResult,
) {
    let result = catch_op_result(|| {
        if progress.is_null() {
            return MontyGoOpResult::err(
                FfiError::Api("progress handle is null".to_owned()),
                None,
                String::new(),
            );
        }
        // SAFETY: the caller passes a valid handle pointer and does not use it concurrently (the Go wrapper enforces this with borrow())
        let progress = unsafe { &mut *progress };
        // SAFETY: the caller passes a readable (ptr, len) pair; null or zero length yields an empty slice
        let results = unsafe { slice_from_raw(results_ptr, results_len) };
        let results: WireFutureResults = match decode_wire(results) {
            Ok(results) => results,
            Err(error) => return MontyGoOpResult::err(error, None, String::new()),
        };
        let results = match decode_future_results(results) {
            Ok(results) => results,
            Err(error) => return MontyGoOpResult::err(error, None, String::new()),
        };
        if !matches!(
            progress.inner.as_ref(),
            Some(
                StoredProgress::Run {
                    progress: RunProgress::ResolveFutures(_),
                    ..
                } | StoredProgress::Repl {
                    progress: ReplProgress::ResolveFutures(_),
                    ..
                }
            )
        ) {
            return MontyGoOpResult::err(
                FfiError::Api("progress is not a future snapshot".to_owned()),
                None,
                String::new(),
            );
        }
        let Some(inner) = progress.inner.take() else {
            return MontyGoOpResult::err(
                FfiError::Api("progress handle is no longer available".to_owned()),
                None,
                String::new(),
            );
        };
        match resume_futures_internal(inner, results) {
            Ok((progress, prints)) => MontyGoOpResult::ok(progress, prints),
            Err((error, repl, prints)) => MontyGoOpResult::err(error, repl, prints),
        }
    });
    if !out.is_null() {
        // SAFETY: caller owns the out pointer
        unsafe { *out = result };
    }
}

#[unsafe(no_mangle)]
pub extern "C" fn monty_go_progress_abort(
    progress: *mut MontyGoProgress,
    result_ptr: *const u8,
    result_len: usize,
    out: *mut MontyGoOpResult,
) {
    let result = catch_op_result(|| {
        if progress.is_null() {
            return MontyGoOpResult::err(
                FfiError::Api("progress handle is null".to_owned()),
                None,
                String::new(),
            );
        }
        // SAFETY: the caller passes a valid handle pointer and does not use it concurrently (the Go wrapper enforces this with borrow())
        let progress = unsafe { &mut *progress };
        // SAFETY: the caller passes a readable (ptr, len) pair; null or zero length yields an empty slice
        let result = unsafe { slice_from_raw(result_ptr, result_len) };
        let result: WireCallResult = match decode_wire(result) {
            Ok(result) => result,
            Err(error) => return MontyGoOpResult::err(error, None, String::new()),
        };
        if result.kind != WIRE_CALL_RESULT_EXCEPTION {
            return MontyGoOpResult::err(
                FfiError::Api(format!(
                    "expected exception result for abort, got kind {}",
                    result.kind
                )),
                None,
                String::new(),
            );
        }
        let exc_type = match parse_exc_type(&result.exc_type) {
            Ok(exc_type) => exc_type,
            Err(error) => return MontyGoOpResult::err(error, None, String::new()),
        };
        let exc = MontyException::new(exc_type, result.arg);
        if matches!(
            progress.inner.as_ref(),
            Some(
                StoredProgress::Run {
                    progress: RunProgress::Complete(_),
                    ..
                } | StoredProgress::Repl {
                    progress: ReplProgress::Complete { .. },
                    ..
                }
            )
        ) {
            return MontyGoOpResult::err(
                FfiError::Api("completed execution cannot be aborted".to_owned()),
                None,
                String::new(),
            );
        }
        let Some(inner) = progress.inner.take() else {
            return MontyGoOpResult::err(
                FfiError::Api("progress handle is no longer available".to_owned()),
                None,
                String::new(),
            );
        };
        match abort_internal(inner, exc) {
            Ok((progress, prints)) => MontyGoOpResult::ok(progress, prints),
            Err((error, repl, prints)) => MontyGoOpResult::err(error, repl, prints),
        }
    });
    if !out.is_null() {
        // SAFETY: caller owns the out pointer
        unsafe { *out = result };
    }
}

#[cfg(test)]
mod panic_guard_tests {
    use super::*;

    fn bytes_to_string(bytes: &MontyGoBytes) -> String {
        let text = if bytes.ptr.is_null() {
            String::new()
        } else {
            // SAFETY: ptr/len came from `MontyGoBytes::from_vec`
            String::from_utf8_lossy(unsafe { std::slice::from_raw_parts(bytes.ptr, bytes.len) })
                .into_owned()
        };
        monty_go_bytes_free(bytes.ptr, bytes.len);
        text
    }

    #[test]
    fn catch_or_returns_fallback_on_panic() {
        assert_eq!(catch_or(|| 7, || panic!("boom")), 7);
        assert_eq!(catch_or(|| 7, || 3), 3);
    }

    #[test]
    fn catch_drop_swallows_a_panicking_destructor() {
        struct PanicsOnDrop;
        impl Drop for PanicsOnDrop {
            fn drop(&mut self) {
                panic!("drop panicked");
            }
        }
        let value = PanicsOnDrop;
        catch_drop(move || drop(value));
    }

    #[test]
    fn free_functions_accept_null() {
        monty_go_bytes_free(ptr::null_mut(), 0);
        monty_go_runner_free(ptr::null_mut());
        monty_go_repl_free(ptr::null_mut());
        monty_go_progress_free(ptr::null_mut());
        monty_go_error_free(ptr::null_mut());
    }

    #[test]
    fn free_functions_release_handles() {
        let error = Box::into_raw(Box::new(MontyGoError {
            inner: FfiError::Api("x".to_owned()),
        }));
        monty_go_error_free(error);
        let bytes = MontyGoBytes::from_vec(vec![1, 2, 3]);
        monty_go_bytes_free(bytes.ptr, bytes.len);
    }

    #[test]
    fn error_json_and_display_work_for_a_normal_error() {
        let error = Box::into_raw(Box::new(MontyGoError {
            inner: FfiError::Api("hello".to_owned()),
        }));
        let mut json = MontyGoBytes::empty();
        monty_go_error_json(error, &raw mut json);
        assert!(bytes_to_string(&json).contains("hello"));

        let mut shown = MontyGoBytes::empty();
        monty_go_error_display(error, c"msg".as_ptr(), false, &raw mut shown);
        assert_eq!(bytes_to_string(&shown), "hello");
        monty_go_error_free(error);
    }

    #[test]
    fn panic_summary_is_valid_json() {
        let json = serde_json::to_string(&panic_summary()).expect("serializes");
        assert!(json.contains(PANIC_RENDERING_ERROR));
    }

    #[test]
    fn progress_abort_handles_null_and_uncatchable_exception() {
        let mut out = MontyGoOpResult {
            progress: ptr::null_mut(),
            progress_payload: MontyGoBytes::empty(),
            error: ptr::null_mut(),
            repl: ptr::null_mut(),
            prints: MontyGoBytes::empty(),
        };
        monty_go_progress_abort(ptr::null_mut(), ptr::null(), 0, &raw mut out);
        assert!(!out.error.is_null());
        monty_go_error_free(out.error);

        let runner = MontyRun::new(
            "try:\n    ext()\nexcept BaseException:\n    'caught'".to_owned(),
            "abort.py",
            Vec::new(),
            CompileOptions::default(),
        )
        .expect("compiles");
        let progress = runner
            .start(
                Vec::new(),
                ResourceTracker::default(),
                PrintWriter::Callback(&mut PrintCollector::new()),
            )
            .expect("starts");
        let progress_ptr = Box::into_raw(Box::new(MontyGoProgress {
            inner: Some(StoredProgress::Run {
                progress,
                script_name: "abort.py".to_owned(),
                suspensions_seen: 1,
            }),
        }));
        let payload = rmp_serde::to_vec_named(&WireCallResult {
            kind: WIRE_CALL_RESULT_EXCEPTION,
            exc_type: "RuntimeError".to_owned(),
            arg: Some("stopped by host".to_owned()),
            ..WireCallResult::default()
        })
        .expect("encodes abort exception");
        let mut abort_out = MontyGoOpResult {
            progress: ptr::null_mut(),
            progress_payload: MontyGoBytes::empty(),
            error: ptr::null_mut(),
            repl: ptr::null_mut(),
            prints: MontyGoBytes::empty(),
        };
        monty_go_progress_abort(
            progress_ptr,
            payload.as_ptr(),
            payload.len(),
            &raw mut abort_out,
        );
        assert!(abort_out.progress.is_null());
        assert!(!abort_out.error.is_null());
        let mut json = MontyGoBytes::empty();
        monty_go_error_json(abort_out.error, &raw mut json);
        assert!(bytes_to_string(&json).contains("stopped by host"));
        monty_go_error_free(abort_out.error);
        monty_go_progress_free(progress_ptr);
    }
}
