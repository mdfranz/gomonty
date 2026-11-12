# Finite iterator combinators, bounded cycles, reduce, and partial application.
from itertools import combinations, permutations, islice, chain, cycle
from functools import reduce, partial

pairs = list(combinations([1, 2, 3], 2))
orders = list(permutations([1, 2]))
joined = list(chain([1, 2], [3]))
cycled = list(islice(cycle(["a", "b"]), 5))
product = reduce(lambda a, b: a * b, [1, 2, 3, 4])


def add(a, b):
    return a + b


add_ten = partial(add, 10)
print(pairs, orders, joined, cycled, product, add_ten(5))

assert pairs == [(1, 2), (1, 3), (2, 3)]
assert orders == [(1, 2), (2, 1)]
assert joined == [1, 2, 3]
assert cycled == ["a", "b", "a", "b", "a"]
assert product == 24
assert add_ten(5) == 15
