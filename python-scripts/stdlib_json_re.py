# Nested JSON round trips and regular expression matching and substitution.
import json
import re

payload = {"jobs": [{"id": "job-42", "ready": True}], "missing": None}
encoded = json.dumps(payload)
decoded = json.loads(encoded)
matched = re.match(r"job-(\d+)$", decoded["jobs"][0]["id"])
searched = re.search(r"job-\d+", "queued job-42 today")
redacted = re.sub(r"\d+", "N", "job-42 batch-7")
print(decoded, matched.group(1), searched.group(0), redacted)

assert decoded == payload
assert matched.group(1) == "42"
assert searched.group(0) == "job-42"
assert re.match(r"job-\d+$", "invalid") is None
assert redacted == "job-N batch-N"
