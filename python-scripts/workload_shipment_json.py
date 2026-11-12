# Workload: parse JSON and total weight per warehouse.
import json
text = '{"shipments":[{"warehouse":"east","boxes":[{"weight":12.5,"count":4},{"weight":8,"count":3}]},{"warehouse":"west","boxes":[{"weight":10,"count":5},{"weight":6.5,"count":8}]},{"warehouse":"east","boxes":[{"weight":4.5,"count":10}]}]}'
totals = {}
for shipment in json.loads(text)["shipments"]:
    for box in shipment["boxes"]:
        name = shipment["warehouse"]
        totals[name] = totals.get(name, 0) + box["weight"] * box["count"]
parts = []
for name in sorted(totals):
    value = totals[name]
    if value == int(value):
        value = int(value)
    parts.append(name + ":" + str(value))
answer = ", ".join(parts)
assert answer == "east:119, west:102"
answer
