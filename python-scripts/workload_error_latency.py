# Workload: highest average latency among ERROR rows.
rows = [("api", "ERROR", 420), ("worker", "ERROR", 900), ("api", "INFO", 80), ("auth", "ERROR", 250), ("worker", "ERROR", 300), ("api", "ERROR", 380), ("auth", "INFO", 40)]
totals = {}
for service, level, latency in rows:
    if level == "ERROR":
        total, count = totals.get(service, (0, 0))
        totals[service] = (total + latency, count + 1)
averages = {s: t // c for s, (t, c) in totals.items()}
best = max(averages, key=lambda s: averages[s])
answer = best + ":" + str(averages[best])
assert answer == "worker:600"
answer
