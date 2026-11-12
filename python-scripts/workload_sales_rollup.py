# Workload: revenue per region, return the top region.
rows = [("east", 3, 12.5), ("west", 4, 15), ("north", 10, 6.25), ("east", 2, 8), ("west", 5, 9.5), ("west", 1, 21)]
revenue = {}
for region, units, price in rows:
    revenue[region] = revenue.get(region, 0) + units * price
best = max(revenue, key=lambda r: revenue[r])
answer = best + ":" + str(revenue[best])
assert answer == "west:128.5"
answer
