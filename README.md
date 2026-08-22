# eWeLink API bridge

A small Go HTTP service that turns eWeLink's MCP server into a conventional API.

It refreshes the device snapshot from eWeLink once per minute by default; eWeLink remains authoritative. A successful switch operation updates the local snapshot only until the next refresh.

> **Disclaimer:** eWeLink's MCP server is available only with the paid **Prime** MCP plan, which costs **$19.90/year**.

## Configuration

Never put the authenticated MCP URL in source control. Pass it as an environment variable:

```sh
export EWELINK_MCP_URL='https://…'
export LISTEN_ADDR=':8080'                 # optional; defaults to :8080
export REFRESH_INTERVAL='1m'                # optional; defaults to 1m
export CACHE_MODE='memory'                  # memory (default) or redis

go run ./cmd/ewelink-api
```

`CACHE_MODE=redis` shares the snapshot between bridge instances. Scheduled and startup refreshes check the shared `refreshedAt` timestamp first; an atomic short Redis lease ensures that only one replica fetches eWeLink when the snapshot is stale. Other replicas continue serving the shared snapshot, so eWeLink is polled at most once per `REFRESH_INTERVAL`. This is a per-refresh reservation, not a long-lived leader-election role. With `CACHE_MODE=memory`, each replica has its own cache and polls independently. Redis settings are optional and default to `127.0.0.1:6379`, database `0`, with no authentication:

```sh
export CACHE_MODE='redis'
export REDIS_HOST='127.0.0.1'
export REDIS_PORT='6379'
export REDIS_DB='0'
export REDIS_AUTH=''                        # optional Redis password
```

The service does an initial refresh before accepting requests. It exits if that refresh fails, preventing it from serving an empty or stale cache as if it were valid.

## HTTP API

All application responses are JSON.

| Method | Path | Purpose |
|---|---|---|
| `GET` | `/health-check` | Refresh health; `200` after the last refresh succeeded, otherwise `503` |
| `GET` | `/metrics` | Prometheus/OpenMetrics metrics, including standard Go/process metrics |
| `GET` | `/v1/devices` | Cached devices and snapshot metadata |
| `POST` | `/v1/update` | Force a device-list refresh from eWeLink and return the new snapshot (bypasses the scheduled refresh interval) |
| `GET` | `/v1/devices/force` | Force a fresh device list from eWeLink, update cache, and return the new snapshot |
| `GET` | `/v1/devices/{deviceID}` | A single cached device |
| `POST` | `/v1/devices/{deviceID}/switch` | Set or toggle its primary `switch` parameter |

`POST /switch` accepts optional URL-encoded form data. Send `state=on` or `state=off` to set the state; omit `state` to toggle the cached current state.

A device must have a scalar `params.switch` value of `on` or `off`; multi-channel devices require a future channel-aware endpoint rather than guessing a channel.

Example:

```sh
curl http://localhost:8080/health-check
curl http://localhost:8080/metrics
curl http://localhost:8080/v1/devices
curl -X POST http://localhost:8080/v1/update
curl http://localhost:8080/v1/devices/force
curl -X POST http://localhost:8080/v1/devices/1000abc/switch \
  -d 'state=on'
curl -X POST http://localhost:8080/v1/devices/1000abc/switch
```

### Response examples

A healthy `GET /health-check` returns `200 OK`:

```json
{
  "ok": true,
  "refreshedAt": "2026-08-22T10:15:30Z",
  "error": ""
}
```

`GET /v1/devices` returns the cached authoritative snapshot. `POST /v1/update` and `GET /v1/devices/force` force a refresh from eWeLink, update the configured cache, and return the same snapshot shape:

```json
{
  "devices": [
    {
      "id": "1000abc",
      "name": "Living room lamp",
      "online": true,
      "uiid": "1",
      "params": {
        "switch": "off"
      }
    }
  ],
  "refreshedAt": "2026-08-22T10:15:30Z"
}
```

A successful `POST /v1/devices/1000abc/switch` with `state=on` returns `200 OK` and the temporarily updated cached device:

```json
{
  "id": "1000abc",
  "name": "Living room lamp",
  "online": true,
  "uiid": "1",
  "params": {
    "switch": "on"
  }
}
```

Invalid switch requests return `422 Unprocessable Entity`, for example when `state` is neither `on` nor `off`:

```json
{
  "error": "unsupported state \"invalid\"; use on or off, or omit state to toggle"
}
```

If the most recent refresh failed, `GET /health-check` returns `503 Service Unavailable`:

```json
{
  "ok": false,
  "refreshedAt": "2026-08-22T10:15:30Z",
  "error": "eWeLink request timed out"
}
```

## Metrics

The `/metrics` endpoint exposes the standard Prometheus Go and process metrics plus:

- `ewelink_device_state{id, name, online, uiid}` — current primary switch state (`on=1`, `off=0`)
- `ewelink_device_switches_total{id, name, online, uiid}` — successful primary switch operations

Devices without a scalar `params.switch` state are omitted from these device-specific metrics.

## MCP mapping

The bridge uses the MCP `getBasicInformation` tool (`{"extraParams":{}}`) for refreshes and `controlSingleDevice` for changes. Control requests provide the cached `deviceid`, `name`, `params: {"switch": "on"|"off"}`, and `extraParams: {}`.

The bridge does not retry a control request: a network failure after it has been sent leaves the final device state unknown. It only modifies local cache state after the MCP tool reports success.

## Tests

```sh
go test ./...
```
