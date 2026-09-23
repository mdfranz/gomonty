//! Binary wire types used at the Rust/Go boundary.
//!
//! These types are serialized with MessagePack on the hot path so the Go
//! wrapper can avoid JSON decoding for every start/resume/complete step. Error
//! summaries remain JSON because they are not performance-sensitive and are
//! exposed for formatting/debugging.

use std::{collections::BTreeMap, time::Duration};

use monty_types::{
    ExcType, MontyDate, MontyDateTime, MontyException, MontyObject, MontyTime, MontyTimeDelta,
    MontyTimeZone, MontyUuid, ObjectRef, ResourceLimits, StackFrame,
    unstable::{self, MontyNode},
};
use num_bigint::BigInt;
use serde::{Deserialize, Serialize};

/// Current schema version for the Go FFI.
pub const WIRE_VERSION: u32 = 1;

pub const WIRE_VALUE_NONE: u8 = 0;
pub const WIRE_VALUE_ELLIPSIS: u8 = 1;
pub const WIRE_VALUE_BOOL: u8 = 2;
pub const WIRE_VALUE_INT: u8 = 3;
pub const WIRE_VALUE_BIG_INT: u8 = 4;
pub const WIRE_VALUE_FLOAT: u8 = 5;
pub const WIRE_VALUE_STRING: u8 = 6;
pub const WIRE_VALUE_BYTES: u8 = 7;
pub const WIRE_VALUE_LIST: u8 = 8;
pub const WIRE_VALUE_TUPLE: u8 = 9;
pub const WIRE_VALUE_NAMED_TUPLE: u8 = 10;
pub const WIRE_VALUE_DICT: u8 = 11;
pub const WIRE_VALUE_SET: u8 = 12;
pub const WIRE_VALUE_FROZEN_SET: u8 = 13;
pub const WIRE_VALUE_EXCEPTION: u8 = 14;
pub const WIRE_VALUE_PATH: u8 = 15;
/// Removed: upstream replaced `Dataclass` with `ClassInstance`
/// ([`WIRE_VALUE_CLASS_INSTANCE`]). Kept, unassigned to any `MontyObject`
/// variant, so wire compatibility numbering never shifts; `into_monty`
/// reports decoding it explicitly rather than silently misinterpreting it.
pub const WIRE_VALUE_DATACLASS: u8 = 16;
pub const WIRE_VALUE_FUNCTION: u8 = 17;
pub const WIRE_VALUE_REPR: u8 = 18;
pub const WIRE_VALUE_CYCLE: u8 = 19;
pub const WIRE_VALUE_DATE: u8 = 20;
pub const WIRE_VALUE_DATETIME: u8 = 21;
pub const WIRE_VALUE_TIMEDELTA: u8 = 22;
pub const WIRE_VALUE_TIMEZONE: u8 = 23;
pub const WIRE_VALUE_NOT_IMPLEMENTED: u8 = 24;
pub const WIRE_VALUE_TIME: u8 = 25;
pub const WIRE_VALUE_CLASS_INSTANCE: u8 = 26;
pub const WIRE_VALUE_FILE_HANDLE: u8 = 27;

pub const WIRE_CALL_RESULT_RETURN: u8 = 0;
pub const WIRE_CALL_RESULT_EXCEPTION: u8 = 1;
pub const WIRE_CALL_RESULT_PENDING: u8 = 2;

pub const WIRE_LOOKUP_RESULT_VALUE: u8 = 0;
pub const WIRE_LOOKUP_RESULT_UNDEFINED: u8 = 1;

pub const WIRE_PROGRESS_FUNCTION_CALL: u8 = 0;
pub const WIRE_PROGRESS_NAME_LOOKUP: u8 = 1;
pub const WIRE_PROGRESS_FUTURE: u8 = 2;
pub const WIRE_PROGRESS_COMPLETE: u8 = 3;

#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]
pub struct WirePair {
    pub key: WireValue,
    pub value: WireValue,
}

#[derive(Debug, Clone, PartialEq, Serialize, Deserialize, Default)]
pub struct WireValue {
    pub kind: u8,
    #[serde(default, skip_serializing_if = "is_false")]
    pub bool: bool,
    #[serde(default, skip_serializing_if = "is_zero_i64", rename = "int")]
    pub int_value: i64,
    #[serde(default, skip_serializing_if = "String::is_empty", rename = "big_int")]
    pub big_int: String,
    #[serde(default, skip_serializing_if = "is_zero_f64", rename = "float")]
    pub float_value: f64,
    #[serde(default, skip_serializing_if = "String::is_empty", rename = "string")]
    pub string_value: String,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub bytes: Vec<u8>,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub items: Vec<WireValue>,
    #[serde(
        default,
        skip_serializing_if = "String::is_empty",
        rename = "type_name"
    )]
    pub type_name: String,
    #[serde(default, skip_serializing_if = "Vec::is_empty", rename = "field_names")]
    pub field_names: Vec<String>,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub values: Vec<WireValue>,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub pairs: Vec<WirePair>,
    #[serde(default, skip_serializing_if = "String::is_empty", rename = "exc_type")]
    pub exc_type: String,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub arg: Option<String>,
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub name: String,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub attrs: Vec<WirePair>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub docstring: Option<String>,
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub placeholder: String,
    #[serde(default, skip_serializing_if = "is_zero_i32")]
    pub year: i32,
    #[serde(default, skip_serializing_if = "is_zero_u8")]
    pub month: u8,
    #[serde(default, skip_serializing_if = "is_zero_u8")]
    pub day: u8,
    #[serde(default, skip_serializing_if = "is_zero_u8")]
    pub hour: u8,
    #[serde(default, skip_serializing_if = "is_zero_u8")]
    pub minute: u8,
    #[serde(default, skip_serializing_if = "is_zero_u8")]
    pub second: u8,
    #[serde(default, skip_serializing_if = "is_zero_u32")]
    pub microsecond: u32,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub offset_seconds: Option<i32>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub timezone_name: Option<String>,
    #[serde(default, skip_serializing_if = "is_zero_i32")]
    pub days: i32,
    #[serde(default, skip_serializing_if = "is_zero_i32")]
    pub seconds: i32,
    #[serde(default, skip_serializing_if = "is_zero_i32")]
    pub microseconds: i32,
    #[serde(default, skip_serializing_if = "is_zero_u8")]
    pub fold: u8,
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub class_id: String,
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub instance_id: String,
    #[serde(default, skip_serializing_if = "is_false")]
    pub host_defined: bool,
    #[serde(default, skip_serializing_if = "is_false")]
    pub is_dataclass: bool,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub class_attrs: Vec<WirePair>,
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub mode: String,
    #[serde(default, skip_serializing_if = "is_zero_u64")]
    pub position: u64,
}

impl WireValue {
    #[must_use]
    pub fn from_monty(obj: &MontyObject) -> Self {
        Self::from_object_ref(obj.as_ref())
    }

    /// Recursively converts a borrowed value. Children share the parent's
    /// arena, so every nested id is resolved through `unstable::child` on
    /// the same root `value` rather than re-borrowing from a fresh object.
    ///
    /// `pub(crate)` so callers already holding an [`ObjectRef`] (e.g. from
    /// `CallArgs::args`/`::kwargs`) can convert directly without an
    /// intermediate owned [`MontyObject`].
    pub(crate) fn from_object_ref(value: ObjectRef<'_>) -> Self {
        match unstable::node(value) {
            MontyNode::None => Self {
                kind: WIRE_VALUE_NONE,
                ..Self::default()
            },
            MontyNode::Ellipsis => Self {
                kind: WIRE_VALUE_ELLIPSIS,
                ..Self::default()
            },
            MontyNode::NotImplemented => Self {
                kind: WIRE_VALUE_NOT_IMPLEMENTED,
                ..Self::default()
            },
            MontyNode::Bool(v) => Self {
                kind: WIRE_VALUE_BOOL,
                bool: *v,
                ..Self::default()
            },
            MontyNode::Int(v) => Self {
                kind: WIRE_VALUE_INT,
                int_value: *v,
                ..Self::default()
            },
            MontyNode::BigInt(v) => Self {
                kind: WIRE_VALUE_BIG_INT,
                big_int: v.to_string(),
                ..Self::default()
            },
            MontyNode::Float(v) => Self {
                kind: WIRE_VALUE_FLOAT,
                float_value: *v,
                ..Self::default()
            },
            MontyNode::String(v) => Self {
                kind: WIRE_VALUE_STRING,
                string_value: v.clone(),
                ..Self::default()
            },
            MontyNode::Bytes(v) => Self {
                kind: WIRE_VALUE_BYTES,
                bytes: v.clone(),
                ..Self::default()
            },
            MontyNode::List(ids) => Self {
                kind: WIRE_VALUE_LIST,
                items: ids
                    .iter()
                    .map(|id| Self::from_object_ref(unstable::child(value, *id)))
                    .collect(),
                ..Self::default()
            },
            MontyNode::Tuple(ids) => Self {
                kind: WIRE_VALUE_TUPLE,
                items: ids
                    .iter()
                    .map(|id| Self::from_object_ref(unstable::child(value, *id)))
                    .collect(),
                ..Self::default()
            },
            MontyNode::NamedTuple {
                type_name,
                field_names,
                values,
            } => Self {
                kind: WIRE_VALUE_NAMED_TUPLE,
                type_name: type_name.clone(),
                field_names: field_names.clone(),
                values: values
                    .iter()
                    .map(|id| Self::from_object_ref(unstable::child(value, *id)))
                    .collect(),
                ..Self::default()
            },
            MontyNode::Dict(pairs) => Self {
                kind: WIRE_VALUE_DICT,
                pairs: pairs
                    .iter()
                    .map(|(key, val)| WirePair {
                        key: Self::from_object_ref(unstable::child(value, *key)),
                        value: Self::from_object_ref(unstable::child(value, *val)),
                    })
                    .collect(),
                ..Self::default()
            },
            MontyNode::Set(ids) => Self {
                kind: WIRE_VALUE_SET,
                items: ids
                    .iter()
                    .map(|id| Self::from_object_ref(unstable::child(value, *id)))
                    .collect(),
                ..Self::default()
            },
            MontyNode::FrozenSet(ids) => Self {
                kind: WIRE_VALUE_FROZEN_SET,
                items: ids
                    .iter()
                    .map(|id| Self::from_object_ref(unstable::child(value, *id)))
                    .collect(),
                ..Self::default()
            },
            MontyNode::Exception { exc_type, arg } => Self {
                kind: WIRE_VALUE_EXCEPTION,
                exc_type: exc_type.to_string(),
                arg: arg.clone(),
                ..Self::default()
            },
            MontyNode::Path(v) => Self {
                kind: WIRE_VALUE_PATH,
                string_value: v.clone(),
                ..Self::default()
            },
            MontyNode::ClassInstance {
                class_type,
                instance_id,
                attrs,
            } => {
                let class_ref = unstable::child(value, *class_type);
                let MontyNode::ClassType(class_node) = unstable::node(class_ref) else {
                    unreachable!("ClassInstance.class_type always points at a ClassType node")
                };
                Self {
                    kind: WIRE_VALUE_CLASS_INSTANCE,
                    name: class_node.name.clone(),
                    class_id: class_node.id.to_string(),
                    instance_id: instance_id.to_string(),
                    host_defined: class_node.host_defined,
                    is_dataclass: class_node.is_dataclass,
                    class_attrs: class_node
                        .attrs
                        .iter()
                        .map(|(key, val)| WirePair {
                            key: Self::from_object_ref(unstable::child(class_ref, *key)),
                            value: Self::from_object_ref(unstable::child(class_ref, *val)),
                        })
                        .collect(),
                    attrs: attrs
                        .iter()
                        .map(|(key, val)| WirePair {
                            key: Self::from_object_ref(unstable::child(value, *key)),
                            value: Self::from_object_ref(unstable::child(value, *val)),
                        })
                        .collect(),
                    ..Self::default()
                }
            }
            // Output-only: a bare class object (e.g. `type(instance)` on a
            // sandbox class) with no dedicated wire kind yet. Falls back to
            // its repr, same as builtin `Type`/`BuiltinFunction` below.
            MontyNode::ClassType(_) => Self {
                kind: WIRE_VALUE_REPR,
                string_value: value.py_repr(),
                ..Self::default()
            },
            MontyNode::Time(time) => Self {
                kind: WIRE_VALUE_TIME,
                hour: time.hour,
                minute: time.minute,
                second: time.second,
                microsecond: time.microsecond,
                offset_seconds: time.offset_seconds,
                timezone_name: time.timezone_name.clone(),
                fold: time.fold,
                ..Self::default()
            },
            MontyNode::FileHandle(handle) => Self {
                kind: WIRE_VALUE_FILE_HANDLE,
                string_value: handle.path.clone(),
                mode: handle.mode.as_str().to_owned(),
                position: handle.position,
                ..Self::default()
            },
            MontyNode::Function { name, docstring } => Self {
                kind: WIRE_VALUE_FUNCTION,
                name: name.clone(),
                docstring: docstring.clone(),
                ..Self::default()
            },
            MontyNode::Repr(v) => Self {
                kind: WIRE_VALUE_REPR,
                string_value: v.clone(),
                ..Self::default()
            },
            MontyNode::Cycle(placeholder) => Self {
                kind: WIRE_VALUE_CYCLE,
                placeholder: placeholder.clone(),
                ..Self::default()
            },
            MontyNode::Date(date) => Self {
                kind: WIRE_VALUE_DATE,
                year: date.year,
                month: date.month,
                day: date.day,
                ..Self::default()
            },
            MontyNode::DateTime(datetime) => Self {
                kind: WIRE_VALUE_DATETIME,
                year: datetime.year,
                month: datetime.month,
                day: datetime.day,
                hour: datetime.hour,
                minute: datetime.minute,
                second: datetime.second,
                microsecond: datetime.microsecond,
                offset_seconds: datetime.offset_seconds,
                timezone_name: datetime.timezone_name.clone(),
                ..Self::default()
            },
            MontyNode::TimeDelta(delta) => Self {
                kind: WIRE_VALUE_TIMEDELTA,
                days: delta.days,
                seconds: delta.seconds,
                microseconds: delta.microseconds,
                ..Self::default()
            },
            MontyNode::TimeZone(tz) => Self {
                kind: WIRE_VALUE_TIMEZONE,
                days: tz.offset_seconds,
                timezone_name: tz.name.clone(),
                ..Self::default()
            },
            MontyNode::Type(v) => Self {
                kind: WIRE_VALUE_REPR,
                string_value: format!("<class '{v}'>"),
                ..Self::default()
            },
            MontyNode::BuiltinFunction(v) => Self {
                kind: WIRE_VALUE_REPR,
                string_value: format!("<built-in function {v}>"),
                ..Self::default()
            },
        }
    }

    pub fn into_monty(self) -> Result<MontyObject, String> {
        match self.kind {
            WIRE_VALUE_NONE => Ok(MontyObject::none()),
            WIRE_VALUE_ELLIPSIS => Ok(MontyObject::ellipsis()),
            WIRE_VALUE_BOOL => Ok(MontyObject::bool(self.bool)),
            WIRE_VALUE_INT => Ok(MontyObject::int(self.int_value)),
            WIRE_VALUE_BIG_INT => self
                .big_int
                .parse::<BigInt>()
                .map(MontyObject::bigint)
                .map_err(|e| format!("invalid bigint: {e}")),
            WIRE_VALUE_FLOAT => Ok(MontyObject::float(self.float_value)),
            WIRE_VALUE_STRING => Ok(MontyObject::string(self.string_value)),
            WIRE_VALUE_BYTES => Ok(MontyObject::bytes(self.bytes)),
            WIRE_VALUE_LIST => Ok(MontyObject::list(
                self.items
                    .into_iter()
                    .map(Self::into_monty)
                    .collect::<Result<Vec<_>, _>>()?,
            )),
            WIRE_VALUE_TUPLE => Ok(MontyObject::tuple(
                self.items
                    .into_iter()
                    .map(Self::into_monty)
                    .collect::<Result<Vec<_>, _>>()?,
            )),
            WIRE_VALUE_NAMED_TUPLE => Ok(MontyObject::named_tuple(
                self.type_name,
                self.field_names,
                self.values
                    .into_iter()
                    .map(Self::into_monty)
                    .collect::<Result<Vec<_>, _>>()?,
            )),
            WIRE_VALUE_DICT => Ok(MontyObject::dict(
                self.pairs
                    .into_iter()
                    .map(|pair| Ok((pair.key.into_monty()?, pair.value.into_monty()?)))
                    .collect::<Result<Vec<_>, String>>()?,
            )),
            WIRE_VALUE_SET => Ok(MontyObject::set(
                self.items
                    .into_iter()
                    .map(Self::into_monty)
                    .collect::<Result<Vec<_>, _>>()?,
            )),
            WIRE_VALUE_FROZEN_SET => Ok(MontyObject::frozenset(
                self.items
                    .into_iter()
                    .map(Self::into_monty)
                    .collect::<Result<Vec<_>, _>>()?,
            )),
            WIRE_VALUE_EXCEPTION => Ok(MontyObject::exception(
                self.exc_type
                    .parse()
                    .map_err(|_| format!("unknown exception type: {}", self.exc_type))?,
                self.arg,
            )),
            WIRE_VALUE_PATH => Ok(MontyObject::path(self.string_value)),
            WIRE_VALUE_DATACLASS => Err(
                "dataclass values are no longer supported by this version of monty; use class_instance".to_owned(),
            ),
            WIRE_VALUE_FUNCTION => Ok(MontyObject::function(self.name, self.docstring)),
            WIRE_VALUE_REPR => Err("repr values cannot be used as Monty inputs".to_owned()),
            WIRE_VALUE_CYCLE => Err("cycle placeholders cannot be used as Monty inputs".to_owned()),
            WIRE_VALUE_DATE => Ok(MontyObject::date(MontyDate {
                year: self.year,
                month: self.month,
                day: self.day,
            })),
            WIRE_VALUE_DATETIME => Ok(MontyObject::datetime(MontyDateTime {
                year: self.year,
                month: self.month,
                day: self.day,
                hour: self.hour,
                minute: self.minute,
                second: self.second,
                microsecond: self.microsecond,
                offset_seconds: self.offset_seconds,
                timezone_name: self.timezone_name,
            })),
            WIRE_VALUE_TIMEDELTA => Ok(MontyObject::timedelta(MontyTimeDelta {
                days: self.days,
                seconds: self.seconds,
                microseconds: self.microseconds,
            })),
            WIRE_VALUE_TIMEZONE => Ok(MontyObject::timezone(MontyTimeZone {
                offset_seconds: self.days,
                name: self.timezone_name,
            })),
            WIRE_VALUE_NOT_IMPLEMENTED => Ok(MontyObject::not_implemented()),
            WIRE_VALUE_TIME => Ok(MontyObject::time(MontyTime {
                hour: self.hour,
                minute: self.minute,
                second: self.second,
                microsecond: self.microsecond,
                offset_seconds: self.offset_seconds,
                timezone_name: self.timezone_name,
                fold: self.fold,
            })),
            WIRE_VALUE_CLASS_INSTANCE => {
                let class_id = MontyUuid::parse(&self.class_id)
                    .ok_or_else(|| format!("invalid class_instance class_id: {}", self.class_id))?;
                let instance_id = MontyUuid::parse(&self.instance_id)
                    .ok_or_else(|| format!("invalid class_instance instance_id: {}", self.instance_id))?;
                let class_attrs = self
                    .class_attrs
                    .into_iter()
                    .map(|pair| Ok((pair.key.into_monty()?, pair.value.into_monty()?)))
                    .collect::<Result<Vec<_>, String>>()?;
                let attrs = self
                    .attrs
                    .into_iter()
                    .map(|pair| Ok((pair.key.into_monty()?, pair.value.into_monty()?)))
                    .collect::<Result<Vec<_>, String>>()?;
                let class_type = MontyObject::class_type(
                    self.name,
                    class_id,
                    self.host_defined,
                    self.is_dataclass,
                    class_attrs,
                );
                Ok(MontyObject::class_instance(class_type, instance_id, attrs))
            }
            WIRE_VALUE_FILE_HANDLE => Err("file handles cannot be used as Monty inputs".to_owned()),
            other => Err(format!("unknown wire value kind: {other}")),
        }
    }
}

#[derive(Debug, Clone, Default, Serialize, Deserialize)]
pub struct WireCompileOptions {
    #[serde(default = "default_wire_version")]
    pub version: u32,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub script_name: Option<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub inputs: Option<Vec<String>>,
    #[serde(default, skip_serializing_if = "is_false")]
    pub type_check: bool,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub type_check_stubs: Option<String>,
}

#[derive(Debug, Clone, Default, Serialize, Deserialize)]
pub struct WireResourceLimits {
    #[serde(default = "default_wire_version")]
    pub version: u32,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub max_duration_secs: Option<f64>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub max_memory: Option<usize>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub gc_interval: Option<usize>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub max_recursion_depth: Option<usize>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub max_suspensions: Option<usize>,
}

impl From<WireResourceLimits> for ResourceLimits {
    fn from(value: WireResourceLimits) -> Self {
        let mut limits = ResourceLimits::default();
        // Upstream split one overall duration budget into a per-feed and a
        // per-turn budget. gomonty's wire format still exposes a single
        // knob, so both budgets get the same value — matching the old
        // single-duration behavior until the split is exposed separately.
        let duration = value.max_duration_secs.map(Duration::from_secs_f64);
        limits.max_feed_duration = duration;
        limits.max_turn_duration = duration;
        limits.max_memory = value.max_memory;
        limits.gc_interval = value.gc_interval;
        if let Some(max_recursion_depth) = value.max_recursion_depth {
            limits.max_recursion_depth = max_recursion_depth;
        }
        if let Some(max_suspensions) = value.max_suspensions {
            limits.max_suspensions = max_suspensions;
        }
        limits
    }
}

#[derive(Debug, Clone, Default, Serialize, Deserialize)]
pub struct WireStartOptions {
    #[serde(default = "default_wire_version")]
    pub version: u32,
    #[serde(default, skip_serializing_if = "BTreeMap::is_empty")]
    pub inputs: BTreeMap<String, WireValue>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub limits: Option<WireResourceLimits>,
}

#[derive(Debug, Clone, Default, Serialize, Deserialize)]
pub struct WireReplOptions {
    #[serde(default = "default_wire_version")]
    pub version: u32,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub script_name: Option<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub limits: Option<WireResourceLimits>,
}

#[derive(Debug, Clone, Default, Serialize, Deserialize)]
pub struct WireFeedOptions {
    #[serde(default = "default_wire_version")]
    pub version: u32,
    #[serde(default, skip_serializing_if = "BTreeMap::is_empty")]
    pub inputs: BTreeMap<String, WireValue>,
}

#[derive(Debug, Clone, Default, Serialize, Deserialize)]
pub struct WireCallResult {
    pub kind: u8,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub value: Option<WireValue>,
    #[serde(default, skip_serializing_if = "String::is_empty", rename = "exc_type")]
    pub exc_type: String,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub arg: Option<String>,
}

#[derive(Debug, Clone, Default, Serialize, Deserialize)]
pub struct WireLookupResult {
    pub kind: u8,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub value: Option<WireValue>,
}

#[derive(Debug, Clone, Default, Serialize, Deserialize)]
pub struct WireFutureResults {
    #[serde(default = "default_wire_version")]
    pub version: u32,
    #[serde(default, skip_serializing_if = "BTreeMap::is_empty")]
    pub results: BTreeMap<u32, WireCallResult>,
}

#[derive(Debug, Clone, Default, Serialize, Deserialize)]
pub struct WireProgressPayload {
    pub variant: u8,
    #[serde(default = "default_wire_version")]
    pub version: u32,
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub script_name: String,
    #[serde(default, skip_serializing_if = "is_false")]
    pub is_repl: bool,
    #[serde(default, skip_serializing_if = "is_false")]
    pub is_os_function: bool,
    #[serde(default, skip_serializing_if = "is_false")]
    pub is_method_call: bool,
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub function_name: String,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub args: Vec<WireValue>,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub kwargs: Vec<WirePair>,
    #[serde(default, skip_serializing_if = "is_zero_u32")]
    pub call_id: u32,
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub variable_name: String,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub pending_call_ids: Vec<u32>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub output: Option<WireValue>,
}

#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
pub struct WireFrame {
    pub filename: String,
    pub line: u32,
    pub column: u32,
    pub end_line: u32,
    pub end_column: u32,
    pub function_name: Option<String>,
    pub source_line: Option<String>,
}

impl From<&StackFrame> for WireFrame {
    fn from(value: &StackFrame) -> Self {
        Self {
            filename: value.filename.clone(),
            line: u32::from(value.start.line),
            column: u32::from(value.start.column),
            end_line: u32::from(value.end.line),
            end_column: u32::from(value.end.column),
            function_name: value.frame_name.clone(),
            source_line: value.preview_line.as_deref().map(str::to_owned),
        }
    }
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct WireErrorSummary {
    #[serde(default = "default_wire_version")]
    pub version: u32,
    pub kind: String,
    pub type_name: String,
    pub message: String,
    #[serde(default)]
    pub traceback: Vec<WireFrame>,
}

impl WireErrorSummary {
    #[must_use]
    pub fn from_exception(error: &MontyException) -> Self {
        let kind = if error.exc_type() == ExcType::SyntaxError {
            "syntax"
        } else {
            "runtime"
        };
        Self {
            version: WIRE_VERSION,
            kind: kind.to_owned(),
            type_name: error.exc_type().to_string(),
            message: error.message().unwrap_or_default().to_owned(),
            traceback: error.traceback().iter().map(WireFrame::from).collect(),
        }
    }
}

#[must_use]
pub const fn default_wire_version() -> u32 {
    WIRE_VERSION
}

const fn is_false(value: &bool) -> bool {
    !*value
}

const fn is_zero_u32(value: &u32) -> bool {
    *value == 0
}

const fn is_zero_u64(value: &u64) -> bool {
    *value == 0
}

const fn is_zero_i64(value: &i64) -> bool {
    *value == 0
}

fn is_zero_f64(value: &f64) -> bool {
    *value == 0.0
}

const fn is_zero_i32(value: &i32) -> bool {
    *value == 0
}

const fn is_zero_u8(value: &u8) -> bool {
    *value == 0
}

#[cfg(test)]
mod tests {
    use num_bigint::BigInt;

    use super::WireValue;
    use monty_types::{
        ExcType, MontyDate, MontyDateTime, MontyFileHandle, MontyObject, MontyTime, MontyTimeDelta,
        MontyTimeZone, MontyUuid,
    };

    #[test]
    fn wire_value_round_trips_nested_dicts() {
        let original = MontyObject::dict(vec![
            (
                MontyObject::string("numbers"),
                MontyObject::list(vec![
                    MontyObject::int(1),
                    MontyObject::bigint(BigInt::from(1_u64) << 80),
                ]),
            ),
            (
                MontyObject::string("path"),
                MontyObject::path("/tmp/example.txt"),
            ),
        ]);

        let decoded = WireValue::from_monty(&original)
            .into_monty()
            .expect("wire value should round-trip");
        assert_eq!(decoded, original);
    }

    #[test]
    fn wire_value_round_trips_class_instances() {
        let class_type = MontyObject::class_type(
            "Config",
            MontyUuid::from_u128(0x1234_5678_9abc_def0_1234_5678_9abc_def0),
            true,
            true,
            vec![(MontyObject::string("version"), MontyObject::int(1))],
        );
        let original = MontyObject::class_instance(
            class_type,
            MontyUuid::from_u128(0xfedc_ba98_7654_3210_fedc_ba98_7654_3210),
            vec![
                (MontyObject::string("enabled"), MontyObject::bool(true)),
                (
                    MontyObject::string("path"),
                    MontyObject::path("/config.json"),
                ),
            ],
        );

        let decoded = WireValue::from_monty(&original)
            .into_monty()
            .expect("class instance should round-trip");
        assert_eq!(decoded, original);
    }

    #[test]
    fn wire_value_round_trips_not_implemented() {
        let original = MontyObject::not_implemented();

        let decoded = WireValue::from_monty(&original)
            .into_monty()
            .expect("NotImplemented should round-trip");
        assert_eq!(decoded, original);
    }

    #[test]
    fn wire_value_round_trips_time() {
        let original = MontyObject::time(MontyTime {
            hour: 14,
            minute: 30,
            second: 45,
            microsecond: 123_456,
            offset_seconds: Some(3600),
            timezone_name: Some("UTC+01:00".to_owned()),
            fold: 1,
        });

        let decoded = WireValue::from_monty(&original)
            .into_monty()
            .expect("time should round-trip");
        assert_eq!(decoded, original);
    }

    #[test]
    fn wire_value_round_trips_naive_time() {
        let original = MontyObject::time(MontyTime {
            hour: 0,
            minute: 0,
            second: 0,
            microsecond: 0,
            offset_seconds: None,
            timezone_name: None,
            fold: 0,
        });

        let decoded = WireValue::from_monty(&original)
            .into_monty()
            .expect("naive time should round-trip");
        assert_eq!(decoded, original);
    }

    #[test]
    fn wire_value_rejects_file_handle_inputs() {
        let original = MontyObject::file_handle(MontyFileHandle {
            path: "/tmp/example.txt".to_owned(),
            mode: "r".parse().expect("valid mode"),
            position: 0,
        });

        let error = WireValue::from_monty(&original)
            .into_monty()
            .expect_err("file handles must be rejected as inputs");
        assert_eq!(error, "file handles cannot be used as Monty inputs");
    }

    #[test]
    fn wire_value_rejects_dataclass_inputs() {
        let error = WireValue {
            kind: super::WIRE_VALUE_DATACLASS,
            ..WireValue::default()
        }
        .into_monty()
        .expect_err("dataclass wire values must be rejected");
        assert_eq!(
            error,
            "dataclass values are no longer supported by this version of monty; use class_instance"
        );
    }

    #[test]
    fn wire_value_rejects_repr_inputs() {
        let error = WireValue {
            kind: super::WIRE_VALUE_REPR,
            string_value: "<object repr>".to_owned(),
            ..WireValue::default()
        }
        .into_monty()
        .expect_err("repr values must be rejected");
        assert_eq!(error, "repr values cannot be used as Monty inputs");
    }

    #[test]
    fn wire_value_round_trips_exceptions() {
        let original = MontyObject::exception(ExcType::RuntimeError, Some("boom".to_owned()));

        let decoded = WireValue::from_monty(&original)
            .into_monty()
            .expect("exceptions should round-trip");
        assert_eq!(decoded, original);
    }

    #[test]
    fn wire_value_round_trips_date() {
        let original = MontyObject::date(MontyDate {
            year: 2026,
            month: 3,
            day: 29,
        });

        let decoded = WireValue::from_monty(&original)
            .into_monty()
            .expect("date should round-trip");
        assert_eq!(decoded, original);
    }

    #[test]
    fn wire_value_round_trips_datetime() {
        let original = MontyObject::datetime(MontyDateTime {
            year: 2026,
            month: 3,
            day: 29,
            hour: 14,
            minute: 30,
            second: 45,
            microsecond: 123_456,
            offset_seconds: Some(3600),
            timezone_name: Some("UTC+01:00".to_owned()),
        });

        let decoded = WireValue::from_monty(&original)
            .into_monty()
            .expect("datetime should round-trip");
        assert_eq!(decoded, original);
    }

    #[test]
    fn wire_value_round_trips_naive_datetime() {
        let original = MontyObject::datetime(MontyDateTime {
            year: 2026,
            month: 1,
            day: 1,
            hour: 0,
            minute: 0,
            second: 0,
            microsecond: 0,
            offset_seconds: None,
            timezone_name: None,
        });

        let decoded = WireValue::from_monty(&original)
            .into_monty()
            .expect("naive datetime should round-trip");
        assert_eq!(decoded, original);
    }

    #[test]
    fn wire_value_round_trips_timedelta() {
        let original = MontyObject::timedelta(MontyTimeDelta {
            days: -1,
            seconds: 3600,
            microseconds: 500_000,
        });

        let decoded = WireValue::from_monty(&original)
            .into_monty()
            .expect("timedelta should round-trip");
        assert_eq!(decoded, original);
    }

    #[test]
    fn wire_value_round_trips_timezone() {
        let original = MontyObject::timezone(MontyTimeZone {
            offset_seconds: -18000,
            name: Some("EST".to_owned()),
        });

        let decoded = WireValue::from_monty(&original)
            .into_monty()
            .expect("timezone should round-trip");
        assert_eq!(decoded, original);
    }

    #[test]
    fn wire_value_round_trips_utc_timezone() {
        let original = MontyObject::timezone(MontyTimeZone {
            offset_seconds: 0,
            name: None,
        });

        let decoded = WireValue::from_monty(&original)
            .into_monty()
            .expect("utc timezone should round-trip");
        assert_eq!(decoded, original);
    }
}
