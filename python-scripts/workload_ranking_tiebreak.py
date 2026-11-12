# Workload: rank by score desc, then minutes asc, then name asc.
records = [("ana", 88, 12), ("bo", 91, 15), ("cy", 88, 9), ("dee", 91, 11), ("eli", 84, 20)]
ranked = sorted(records, key=lambda r: (-r[1], r[2], r[0]))
answer = ", ".join(r[0] for r in ranked)
assert answer == "dee, bo, cy, ana, eli"
answer
