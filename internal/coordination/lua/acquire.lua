if redis.call("EXISTS", KEYS[1]) == 1 then
  return 0
end
local token = redis.call("INCR", KEYS[2])
redis.call("SET", KEYS[1], token, "PX", ARGV[1])
return token
