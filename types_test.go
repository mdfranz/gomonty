package monty

import (
	"encoding/json"
	"math/big"
	"regexp"
	"testing"
)

func TestValueJSONRoundTrip(t *testing.T) {
	original := DictValue(Dict{
		{Key: String("answer"), Value: Int(42)},
		{Key: String("items"), Value: List(String("a"), Bool(true))},
		{Key: String("big"), Value: BigInt(big.NewInt(0).Exp(big.NewInt(2), big.NewInt(70), nil))},
	})

	data, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var decoded Value
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	data2, err := json.Marshal(decoded)
	if err != nil {
		t.Fatalf("re-marshal: %v", err)
	}

	if string(data) != string(data2) {
		t.Fatalf("round-trip mismatch:\n%s\n%s", data, data2)
	}
}

func TestDateTimeJSONRoundTrip(t *testing.T) {
	offset := int32(3600)
	tzName := "UTC+01:00"
	values := []Value{
		DateValue(Date{Year: 2026, Month: 3, Day: 29}),
		DateTimeValue(DateTime{
			Year: 2026, Month: 3, Day: 29,
			Hour: 14, Minute: 30, Second: 45, Microsecond: 123456,
			OffsetSeconds: &offset, TimezoneName: &tzName,
		}),
		DateTimeValue(DateTime{Year: 2026, Month: 1, Day: 1}),
		TimeDeltaValue(TimeDelta{Days: -1, Seconds: 3600, Microseconds: 500000}),
		TimeZoneValue(TimeZone{OffsetSeconds: -18000, Name: &tzName}),
		TimeZoneValue(TimeZone{OffsetSeconds: 0}),
	}

	for _, original := range values {
		data, err := json.Marshal(original)
		if err != nil {
			t.Fatalf("marshal %s: %v", original.Kind(), err)
		}

		var decoded Value
		if err := json.Unmarshal(data, &decoded); err != nil {
			t.Fatalf("unmarshal %s: %v", original.Kind(), err)
		}

		data2, err := json.Marshal(decoded)
		if err != nil {
			t.Fatalf("re-marshal %s: %v", original.Kind(), err)
		}

		if string(data) != string(data2) {
			t.Fatalf("round-trip mismatch for %s:\n%s\n%s", original.Kind(), data, data2)
		}
	}
}

func TestDateTimeAccessors(t *testing.T) {
	date := Date{Year: 2026, Month: 3, Day: 29}
	v := DateValue(date)
	if got, ok := v.Date(); !ok || got != date {
		t.Fatalf("Date() = %v, %v", got, ok)
	}

	dt := DateTime{Year: 2026, Month: 3, Day: 29, Hour: 14}
	v = DateTimeValue(dt)
	if got, ok := v.DateTime(); !ok || got != dt {
		t.Fatalf("DateTime() = %v, %v", got, ok)
	}

	td := TimeDelta{Days: 1, Seconds: 60}
	v = TimeDeltaValue(td)
	if got, ok := v.TimeDelta(); !ok || got != td {
		t.Fatalf("TimeDelta() = %v, %v", got, ok)
	}

	tz := TimeZone{OffsetSeconds: 3600}
	v = TimeZoneValue(tz)
	if got, ok := v.TimeZone(); !ok || got != tz {
		t.Fatalf("TimeZone() = %v, %v", got, ok)
	}
}

func TestDateTimeValueOf(t *testing.T) {
	cases := []any{
		Date{Year: 2026, Month: 3, Day: 29},
		DateTime{Year: 2026, Month: 3, Day: 29},
		TimeDelta{Days: 1},
		TimeZone{OffsetSeconds: 0},
	}
	for _, c := range cases {
		if _, err := ValueOf(c); err != nil {
			t.Fatalf("ValueOf(%T): %v", c, err)
		}
	}
}

func TestDateTimeString(t *testing.T) {
	v := DateValue(Date{Year: 2026, Month: 3, Day: 29})
	if s := v.String(); s != "2026-03-29" {
		t.Fatalf("Date.String() = %q", s)
	}

	offset := int32(3600)
	v = DateTimeValue(DateTime{
		Year: 2026, Month: 3, Day: 29, Hour: 14, Minute: 30, Second: 45,
		Microsecond: 123456, OffsetSeconds: &offset,
	})
	if s := v.String(); s != "2026-03-29 14:30:45.123456+01:00" {
		t.Fatalf("DateTime.String() = %q", s)
	}

	v = TimeDeltaValue(TimeDelta{Days: -1, Seconds: 3600, Microseconds: 500000})
	if s := v.String(); s != "timedelta(days=-1, seconds=3600, microseconds=500000)" {
		t.Fatalf("TimeDelta.String() = %q", s)
	}

	v = TimeZoneValue(TimeZone{OffsetSeconds: -18000})
	if s := v.String(); s != "UTC-05:00" {
		t.Fatalf("TimeZone.String() = %q", s)
	}

	name := "EST"
	v = TimeZoneValue(TimeZone{OffsetSeconds: -18000, Name: &name})
	if s := v.String(); s != "EST" {
		t.Fatalf("TimeZone.String() with name = %q", s)
	}
}

func TestValueOfStatResult(t *testing.T) {
	original := StatResult{
		Mode:  0o100644,
		Ino:   1,
		Dev:   2,
		Nlink: 1,
		UID:   10,
		GID:   20,
		Size:  123,
		Atime: 1.5,
		Mtime: 2.5,
		Ctime: 3.5,
	}

	value, err := ValueOf(original)
	if err != nil {
		t.Fatalf("ValueOf: %v", err)
	}

	stat, ok := value.StatResult()
	if !ok {
		t.Fatal("expected stat result")
	}

	if stat != original {
		t.Fatalf("unexpected stat result: %#v != %#v", stat, original)
	}
}

func TestUpstreamRefreshValueJSONRoundTrip(t *testing.T) {
	offset := int32(3600)
	timezoneName := "Europe/Paris"
	class := ClassInstance{
		ClassType: ClassType{
			Name:        "Config",
			ID:          "12345678-9abc-4def-8123-456789abcdef",
			HostDefined: true,
			IsDataclass: true,
			Attrs:       Dict{{Key: String("version"), Value: Int(1)}},
		},
		InstanceID: "fedcba98-7654-4321-8fed-cba987654321",
		Attrs:      Dict{{Key: String("enabled"), Value: Bool(true)}},
	}
	values := []Value{
		NotImplemented(),
		TimeValue(Time{
			Hour: 14, Minute: 30, Second: 45, Microsecond: 123456,
			OffsetSeconds: &offset, TimezoneName: &timezoneName, Fold: 1,
		}),
		ClassInstanceValue(class),
		FileHandleValue(FileHandle{Path: "/tmp/example.txt", Mode: "r", Position: 42}),
	}

	for _, original := range values {
		data, err := json.Marshal(original)
		if err != nil {
			t.Fatalf("marshal %s: %v", original.Kind(), err)
		}
		var decoded Value
		if err := json.Unmarshal(data, &decoded); err != nil {
			t.Fatalf("unmarshal %s: %v", original.Kind(), err)
		}
		data2, err := json.Marshal(decoded)
		if err != nil {
			t.Fatalf("re-marshal %s: %v", original.Kind(), err)
		}
		if string(data) != string(data2) {
			t.Fatalf("round-trip mismatch for %s:\n%s\n%s", original.Kind(), data, data2)
		}
	}
}

func TestClassInstanceIDsAndAccessors(t *testing.T) {
	id := NewClassID()
	if !regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`).MatchString(id) {
		t.Fatalf("NewClassID() = %q, want UUIDv4", id)
	}

	value := ClassInstanceValue(ClassInstance{ClassType: ClassType{Name: "Generated"}})
	instance, ok := value.ClassInstance()
	if !ok || instance.ClassType.ID == "" || instance.InstanceID == "" {
		t.Fatalf("ClassInstance() = %#v, %v; want generated IDs", instance, ok)
	}

	time := Time{Hour: 1, Minute: 2, Second: 3}
	if got, ok := TimeValue(time).Time(); !ok || got != time {
		t.Fatalf("Time() = %#v, %v", got, ok)
	}
	handle := FileHandle{Path: "/tmp/example.txt", Mode: "rb", Position: 9}
	if got, ok := FileHandleValue(handle).FileHandle(); !ok || got != handle {
		t.Fatalf("FileHandle() = %#v, %v", got, ok)
	}

	if got := NotImplemented().String(); got != "NotImplemented" {
		t.Fatalf("NotImplemented().String() = %q", got)
	}
	if got := TimeValue(Time{Hour: 1, Minute: 2, Second: 3}).String(); got != "01:02:03" {
		t.Fatalf("TimeValue(...).String() = %q", got)
	}
	if got := FileHandleValue(handle).String(); got != `<file name="/tmp/example.txt" mode="rb">` {
		t.Fatalf("FileHandleValue(...).String() = %q", got)
	}
}
