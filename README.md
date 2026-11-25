<div align="center">
  <img src="https://raw.githubusercontent.com/TypeType-Video/TypeType/main/assets/banner.svg" alt="TypeType" width="100%">
  <h1>TypeType Server Go</h1>
  <p>The Go rewrite of the TypeType backend: extraction, API, and private user data.</p>
</div>

You want to know the current position of TypeType about AI ? Go check [this](https://github.com/TypeType-Video/TypeType/blob/dev/AI_TRANSPARENCY.md).

> [!WARNING]
> This server is experimental. It is not part of any TypeType release yet and must not run a real instance.
> The plan is for it to replace [TypeType-Server](https://github.com/TypeType-Video/TypeType-Server) completely
> once it matches it everywhere.

TypeType-Server-Go is a Go port of the Kotlin/Ktor server behind TypeType. It serves the same HTTP API, routes, and
JSON, reads the same environment variables, and uses the same PostgreSQL schema and Dragonfly keys, so the web, Android
and TV clients talk to it unchanged. It ships as one static binary with no C, in a distroless image.

## Responsibilities

| Area | Examples |
| --- | --- |
| Extraction | YouTube, Bilibili and NicoNico streams, search, trending, comments, and channels |
| Playback sessions | YouTube SABR sessions, formats, segments, seeks, and recovery state |
| User data | Accounts, history, subscriptions, playlists, favorites, progress, and settings |
| Imports | YouTube Takeout and PipePipe, NewPipe, Invidious, Piped, and LibreTube backups |
| Integrations | TypeType-Token, TypeType-Downloader, media proxying, OIDC, and notifications |
| Administration | Instance settings, users, sessions, allow lists, and bug reports |

YouTube token and decoder work stays in [TypeType-Token](https://github.com/TypeType-Video/TypeType-Token), and
download execution in [TypeType-Downloader](https://github.com/TypeType-Video/TypeType-Downloader).

## Stack

| Role | Technology |
| --- | --- |
| Language and runtime | Go 1.27, standard library HTTP server |
| Persistence | PostgreSQL through pgx |
| Cache | Dragonfly through the Redis protocol |
| Image | Static binary on distroless, non-root |
| Tests | `go test`, race detector, fuzzing against encoding/json |

## API contract

The server answers the contract of [TypeType-Server](https://github.com/TypeType-Video/TypeType-Server); its OpenAPI
files are vendored under [`api/`](api/) and every route of the Kotlin server is in the router.

## Development

Requirements:

- Go 1.27
- PostgreSQL and Dragonfly, for example from the TypeType-Server compose file

```sh
go build -o bin/typetype-server ./cmd/typetype-server
DATABASE_URL=jdbc:postgresql://127.0.0.1:5432/typetype DRAGONFLY_URL=redis://127.0.0.1:6379 \
  JWT_SECRET=change-me bin/typetype-server
```

The server listens on `http://localhost:8080` by default. The container image builds with `docker build .`.

## Checks

```sh
go vet ./...
go test -race ./...
CGO_ENABLED=0 go build ./cmd/typetype-server
```

Bug reports and feature requests belong in the [central issue tracker](https://github.com/TypeType-Video/TypeType/issues).

## License

TypeType-Server-Go is licensed under [AGPL-3.0](LICENSE).
