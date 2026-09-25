# Calls the host_time() external function shmonty registers, so running
# this with /execute host_callback.py while /debug is on shows a callback
# span (StartCallback/End) in the telemetry pane, not just an execution span.
timestamp = host_time()
print("host time:", timestamp)
timestamp
