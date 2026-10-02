inari connects [kon](https://github.com/hizkifw/kon) to chat platforms over
ACP. It runs one `kon acp` subprocess and maps each chat conversation to one
kon session. kon's ACP behavior, extensions included, is documented in
[kon's editor integration guide](https://github.com/hizkifw/kon/blob/main/docs/product/acp.md);
read it before changing the client.

Prefer the standard library. The only direct dependency is discordgo.

Each package owns one boundary:

| Package | Owns | Must not own |
| --- | --- | --- |
| `cmd/inari` | startup wiring and flags | business logic |
| `internal/acp` | the ACP client: JSON-RPC over stdio, wire types, kon's extensions | conversations or chat |
| `internal/hub` | conversations to sessions, turn tracking, steering, gathering a turn into posts | anything platform-specific |
| `internal/discord` | the Discord connector: gateway, slash commands, rendering | session or turn semantics |
| `internal/config` | config.json, shared `Access` and `Routes`, one section per connector | runtime state |
| `internal/store` | the conversation-to-session file | session contents |

A new connector is a package beside `internal/discord` that implements
`hub.Output`, builds `hub.Message`s, and keys conversations as
`<connector>:<id>`. It adds a section to `config.Connectors` that embeds
`Routes` and carries an `Access`, and it is wired in `cmd/inari`.

Conventions follow kon: comments explain why, in complete sentences. Run
`make check` before considering a change done, and `make test-race` for
anything concurrent.
