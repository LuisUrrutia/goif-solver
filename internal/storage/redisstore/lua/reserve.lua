if redis.call("GET", KEYS[1]) ~= ARGV[1] then
  return 0
end
if not redis.call("ZSCORE", KEYS[4], ARGV[5]) then
  return -1
end
local owner = redis.call("HGET", KEYS[2], ARGV[2])
if owner and owner ~= ARGV[3] then
  return -2
end
local stage = redis.call("HGET", KEYS[3], ARGV[2])
if stage and stage ~= ARGV[4] then
  return -1
end
redis.call("HSET", KEYS[2], ARGV[2], ARGV[3])
redis.call("HSET", KEYS[3], ARGV[2], ARGV[4])
return 1
