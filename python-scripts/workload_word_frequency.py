# Workload: top three words, count descending then word ascending.
import re
text = "Blue birds fly over blue water; green birds rest by the water. Blue skies, green fields, and birds everywhere."
ignore = {"the", "and", "by", "over"}
counts = {}
for word in re.findall(r"[a-z]+", text.lower()):
    if word not in ignore:
        counts[word] = counts.get(word, 0) + 1
top = sorted(counts.items(), key=lambda kv: (-kv[1], kv[0]))[:3]
answer = ", ".join(w + ":" + str(c) for w, c in top)
assert answer == "birds:3, blue:3, green:2"
answer
