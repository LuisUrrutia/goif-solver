package redisstore

import (
	_ "embed"

	"github.com/redis/go-redis/v9"
)

//go:embed lua/enqueue.lua
var enqueueSource string

var enqueue = redis.NewScript(enqueueSource)

//go:embed lua/ready.lua
var readySource string

var ready = redis.NewScript(readySource)

//go:embed lua/acquire.lua
var acquireSource string

var acquire = redis.NewScript(acquireSource)

//go:embed lua/claim.lua
var claimSource string

var claim = redis.NewScript(claimSource)

//go:embed lua/claim-next.lua
var claimNextSource string

var claimNext = redis.NewScript(claimNextSource)

//go:embed lua/wait.lua
var waitSource string

var waitQueue = redis.NewScript(waitSource)

//go:embed lua/renew.lua
var renewSource string

var renew = redis.NewScript(renewSource)

//go:embed lua/release.lua
var releaseSource string

var release = redis.NewScript(releaseSource)

//go:embed lua/reserve.lua
var reserveSource string

var reserve = redis.NewScript(reserveSource)

//go:embed lua/advance.lua
var advanceSource string

var advance = redis.NewScript(advanceSource)

//go:embed lua/prepare.lua
var prepareSource string

var prepare = redis.NewScript(prepareSource)

//go:embed lua/complete.lua
var completeSource string

var complete = redis.NewScript(completeSource)

//go:embed lua/set-control.lua
var setControlSource string

var setControl = redis.NewScript(setControlSource)

//go:embed lua/bind-config.lua
var bindConfigSource string

var bindConfig = redis.NewScript(bindConfigSource)

//go:embed lua/checkpoint.lua
var checkpointSource string

var checkpoint = redis.NewScript(checkpointSource)

//go:embed lua/stats.lua
var statsSource string

var stats = redis.NewScript(statsSource)
