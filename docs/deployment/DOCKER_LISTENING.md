# Docker listening contract

The application defaults to `127.0.0.1:15631` when started as a native
binary. The Docker image must set the explicit container configuration
`DDBOT_AI_HTTP_LISTEN=0.0.0.0:15631`; the process never guesses whether it is
inside a container.

`EXPOSE 15631` is image metadata only. A Compose deployment must either omit
`ports` and let a reverse proxy reach `http://ddbot-ai:15631` over the internal
network, or use the local-only mapping:

```yaml
ports:
  - "127.0.0.1:15631:15631"
```

The default deployment must not use `15631:15631`, because that publishes the
Dashboard on every host interface. Authentication, CSRF, and TLS requirements
are unchanged by the container's internal `0.0.0.0` listener.
