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
| `internal/selfupdate` | finding, verifying, and installing releases over the executable | CLI parsing |
| `internal/buildinfo` | the build's version and the User-Agent | configuration |

A new connector is a package beside `internal/discord` that implements
`hub.Output`, builds `hub.Message`s, and keys conversations as
`<connector>:<id>`. It adds a section to `config.Connectors` that embeds
`Routes` and carries an `Access`, and it is wired in `cmd/inari`.

Conventions follow kon: comments explain why, in complete sentences. Run
`make check` before considering a change done, and `make test-race` for
anything concurrent.

## Releases

`inari upgrade` in every published release downloads assets by the names
`scripts/release.sh` gives them, parses `checksums.txt` as `checksums.sh`
writes it, and accepts a download only when `inari --version` prints exactly
`inari vX.Y.Z`. Changing any of the three breaks upgrades from those releases.

Pushing a `vX.Y.Z` tag publishes a release: `.github/workflows/ci.yml` builds
the archives for every target and runs `gh release create` with the tag's
message as the release notes. `make tag [BUMP=patch|minor|major]` has
`kon run` follow these steps:

1. Check both `git tag` and `git ls-remote --tags origin` and pick the next
   version that does not collide with either.
2. Format the tag as semver with a `v` prefix, e.g. `v0.1.4`.
3. Write the tag message as a summary of the changes between the previous
   tag and this one, for inari's users, in this format:

   ```
   vX.Y.Z

   ## Section

   - **Headline**: short description
   - **Headline**: short description
   ```

   The first line is the version, each `##` section groups related commits,
   and each bullet pairs a bold headline with a short description.
4. Create the tag with `git tag -a --cleanup=whitespace -F <file>`: the
   default cleanup strips every line starting with `#`, which drops Markdown
   headings.
5. Do not push the tag. Pushing publishes the release, so leave that to a
   person.
