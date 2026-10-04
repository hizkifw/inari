# inari

inari puts [kon](https://kon.kitsu.red) in your chat. It runs `kon acp` and
connects each chat channel to its own kon session over the
[Agent Client Protocol](https://agentclientprotocol.com). It speaks Discord
and Telegram.

It is named for Inari, whose foxes carry messages between the shrine and
the people; inari carries them between your chat and kon.

kon has no sandbox and runs commands without asking. Anyone inari lets in can
run anything kon can, as the user inari runs as. Trust accordingly.

## How it works

- **One session per channel.** Everyone in a channel shares its transcript.
  Each message reaches kon as `[name] text`, so the model knows who said what.
- **Steering.** A message sent while kon works is added to the running turn,
  as Enter does in kon's own UI. What kon says once it has read the message
  is posted as a reply to it, so the answer is easy to find in a chat that
  has moved on.
- **Attachments as files.** Each attachment is saved to a temporary
  directory per session and listed in the message by path, so kon reads it
  with its tools as it would any file. `/new` removes the session's files,
  and inari removes them all when it stops.
- **A message per thing kon says.** Each stretch of text is posted once
  complete, as its own message. The tool calls kon makes after it are edited
  into that message as small lines (`… read main.go`, then
  `✓ read main.go`), at most one edit per 1.5 seconds on Discord and 2 on
  Telegram. The typing indicator shows while kon works.
- **Streaming in Telegram private chats.** There, kon's text streams in
  token by token as a Telegram draft while it is written, and becomes a
  message once complete. `stop_button` adds a button to drafts that
  cancels the turn.
  Telegram offers drafts only in private chats, so groups get whole
  messages.
- **Background jobs.** When a job kon started finishes after its turn, kon
  takes a turn of its own and the result is posted to the channel.
- **Cron jobs and reminders.** kon schedules its own with `inari cron`. A
  job runs in a fresh session and reports back to the chat it was asked
  for in; a reminder just comes back there. See [Cron jobs](#cron-jobs).
- **Replies carry context.** A message that replies to another reaches kon
  with who it replies to and the start of what they said, so "do that one"
  makes sense in a busy chat.
- **Sessions survive restarts.** inari remembers each channel's session in
  `$XDG_STATE_HOME/inari/sessions.json` and resumes it. A turn that inari
  stopping cut off, such as by an upgrade kon ran, is resumed when inari
  starts again, and kon is told to pick up where it stopped. One cut off
  twice in a row is left for a person to restart.

## Setup

1. Install a kon with ACP support (v0.1.15 or later). Run `kon` once to log
   in and pick a default model; inari uses kon's own configuration.
2. Create a bot on Discord, Telegram, or both; see
   [Discord](#discord) and [Telegram](#telegram) below.
3. Install inari. On Linux and macOS:

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

4. Write `~/.config/inari/config.json` (`$XDG_CONFIG_HOME/inari` when that
   is set, `%APPDATA%\inari` on Windows), starting from
   [config.example.json](config.example.json), and run:

   ```sh
   inari
   ```

   The file holds the bot tokens, so keep it private: `chmod 600` it.

   `inari -config path` reads another file, and `-debug` logs more.

### Discord

1. In the [Discord developer portal](https://discord.com/developers/applications),
   create an application and its bot, and turn on the **Message Content**
   privileged intent.
2. Invite the bot with the `bot` and `applications.commands` scopes and the
   View Channels, Send Messages, Send Messages in Threads, and Read Message
   History permissions.

### Telegram

1. Ask [@BotFather](https://t.me/BotFather) for a new bot with `/newbot`,
   and put its token in `connectors.telegram.token`.
2. To use it in a group, turn off its privacy mode (`/setprivacy` in
   @BotFather) before adding it, or make it an admin; otherwise Telegram
   shows it only commands and replies to it. A forum topic is a
   conversation of its own.
3. Telegram's apps show no IDs. Message the bot privately and it replies
   with your user ID to add to `access.users`. For a group's ID, run
   `inari -debug`: it logs the user and chat of each message it ignores.

## Running in the background

```sh
inari daemon install    # run inari as a service, at login and after a failure
inari daemon uninstall  # stop it and remove the service
```

On Linux this is a systemd user service, `inari.service`; see its logs with
`journalctl --user -u inari -f`. Other platforms are not supported yet.

`install` checks the config first, then starts the service, and running it
again replaces the service. The service does not see your shell, so install
copies what inari needs from it: `PATH` (to find kon) and the `XDG_*`
directories. Run it from the shell you would run `inari` in, and again after
changing either. `-config path` and `-debug` are passed on to the service.

A user service stops when you log out unless lingering is on:
`loginctl enable-linger`.

## Upgrading

```sh
inari upgrade          # install the latest release over this one
inari upgrade --check  # only report whether there is one
```

It verifies the download's checksum and that the new binary runs before
replacing anything. When inari runs as a service, the upgrade restarts it
onto the new version, so you can also just ask kon to upgrade inari. Any
other running inari keeps the old version until it restarts.

## Configuration

```json
{
  "kon": {"command": "kon", "args": ["acp"]},
  "state_dir": "",
  "connectors": {
    "discord": {
      "token": "…",
      "guilds": ["…"],
      "access": {"users": ["…"], "channels": ["…"]},
      "channels": {"…": {"cwd": "/abs/path", "instructions": "…"}},
      "default_cwd": "/abs/path",
      "instructions": "…"
    },
    "telegram": {
      "token": "…",
      "api_url": "",
      "stop_button": false,
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
| `token` | The bot token. |
| `guilds` | Discord only: servers to register slash commands in, where they appear at once. Empty registers them globally, which can take a while to show and also works in DMs. |
| `api_url` | Telegram only: a [local Bot API server](https://github.com/tdlib/telegram-bot-api) to use instead of Telegram's. |
| `stop_button` | Telegram only: put a button on streamed drafts that cancels the turn. Off by default, since Telegram animates it in with every new draft; `/cancel` works either way. |
| `access.users` | User IDs trusted in any channel inari serves. |
| `access.channels` | Channel IDs where everyone is trusted. |
| `channels.<id>.cwd` | That channel's working directory. |
| `default_cwd` | The working directory of a channel with none of its own. Leave it empty to serve only listed channels. |
| `instructions` | Told to every channel's new sessions, such as house rules. |
| `channels.<id>.instructions` | Told to that channel's new sessions, after the connector's, such as what the channel is for. |

On Telegram, a channel is a chat ID, and a forum topic is
`<chat id>/<topic id>`. A topic with no `channels` entry of its own uses its
chat's, and everyone in a trusted chat is trusted in its topics.

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
2. how the platform shows its replies: which Markdown renders, where long
   messages split, and that people see tool calls but not their output;
3. the connector's `instructions`, then the channel's.

It is also told its conversation's ID, for scheduling jobs that come back
to it.

kon fixes a session's system prompt when it starts, so changed instructions
apply after `/new`. A resumed session keeps the ones it started with.

kon's own flags shape every session too. Pass them in `kon.args`:

```json
"kon": {"args": ["acp", "--instructions-file", "/abs/house-rules.md", "--system-prompt-override", "/abs/persona.md"]}
```

`--system-prompt-override` replaces kon's built-in prompt, which is written for
a coding agent in a terminal, while keeping everything above.

## Cron jobs

Name a home channel to turn cron on. Jobs and reminders report to the chat
they were asked for in, and to home when they have no chat or theirs is no
longer served:

```json
"cron": {"home": "discord:<channel id>", "timezone": "Asia/Singapore", "timeout": "1h"}
```

| Key | Meaning |
| --- | --- |
| `cron.home` | The channel a job reports to when it names no chat of its own, as `discord:<channel id>` or `telegram:<chat id>`. It must be a channel inari serves. |
| `cron.timezone` | Where schedules are read, as an IANA name. Defaults to the machine's. |
| `cron.timeout` | How long a run may take before it is stopped. Defaults to `1h`. |

With cron on, every session is told it can schedule jobs and reminders, so
you can just ask: "remind me to check the deploy in two hours", or "every
weekday at 9, summarize yesterday's commits". kon runs `inari cron` itself:

```sh
inari cron add --to discord:123 --name standup --schedule "0 9 * * 1-5" "Summarize yesterday's commits."
inari cron add --to telegram:456 --reminder --name deploy --in 2h "Remind Alice to check the deploy."
inari cron list
inari cron remove standup
```

Each session is told its conversation's ID, such as `telegram:456`, and
passes it as `--to`. A session started before inari told sessions this does
not know it, so its jobs go home until `/new`.

- **A job** runs its prompt in a new session and reports back, for work:
  checking a build, summarizing a day.
- **A reminder** (`--reminder`) runs nothing: at its time its text goes
  straight to the chat's agent, for nudges: "tell Alice the freeze
  starts in 10 minutes".

A schedule is a cron expression, `@hourly`/`@daily`/`@weekly`/`@monthly`,
`@every 30m`, or a one-shot `--at` time or `--in` delay. A job keeps the
directory it was added from.

At each due time a job's prompt runs in a new, empty kon session, from the
author `[cron:NAME]`. When it finishes, its final message is sent to its
chat's session as `[cron notice: NAME] …`. A reminder is sent there as
`[reminder: NAME] …` at once. Either steers a turn that is running or starts
one, and the agent there relays it to the channel. A small `⏰` line shows in
the channel as each arrives.

- A run that was due while inari was not running is skipped, except a
  one-shot job or reminder, which comes late and says so.
- A job whose last run is still going skips its next one.
- Jobs are plain files in `$XDG_STATE_HOME/inari/cron/`, one per job.

## Commands

Discord offers these as slash commands; on Telegram they are bot commands,
listed in the chat's command menu.

| Command | Does |
| --- | --- |
| `/cancel` | Stop the running turn and the messages waiting behind it. |
| `/new` | Close the channel's session; the next message starts a fresh one. |
| `/compact` | Summarize the conversation to free context. |
| `/model [name]` | Show or set the model, with autocomplete on Discord and a button per model on Telegram. |
| `/effort [level]` | Show or set the reasoning effort, likewise. |
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
