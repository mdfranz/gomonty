# Workload: total units covered by merged half-open intervals.
intervals = sorted([(5, 18), (12, 25), (30, 41), (39, 50), (55, 60), (58, 70)])
merged = []
for start, end in intervals:
    if merged and start <= merged[-1][1]:
        merged[-1][1] = max(merged[-1][1], end)
    else:
        merged.append([start, end])
answer = sum(end - start for start, end in merged)
assert answer == 55
answer
