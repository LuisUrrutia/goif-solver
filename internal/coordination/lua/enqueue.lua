local old = redis.call("HGET", KEYS[1], "payload")
if old then
  if old ~= ARGV[2] then
    return -1
  end
  return 0
end
redis.call("HSET", KEYS[1], "id", ARGV[1], "payload", ARGV[2], "stage", "discovered", "detail", "")
redis.call("ZADD", KEYS[2], 0, ARGV[1])
return 1
