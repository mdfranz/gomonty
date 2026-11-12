# Workload: decode base64 JSON and sum paid order amounts.
import base64
import json
blob = "eyJvcmRlcnMiOlt7InN0YXR1cyI6InBhaWQiLCJhbW91bnQiOjE5Ljk1fSx7InN0YXR1cyI6InZvaWQiLCJhbW91bnQiOjEwMH0seyJzdGF0dXMiOiJwYWlkIiwiYW1vdW50Ijo3LjV9LHsic3RhdHVzIjoicGFpZCIsImFtb3VudCI6MTIuNTV9XX0="
orders = json.loads(base64.b64decode(blob).decode("utf-8"))["orders"]
answer = round(sum(o["amount"] for o in orders if o["status"] == "paid"), 2)
assert answer == 40
answer
