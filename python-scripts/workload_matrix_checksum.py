# Workload: weighted checksum of a 4x4 matrix product.
A = [[2,1,3,0],[0,4,1,2],[5,2,0,1],[1,3,2,4]]
B = [[1,2,0,3],[4,1,2,0],[2,3,1,1],[0,2,4,2]]
C = [[sum(A[i][k] * B[k][j] for k in range(4)) for j in range(4)] for i in range(4)]
answer = sum((i + 1) * (j + 1) * C[i][j] for i in range(4) for j in range(4))
assert answer == 1428
answer
