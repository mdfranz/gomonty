# Variables, arithmetic, strings, and control flow.
name = "monty"
greeting = "Hello, " + name + "!"
print(greeting)

count = 0
for i in range(5):
    if i % 2 == 0:
        count += 1
    else:
        count -= 1

count
