# Exceptions: raising, catching, and assert.
def safe_divide(a, b):
    try:
        return a / b
    except ZeroDivisionError as e:
        print("caught:", e)
        return None

result = safe_divide(10, 2)
assert result == 5, "10 / 2 should be 5"

safe_divide(10, 0)
