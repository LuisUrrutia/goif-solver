local t = redis.call("TIME")
local now = t[1] * 1000 + math.floor(t[2] / 1000)
for i = 2, #ARGV do
  local id = ARGV[i]
  local due = redis.call("ZSCORE", KEYS[1], id)
  if due and tonumber(due) <= now then
    local lease = KEYS[2 * i - 1]
    local remaining = redis.call("PTTL", lease)
    if remaining == -1 then
      return redis.error_reply("intent lease has no expiry")
    end
    redis.call("HSETNX", KEYS[2], id, due)
    if remaining >= 0 then
      redis.call("ZADD", KEYS[1], now + math.max(remaining, 1), id)
    else
      local token = redis.call("INCR", KEYS[2 * i])
      redis.call("SET", lease, token, "PX", ARGV[1])
      redis.call("ZADD", KEYS[1], now + tonumber(ARGV[1]), id)
      return { id, tostring(token) }
    end
  end
end
return {}
