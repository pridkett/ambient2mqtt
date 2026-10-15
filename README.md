Ambient2MQTT
============

Patrick Wagstrom &lt;patrick@wagstrom.net&gt;

October 2021

Overview
---------

This is a simple single binary server that acts as a target for your Ambient Weather station on your local network and relays the data over MQTT so other IoT related devices can make use of it.

You will need to have a newer firmware installed for your Ambient Weather station in order to make use of this feature.

Usage
-----

```bash
./ambient2mqtt -config config.toml
```

Configuration
-------------

Configuration is done via a TOML file. Here's a simple example configuration:

```toml
[http]
    port = 2466

[mqtt]
    broker_host = "mqtt-broker.lan"
    broker_port = 1883
    broker_username = ""
    broker_password = ""
    client_id = "ambient2mqtt"
    topic_prefix = "weather"
    topic = "ws-2902"

[hass]
    discovery = true
    discovery_prefix = "homeassistant"
    object_id = "ws-2902a"
    object_id = "ws-2902d"
    device_model = "ws-2902d"
    device_name = "ws-2902d"

[influx]
    Hostname = "localhost"
    Port = 8086
    Database = "ambientweather"

```

Influx requests are 1.x. Each output is optional: leave out `[influx]` or `[timescale]` to skip it.

### TimescaleDB

```toml
[timescale]
    table = "weather"
    station = "edgewater"   # stored in the station column
    # max_queue = 2000      # reports held while the database is unreachable
```

Each report becomes one row in a wide table. The program reads the table's columns at startup and fills
each one from the report key of the same name (converted to the column's type); columns a report lacks
are NULL, and report keys without a column are logged once and skipped, so a new station key only needs
a new column. Solar radiation above 1,400 W/m² and UV above 12 are sensor glitches and stored as NULL. The table needs `time` and `station` columns; `PASSKEY` (the station's MAC) and `dateutc`
are never stored. The connection comes from the standard libpq environment variables (`PGHOST`, `PGPORT`,
`PGUSER`, `PGPASSWORD`, `PGDATABASE`, `PGSSLMODE`), so the config file holds no credentials. The schema used
in testing is `testSchema` in `timescale_test.go`.

### Docker

Multi-arch images (amd64, arm64, arm/v7) are published to `ghcr.io/pridkett/ambient2mqtt` for each release.
The container listens on port 2466; point the station's custom server there (path `/data/report/`).

License
-------

Licensed under terms of the MIT license
