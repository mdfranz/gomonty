# Workload: largest sum of any 4 consecutive measurements.
values = [14, 7, 9, 20, 18, 11, 6, 17, 25, 13, 8, 19]
sums = [sum(values[i:i + 4]) for i in range(len(values) - 3)]
answer = max(sums)
assert answer == 65
answer
