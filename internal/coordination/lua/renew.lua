if redis.call("GET", KEYS[1]) ~= ARGV[1] then
  return 0
end
return redis.call("PEXPIRE", KEYS[1], ARGV[2])
