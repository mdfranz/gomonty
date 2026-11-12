# Structured collections, numerical helpers, and integers beyond 64 bits.
from collections import namedtuple, defaultdict, Counter
import math

Point = namedtuple("Point", ["x", "y"])
point = Point(3, 4)
groups = defaultdict(list)
for word in ["ant", "bee", "ape"]:
    groups[word[0]].append(word)
counts = Counter(["red", "blue", "red"])
big = 2 ** 100
print(point, dict(groups), dict(counts), big)

assert point.x == 3 and point.y == 4
assert groups["a"] == ["ant", "ape"]
assert groups["b"] == ["bee"]
assert counts["red"] == 2 and counts["missing"] == 0
assert math.isclose(math.hypot(3, 4), 5.0)
assert math.comb(5, 2) == 10
assert math.gcd(54, 24) == 6
assert big == 1267650600228229401496703205376
assert (big + 7) - big == 7
assert big // (2 ** 90) == 1024
