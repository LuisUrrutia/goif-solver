local first = redis.call("ZRANGE", KEYS[1], 0, 0, "WITHSCORES")
if #first == 0 then
  return tonumber(ARGV[1])
end
local t = redis.call("TIME")
local now = t[1] * 1000 + math.floor(t[2] / 1000)
return math.max(0, math.min(tonumber(ARGV[1]), tonumber(first[2]) - now))
