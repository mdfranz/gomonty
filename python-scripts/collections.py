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
