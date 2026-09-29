if redis.call("GET", KEYS[1]) ~= ARGV[1] then
  return 0
end
local outcome = redis.call("HGET", KEYS[4], ARGV[2])
if outcome then
  if outcome == ARGV[3] then
    return 1
  end
  return -1
end
if redis.call("HEXISTS", KEYS[3], ARGV[2]) == 0 or redis.call("GET", KEYS[2]) ~= ARGV[2] then
  return -1
end
redis.call("HSET", KEYS[4], ARGV[2], ARGV[3])
redis.call("DEL", KEYS[2])
return 1
