local t = redis.call("TIME")
local now = t[1] * 1000 + math.floor(t[2] / 1000)
local function age(key)
  local oldest = redis.call("ZRANGE", key, 0, 0, "WITHSCORES")
  if #oldest == 0 then
    return 0
  end
  return math.max(0, now - tonumber(oldest[2]))
end
return {
  redis.call("ZCARD", KEYS[1]),
  redis.call("ZCOUNT", KEYS[1], "-inf", now),
  age(KEYS[1]),
  redis.call("ZCARD", KEYS[2]),
  age(KEYS[2]),
}
