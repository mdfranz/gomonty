# Calls the host_time() external function shmonty registers, so running
# this with /execute host_callback.py while /debug is on shows a callback
# span (StartCallback/End) in the telemetry pane, not just an execution span.
timestamp = host_time()
print("host time:", timestamp)
timestamp

assert isinstance(timestamp, str)
assert len(timestamp) > 0

# Positional/keyword arguments, structured results, and host exceptions.
assert host_calc("add", 2, 3) == 5
assert host_calc(op="mul", a=3, b=4) == 12
assert host_echo(value=[1, "two"]) == {"value": [1, "two"], "source": "shmonty"}
try:
    host_fail(msg="example failure")
except ValueError as error:
    assert str(error) == "example failure"
else:
    assert False, "host_fail must raise ValueError"
