if redis.call("GET", KEYS[1]) ~= ARGV[1] or redis.call("GET", KEYS[2]) ~= ARGV[2] then
  return 0
end
local existing = redis.call("HGET", KEYS[3], ARGV[3])
if existing then
  if existing == ARGV[4] then
    return 1
  else
    return -1
  end
end
local pending = redis.call("GET", KEYS[4])
if pending and pending ~= ARGV[3] then
  return -2
end
redis.call("HSET", KEYS[3], ARGV[3], ARGV[4])
redis.call("SET", KEYS[4], ARGV[3])
return 1
