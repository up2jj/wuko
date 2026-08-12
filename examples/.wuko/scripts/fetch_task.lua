local token = wuko.env.get("WUKO_DEMO_TOKEN")
if token == nil then
  error("WUKO_DEMO_TOKEN is required")
end

wuko.output("projects", {
  {name = "Backend", id = "backend"},
  {name = "Frontend", id = "frontend"},
})

wuko.output("task", {
  id = "TASK-123",
  title = wuko.args.name,
  branch = "task/TASK-123",
})
