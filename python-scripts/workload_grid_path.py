# Workload: shortest path through a grid with breadth-first search.
grid = [[0,0,1,0,0,0],[1,0,1,0,1,0],[0,0,0,0,1,0],[0,1,1,0,0,0],[0,0,0,1,1,0],[1,1,0,0,0,0]]
rows, cols = len(grid), len(grid[0])
seen = {(0, 0)}
frontier = [(0, 0)]
steps = 0
answer = -1
while frontier:
    if (rows - 1, cols - 1) in frontier:
        answer = steps
        break
    next_frontier = []
    for r, c in frontier:
        for dr, dc in ((1, 0), (-1, 0), (0, 1), (0, -1)):
            nr, nc = r + dr, c + dc
            if 0 <= nr < rows and 0 <= nc < cols and grid[nr][nc] == 0 and (nr, nc) not in seen:
                seen.add((nr, nc))
                next_frontier.append((nr, nc))
    frontier = next_frontier
    steps += 1
assert answer == 10
answer
