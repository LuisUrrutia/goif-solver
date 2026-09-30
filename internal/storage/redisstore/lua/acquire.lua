if redis.call("EXISTS", KEYS[1]) == 1 then
  return 0
end
local token = redis.call("INCR", KEYS[2])
redis.call("SET", KEYS[1], token, "PX", ARGV[1])
if ARGV[2] ~= "" then
  local due = redis.call("ZSCORE", KEYS[3], ARGV[2])
  if due then
    local t = redis.call("TIME")
    local now = t[1] * 1000 + math.floor(t[2] / 1000)
    redis.call("HSETNX", KEYS[4], ARGV[2], due)
    local scheduled = tonumber(redis.call("HGET", KEYS[4], ARGV[2]))
    redis.call("ZADD", KEYS[3], math.max(scheduled, now + tonumber(ARGV[1])), ARGV[2])
  end
end
return token
