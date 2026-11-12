# Monty-specific eager iterators, assertion messages, and language limits.
zipped = zip([1, 2], [3, 4])
mapped = map(lambda x: x * 2, [1, 2])
filtered = filter(lambda x: x > 1, [1, 2, 3])
message = ""
try:
    assert 5 == 6
except AssertionError as error:
    message = str(error)

tracebacks = []


class Scope:
    def __enter__(self):
        return self

    def __exit__(self, t, value, traceback):
        tracebacks.append(traceback)
        return True


with Scope():
    raise ValueError("suppressed")
missing_contextlib = False
try:
    import contextlib
except ModuleNotFoundError:
    missing_contextlib = True
# Parse unsupported syntax through exec so this example itself can run.
rejected_async_with = False
try:
    exec("async def use_scope():\n    async with Scope():\n        pass\n")
except (SyntaxError, NotImplementedError):
    rejected_async_with = True
print(zipped, mapped, filtered, message)

assert isinstance(zipped, list) and zipped == [(1, 3), (2, 4)]
assert isinstance(mapped, list) and mapped == [2, 4]
assert isinstance(filtered, list) and filtered == [2, 3]
assert message == "assert 5 == 6"
assert tracebacks == [None]
assert missing_contextlib
assert rejected_async_with
