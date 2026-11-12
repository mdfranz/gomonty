# Workload: sum of distinct four-digit uppercase JOB ids.
import re
text = "ok JOB-1042; retry JOB-0091; ignore job-77; done JOB-1200; duplicate JOB-0091; malformed JOB-12X4"
ids = {int(m) for m in re.findall(r"JOB-(\d{4})\b", text)}
answer = sum(ids)
assert answer == 2333
answer
