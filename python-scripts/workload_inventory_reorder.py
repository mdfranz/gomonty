# Workload: total cost to bring stock up to its reorder target.
stock = [("A17", 4, 10), ("B04", 18, 12), ("C22", 0, 8), ("D09", 6, 6)]
costs = {"A17": 3.25, "B04": 7.5, "C22": 2.4, "D09": 11.0}
total = 0
for sku, on_hand, target in stock:
    if on_hand < target:
        total += (target - on_hand) * costs[sku]
answer = round(total, 2)
assert answer == 38.7
answer
