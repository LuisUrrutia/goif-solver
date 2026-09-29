local old = redis.call("HGET", KEYS[1], "payload")
if old then
  if old ~= ARGV[2] then
    return -1
  end
  return 0
end
local t = redis.call("TIME")
local now = t[1] * 1000 + math.floor(t[2] / 1000)
redis.call(
  "HSET",
  KEYS[1],
  "id",
  ARGV[1],
  "payload",
  ARGV[2],
  "stage",
  ARGV[3],
  "detail",
  "",
  "created_at",
  now,
  "updated_at",
  now
)
redis.call("ZADD", KEYS[2], 0, ARGV[1])
return 1
