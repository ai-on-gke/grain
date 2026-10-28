import sys

print("granule example app, args:", sys.argv[1:], flush=True)
with open("/run/app-started", "w") as f:
    f.write(" ".join(sys.argv[1:]) + "\n")
while True:
    import time
    time.sleep(3600)
