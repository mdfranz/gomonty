# Async functions and ordered gather results; no sleep or external services.
import asyncio


async def square(value):
    return value * value


async def calculate():
    return await asyncio.gather(square(3), square(1), square(2))


results = asyncio.run(calculate())
print(results)

assert results == [9, 1, 4]
