# Classes, iterator protocol, frozen dataclasses, and dynamic class creation.
from dataclasses import dataclass, FrozenInstanceError


class Countdown:
    def __init__(self, value):
        self.value = value

    def __repr__(self):
        return f"Countdown({self.value})"

    def __eq__(self, other):
        return self.value == other.value

    def __iter__(self):
        return self

    def __next__(self):
        if self.value == 0:
            raise StopIteration
        self.value -= 1
        return self.value + 1


@dataclass(frozen=True, eq=True)
class Point:
    x: int
    y: int


counter = Countdown(3)
representation = repr(counter)
equal = counter == Countdown(3)
values = list(counter)
point = Point(3, 4)
frozen = False
try:
    point.x = 9
except FrozenInstanceError:
    frozen = True
Dynamic = type("Dynamic", (), {"answer": 42})
print(representation, values, point, Dynamic().answer)

assert representation == "Countdown(3)"
assert equal
assert values == [3, 2, 1]
assert point == Point(3, 4)
assert point != Point(4, 3)
assert frozen and point.x == 3
assert Dynamic().answer == 42
