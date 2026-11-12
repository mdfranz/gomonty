# Fixed dates and times: parsing, formatting, UTC, and duration arithmetic.
from datetime import date, time, datetime, timedelta, timezone

day = date.fromisoformat("2024-02-28")
clock = time.fromisoformat("12:30:45")
stamp = datetime.fromisoformat("2024-02-28T12:30:45+00:00")
next_day = day + timedelta(days=1)
later = stamp + timedelta(days=2, seconds=15)
print(next_day.isoformat(), clock.isoformat(), later.isoformat())

assert next_day == date(2024, 2, 29)
assert clock == time(12, 30, 45)
assert stamp.tzinfo == timezone.utc
assert stamp.strftime("%Y-%m-%d %H:%M:%S") == "2024-02-28 12:30:45"
assert later == datetime(2024, 3, 1, 12, 31, tzinfo=timezone.utc)
assert (later - stamp).total_seconds() == 172815
