# Workload: number of coin combinations making 37 (dynamic programming).
coins = [1, 5, 10, 25]
ways = [1] + [0] * 37
for coin in coins:
    for amount in range(coin, 38):
        ways[amount] += ways[amount - coin]
answer = ways[37]
assert answer == 24
answer
