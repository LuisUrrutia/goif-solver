local old = redis.call("GET", KEYS[1])
if old and old ~= ARGV[1] then
  return -1
end
redis.call("SET", KEYS[1], ARGV[1])
return 1
