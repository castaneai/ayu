package main

import (
	"fmt"
	"log"
	"net/http"
	"os"

	"github.com/castaneai/ayu"
	"github.com/go-redis/redis/v8"
)

func main() {
	rd := redis.NewClient(&redis.Options{Addr: "127.0.0.1:6379"})
	rm := ayu.NewRedisRoomManager(rd)
	sv := ayu.NewServer(rm)
	http.Handle("/signaling", sv)

	addr := ":8088"
	if p := os.Getenv("PORT"); p != "" {
		addr = fmt.Sprintf(":%s", p)
	}
	log.Printf("listening on %s...", addr)
	log.Fatal(http.ListenAndServe(addr, nil))
}
