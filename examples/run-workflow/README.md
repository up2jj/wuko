# Blocking child workflows

Run the parent from this directory so local discovery finds both workflows:

```sh
wuko run release
```

Each foreach iteration blocks on its own `run_workflow` call, so `max_concurrency: 2` bounds the
number of active child workflows. The parent collects each child's declared `artifact` output in
target order.
