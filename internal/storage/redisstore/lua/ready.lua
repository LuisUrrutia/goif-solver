local t = redis.call("TIME")
local now = t[1] * 1000 + math.floor(t[2] / 1000)
return redis.call("ZRANGEBYSCORE", KEYS[1], "-inf", now, "LIMIT", ARGV[2], ARGV[1])
