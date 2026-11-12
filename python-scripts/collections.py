# Lists, dicts, tuples, sets, and comprehensions.
numbers = [1, 2, 3, 4, 5]
squares = [n * n for n in numbers]
evens = {n for n in numbers if n % 2 == 0}
lookup = {n: n * n for n in numbers}
point = (3, 4)

print("squares:", squares)
print("evens:", evens)
print("lookup:", lookup)
print("point:", point)

sum(squares)

assert squares == [1, 4, 9, 16, 25]
assert evens == {2, 4}
assert lookup == {1: 1, 2: 4, 3: 9, 4: 16, 5: 25}
assert point == (3, 4)
assert sum(squares) == 55
