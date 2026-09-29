if redis.call("GET", KEYS[1]) ~= ARGV[1] then
  return 0
end
if redis.call("HGET", KEYS[2], "stage") ~= ARGV[2] then
  return -1
end
local t = redis.call("TIME")
local now = t[1] * 1000 + math.floor(t[2] / 1000)
local updated = math.max(now, tonumber(redis.call("HGET", KEYS[2], "updated_at") or "0"))
redis.call("HSET", KEYS[2], "stage", ARGV[3], "detail", ARGV[4], "updated_at", updated)
local resources = redis.call("HGETALL", KEYS[5])
for i = 1, #resources, 2 do
  if resources[i + 1] == ARGV[3] or ARGV[5] == "1" then
    redis.call("HDEL", KEYS[4], resources[i])
    redis.call("HDEL", KEYS[5], resources[i])
  end
end
if ARGV[5] == "1" then
  redis.call("ZREM", KEYS[3], ARGV[6])
else
  redis.call("ZADD", KEYS[3], now + tonumber(ARGV[7]), ARGV[6])
end
return 1
