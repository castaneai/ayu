# ayu

[![Go Reference](https://pkg.go.dev/badge/github.com/castaneai/ayu.svg)](https://pkg.go.dev/github.com/castaneai/ayu)
[![Go Report Card](https://goreportcard.com/badge/github.com/castaneai/ayu)](https://goreportcard.com/report/github.com/castaneai/ayu)

ayu is WebRTC Signaling Server with [ayame](https://github.com/OpenAyame/ayame)-like protocol.

- **Scalable**: ayu uses Redis to store room states, so it can be used on serverless platforms (e.g. Cloud Run).
- **Composable**: ayu provides a net/http compatible WebSocket handler.
- **Customizable**: ayu provides authentication and logger interface, which can be customized.
- **No vendor lock-in**: ayu depends only on Redis. It is not locked in to any specific cloud provider.

## Usage

In the following example, `ws://localhost:8080/signaling` will be the endpoint of the signaling server.
Please see [ayame-spec](https://github.com/OpenAyame/ayame-spec) for details.

```go
package main

import (
	"log"
	"net/http"

	"github.com/castaneai/ayu"
	"github.com/go-redis/redis/v8"
)

func main() {
	redisClient := redis.NewClient(&redis.Options{Addr: "x.x.x.x:6379"})
	sv := ayu.NewServer(redisClient)
	defer sv.Shutdown()
	http.Handle("/signaling", sv)

	addr := ":8080"
	log.Printf("listening on %s...", addr)
	log.Fatal(http.ListenAndServe(addr, nil))
}
```

## Testing

```
make test
```

## License 

[Apache-2.0](./LICENSE)
