local current = redis.call("GET", KEYS[1])
local version = 0
if current then
  version = cjson.decode(current).version
end
if version ~= tonumber(ARGV[1]) then
  return -1
end
redis.call("SET", KEYS[1], ARGV[2])
return 1
