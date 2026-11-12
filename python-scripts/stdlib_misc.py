# Encoding, copying, seeded randomness, Unicode, typing, and fixed time data.
import base64
import binascii
import copy
import random
import sys
import time
import typing
import unicodedata

encoded = base64.b64encode(b"monty")
crc = binascii.crc32(b"monty")
original = {"items": [1, 2]}
cloned = copy.deepcopy(original)
cloned["items"].append(3)
random.seed(42)
first = [random.randint(0, 100) for _ in range(4)]
random.seed(42)
second = [random.randint(0, 100) for _ in range(4)]
normalized = unicodedata.normalize("NFC", "e\u0301")
values: typing.List[int] = [1, 2]
clock = time.gmtime(0)
print(encoded, crc, cloned, first, normalized)

assert encoded == b"bW9udHk="
assert base64.b64decode(encoded) == b"monty"
assert crc == 3825610996
assert original == {"items": [1, 2]}
assert cloned == {"items": [1, 2, 3]}
assert first == second and len(first) == 4
assert all(0 <= n <= 100 for n in first)
assert normalized == "é"
assert unicodedata.category("A") == "Lu"
assert isinstance(sys.version, str) and len(sys.version) > 0
assert values == [1, 2]
assert clock.tm_year == 1970 and clock.tm_mon == 1 and clock.tm_mday == 1
