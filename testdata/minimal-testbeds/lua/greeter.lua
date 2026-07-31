local helpers = require("helpers")

local function format(s)
  return helpers.upper(s)
end

function greet(name)
  return format(name)
end

return { greet = greet }
