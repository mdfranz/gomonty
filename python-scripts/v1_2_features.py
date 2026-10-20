# Features that arrived with upstream Monty v1.1.0 / v1.2.0.
import bisect
import hashlib

z = complex(1, -2) * 2j
print("complex:", z, z.real, z.imag, z.conjugate())
print("negative zero:", complex(-0.0, 0))

match (1, 2):
    case (a, b):
        print("match:", a + b)

xs = [1, 2, 3, 4]
xs[1:3] = [9]
print("slice assignment:", xs)

print("bisect:", bisect.bisect_left([1, 3, 5, 7], 5))
print("sha256:", hashlib.sha256(b"monty").hexdigest()[:16])

try:
    import not_a_real_module
except ModuleNotFoundError as e:
    print("ModuleNotFoundError:", e)

z
