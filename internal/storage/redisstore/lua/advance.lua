if redis.call("GET", KEYS[1]) ~= ARGV[1] then
  return 0
end
if redis.call("HGET", KEYS[2], "stage") ~= ARGV[2] then
  return -1
end
redis.call("HSET", KEYS[2], "stage", ARGV[3], "detail", ARGV[4])
if ARGV[5] == "1" then
  redis.call("ZREM", KEYS[3], ARGV[6])
else
  local t = redis.call("TIME")
  local now = t[1] * 1000 + math.floor(t[2] / 1000)
  redis.call("ZADD", KEYS[3], now + tonumber(ARGV[7]), ARGV[6])
end
return 1
