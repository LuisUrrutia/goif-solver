if redis.call("GET", KEYS[1]) ~= ARGV[1] then
  return 0
end
redis.call("PEXPIRE", KEYS[1], ARGV[2])
if ARGV[3] ~= "" then
  local due = redis.call("ZSCORE", KEYS[2], ARGV[3])
  if due then
    local t = redis.call("TIME")
    local now = t[1] * 1000 + math.floor(t[2] / 1000)
    redis.call("HSETNX", KEYS[3], ARGV[3], due)
    local scheduled = tonumber(redis.call("HGET", KEYS[3], ARGV[3]))
    local available = math.max(scheduled, now + tonumber(ARGV[2]))
    redis.call("ZADD", KEYS[2], available, ARGV[3])
    if available < tonumber(due) then
      redis.call("PUBLISH", KEYS[4], "")
    end
  end
end
return 1
