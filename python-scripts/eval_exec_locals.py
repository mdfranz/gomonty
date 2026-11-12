# Dynamic code with explicit namespaces and snapshots of function locals.
global_scope = {"base": 10}
local_scope = {"offset": 3}
value = eval("base + offset", global_scope, local_scope)
exec("result = base * offset", global_scope, local_scope)


def inspect_locals():
    count = 1
    before = locals()
    count = 2
    after = locals()
    return before["count"], after["count"], count


snapshots = inspect_locals()
print(value, local_scope["result"], snapshots)

assert value == 13
assert local_scope["result"] == 30
assert "result" not in global_scope
assert snapshots == (1, 2, 2)
