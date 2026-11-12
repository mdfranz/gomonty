# Context manager cleanup, exception suppression, and propagation.
events = []


class Scope:
    def __init__(self, suppress=False):
        self.suppress = suppress

    def __enter__(self):
        events.append("enter")
        return "resource"

    def __exit__(self, t, value, traceback):
        events.append("value-error" if t is ValueError else "exit")
        return self.suppress


with Scope() as resource:
    events.append(resource)
with Scope(suppress=True):
    raise ValueError("handled")
propagated = False
try:
    with Scope():
        raise ValueError("propagated")
except ValueError:
    propagated = True
print(events)

assert events == ["enter", "resource", "exit", "enter", "value-error", "enter", "value-error"]
assert propagated
