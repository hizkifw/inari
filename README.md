# inari

inari puts [kon](https://kon.kitsu.red) in your chat. It runs `kon acp` and
connects each chat channel to its own kon session over the
[Agent Client Protocol](https://agentclientprotocol.com). v0 speaks Discord.

It is named for Inari, whose foxes carry messages between the shrine and
the people; inari carries them between your chat and kon.

kon has no sandbox and runs commands without asking. Anyone inari lets in can
run anything kon can, as the user inari runs as. Trust accordingly.

## How it works

- **One session per channel.** Everyone in a channel shares its transcript.
  Each message reaches kon as `[name] text`, so the model knows who said what.
- **Steering.** A message sent while kon works is added to the running turn,
  as Enter does in kon's own UI. A message with attachments waits for its own
  turn instead.
- **A message per thing kon says.** Text is not streamed token by token:
  each stretch of it is posted once complete, as its own message. The tool
  calls kon makes after it are edited into that message as small lines
  (`… read main.go`, then `✓ read main.go`), at most one edit per 1.5
  seconds. The typing indicator shows while kon works.
- **Background jobs.** When a job kon started finishes after its turn, kon
  takes a turn of its own and the result is posted to the channel.
- **Sessions survive restarts.** inari remembers each channel's session in
  `$XDG_STATE_HOME/inari/sessions.json` and resumes it.

## Setup

1. Install a kon with ACP support (v0.1.15 or later). Run `kon` once to log
   in and pick a default model; inari uses kon's own configuration.
2. In the [Discord developer portal](https://discord.com/developers/applications),
   create an application and its bot, and turn on the **Message Content**
   privileged intent.
3. Invite the bot with the `bot` and `applications.commands` scopes and the
   View Channels, Send Messages, Send Messages in Threads, and Read Message
   History permissions.
4. Install inari. On Linux and macOS:

   ```sh
   curl -fsSL https://raw.githubusercontent.com/hizkifw/inari/main/scripts/install.sh | sh
   ```

   On Windows, in PowerShell:

   ```powershell
   iwr -useb https://raw.githubusercontent.com/hizkifw/inari/main/scripts/install.ps1 | iex
   ```

   The installers verify the release's checksum. Set `INARI_VERSION` for a
   specific release, or `INARI_INSTALL_DIR` for another directory than
   `~/.local/bin` (`%LOCALAPPDATA%\Programs\inari` on Windows). With Go:

   ```sh
   go install github.com/hizkifw/inari/cmd/inari@latest
   ```

5. Write `~/.config/inari/config.json` (`$XDG_CONFIG_HOME/inari` when that
   is set, `%APPDATA%\inari` on Windows), starting from
   [config.example.json](config.example.json), and run:

   ```sh
   export INARI_DISCORD_TOKEN=…
   inari
   ```

   `inari -config path` reads another file, and `-debug` logs more.

## Upgrading

```sh
inari upgrade          # install the latest release over this one
inari upgrade --check  # only report whether there is one
```

It verifies the download's checksum and that the new binary runs before
replacing anything. A running inari keeps the old version until it restarts.

## Configuration

```json
{
  "kon": {"command": "kon", "args": ["acp"]},
  "state_dir": "",
  "connectors": {
    "discord": {
      "token_env": "INARI_DISCORD_TOKEN",
      "guilds": ["…"],
      "access": {"users": ["…"], "channels": ["…"]},
      "channels": {"…": {"cwd": "/abs/path", "instructions": "…"}},
      "default_cwd": "/abs/path",
      "instructions": "…"
    }
  }
}
```

| Key | Meaning |
| --- | --- |
| `kon` | How to start kon. Defaults to `kon acp` from PATH. |
| `state_dir` | Where inari keeps its state. Defaults to `$XDG_STATE_HOME/inari`. |
| `token` / `token_env` | The bot token, or the environment variable holding it. |
| `guilds` | Servers to register slash commands in, where they appear at once. Empty registers them globally, which can take a while to show and also works in DMs. |
| `access.users` | User IDs trusted in any channel inari serves. |
| `access.channels` | Channel IDs where everyone is trusted. |
| `channels.<id>.cwd` | That channel's working directory. |
| `default_cwd` | The working directory of a channel with none of its own. Leave it empty to serve only listed channels. |
| `instructions` | Told to every channel's new sessions, such as house rules. |
| `channels.<id>.instructions` | Told to that channel's new sessions, after the connector's, such as what the channel is for. |

A message is answered only when its channel has a working directory **and**
its author or channel is trusted. Everything else is ignored. Unknown keys
are errors, so a typo cannot quietly loosen access.

Every connector shares the same `access`, `channels`, `default_cwd`, and
`instructions` shape, so a new one adds a section under `connectors` and
nothing else changes.

### What kon is told

Each new session's system prompt gets, after kon's own and any `AGENTS.md`
in its directory:

1. that it is in a group chat where each message starts with `[name]`;
2. how Discord shows its replies: which Markdown renders, the 2000-character
   split, and that people see tool calls but not their output;
3. the connector's `instructions`, then the channel's.

kon fixes a session's system prompt when it starts, so changed instructions
apply after `/new`. A resumed session keeps the ones it started with.

kon's own flags shape every session too. Pass them in `kon.args`:

```json
"kon": {"args": ["acp", "--instructions-file", "/abs/house-rules.md", "--system-prompt-override", "/abs/persona.md"]}
```

`--system-prompt-override` replaces kon's built-in prompt, which is written for
a coding agent in a terminal, while keeping everything above.

## Slash commands

| Command | Does |
| --- | --- |
| `/cancel` | Stop the running turn and the messages waiting behind it. |
| `/new` | Close the channel's session; the next message starts a fresh one. |
| `/compact` | Summarize the conversation to free context. |
| `/model [name]` | Show or set the model, with autocomplete. |
| `/effort [level]` | Show or set the reasoning effort, with autocomplete. |
| `/status` | Session, model, context use, and cost. |
| `/jobs` | List background jobs. |
| `/kill id` | Stop a background job. |

kon saves model and effort as its defaults, as `/model` does in its own UI.
Changing them in one channel therefore also changes them for sessions started later.

## Development

```sh
make check            # gofmt, vet, shuffled tests
make test-race        # race detector
make test-kon KON=…   # round trip with a real kon acp
make build            # bin/inari
make release VERSION=v0.1.0  # every release archive, into dist/
```

Pushing a `vX.Y.Z` tag publishes a release; see Releases in
[AGENTS.md](AGENTS.md).

## License

MIT
