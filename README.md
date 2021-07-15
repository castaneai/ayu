# ayu

ayu is WebRTC Signaling Server with [ayame](https://github.com/OpenAyame/ayame)-like protocol.

- **Scalable**: ayu uses Redis to store the Room state, so it can be used on serverless WebSocket service(e.g. Cloud Run).
- **No vendor lock-in**: ayu depends only on Go and Redis. It is not locked in to any particular cloud provider.
- **Composable**: ayu provides a net/http.Handler compatible WebSocket server in a Go package.
- **Customizable**: ayu provides authentication and logger interface, which can be customized.

## Testing

```
make test
```

## License 

[Apache-2.0](./LICENSE)
