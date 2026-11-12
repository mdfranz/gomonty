# Workload: count 4-element combinations that sum to 44.
from itertools import combinations
values = [3, 5, 7, 9, 11, 13, 15, 17, 19]
answer = sum(1 for combo in combinations(values, 4) if sum(combo) == 44)
assert answer == 12
answer
