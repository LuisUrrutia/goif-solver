if redis.call("GET", KEYS[1]) ~= ARGV[1] then
  return 0
end
local pending = redis.call("GET", KEYS[2])
if not pending then
  return 1
end
if pending ~= ARGV[2] then
  return -1
end
redis.call("DEL", KEYS[2])
return 1
