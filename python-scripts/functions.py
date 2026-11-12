# Function definitions and recursion.
def factorial(n):
    if n <= 1:
        return 1
    return n * factorial(n - 1)


def add(x, y):
    return x + y


print("5! =", factorial(5))
print("add(2, 3) =", add(2, 3))

factorial(5) + add(2, 3)

assert factorial(0) == 1
assert factorial(5) == 120
assert add(2, 3) == 5
