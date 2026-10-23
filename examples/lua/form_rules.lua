local M = {}

function M.ticket(value)
  return string.match(value, "^OPS%-%d+$") ~= nil
end

return M
