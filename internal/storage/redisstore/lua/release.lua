if redis.call("GET", KEYS[1]) ~= ARGV[1] then
  return 0
end
redis.call("DEL", KEYS[1])
if ARGV[2] ~= "" then
  local due = redis.call("HGET", KEYS[3], ARGV[2])
  if due and redis.call("ZSCORE", KEYS[2], ARGV[2]) then
    redis.call("ZADD", KEYS[2], due, ARGV[2])
    redis.call("PUBLISH", KEYS[4], "")
  end
  redis.call("HDEL", KEYS[3], ARGV[2])
end
return 1
