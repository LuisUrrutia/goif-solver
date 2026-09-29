local current = redis.call("GET", KEYS[1]) or ""
if current ~= ARGV[1] then
  return 0
end
redis.call("SET", KEYS[1], ARGV[2])
return 1
