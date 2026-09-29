local due = redis.call("ZSCORE", KEYS[1], ARGV[1])
if not due then
  return -1
end
local t = redis.call("TIME")
local now = t[1] * 1000 + math.floor(t[2] / 1000)
if tonumber(due) > now then
  return -1
end
if redis.call("EXISTS", KEYS[2]) == 1 then
  return 0
end
local token = redis.call("INCR", KEYS[3])
redis.call("SET", KEYS[2], token, "PX", ARGV[2])
return token
