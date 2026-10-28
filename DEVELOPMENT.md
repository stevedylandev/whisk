# Developing whisk

This is the guide to working on whisk's code: how it's laid out, how the parts
fit together, and where to make common changes. For what whisk does and how to
use it, see [README.md](README.md). 

## Quick start

```sh
go build -o whisk .          # build
go run . --server localhost:8080 --name dev   # run against a local server
go test ./...                # unit tests
go test -race ./...          # tests under the race detector
go vet ./...
```

You need the Go version named in `go.mod`. The public demo server is
`wss://server.apron.chat/`. A local `aprond` on `http://localhost:8080` also
works: whisk maps `http` to `ws` and retries the dial at `/ws` when the root
serves a web page.

To keep your real servers and tokens out of a dev run, pass
`--config ./dev.json.local`. The `.gitignore` already ignores `*.json.local`.

## Layout

```
whisk/
├── main.go                 flags, config load, theme, start Bubble Tea
└── internal/
    ├── protocol/           wire types and codecs; no I/O
    │   ├── frame.go        frame envelope, error codes, request/notification encoding
    │   ├── types.go        Server, User, Message, Room, History*, Reactions*, …
    │   ├── logid.go        log id compare / +1 / −1 (LogIDAfter, NextLogID, PrevLogID)
    │   └── id.go           random request ids
    ├── client/             WebSocket transport; no UI dependencies
    │   └── client.go       Dial (+ /ws fallback), Request/Notify, read loop, ping, Done
    ├── config/             config file: saved servers, theme, legacy migration
    │   └── config.go       Load/Save, NormalizeURL, Find/Upsert/Remove
    ├── state/              protocol state machine; pure, no I/O or goroutines
    │   └── store.go        rooms, messages, users, reactions, typing, mutes,
    │                       notices, history cursors and checkpoints
    └── ui/                 Bubble Tea app (one package, split by topic)
        ├── model.go        Model, NewModel, Init/Update/View, status line, shared helpers
        ├── session.go      session (one per server) and lookup/add/drop helpers
        ├── cmds.go         tea.Cmds that do I/O, and the messages they return
        ├── connection.go   connect, greeting, reconnect/backoff, incoming events
        ├── requests.go     request replies, dispatched by tag; RPC error handling
        ├── history.go      history paging and reconnect recovery (per-room and room-less)
        ├── activity.go     typing, read cursors, idle reporting
        ├── auth.go         sign-in flow: server prompt, method chooser, prompts, finishAuth
        ├── auth_view.go    sign-in screen rendering
        ├── chat.go         chat key handling, selection, message actions, sidebar model
        ├── chat_view.go    chat layout and rendering: panes, header, sidebar, bottom bar
        ├── commands.go     slash commands (/nick, /mute, /join, /server, …)
        ├── compose.go      sending: messages, replies, edits (saves), commands, uploads
        ├── paths.go        local paths: ~ expansion, tab completion for /attach
        ├── emoji.go        emoji picker: catalog search, key handling, reactions
        ├── emoji_view.go   emoji picker popup rendering
        ├── emoji_data.go   embedded searchable emoji names (generated)
        ├── compress.go     image compression for uploads
        ├── help.go         help modal, plus fetching the server's /help
        ├── render.go       message, embed, reply and notice rendering; mentions
        └── styles.go       theme, colors, lipgloss styles
```

The packages depend on each other in one direction only:

```
main ──▶ ui ──▶ client ──▶ protocol
          │                  ▲
          ├──▶ state ────────┘
          └──▶ config
```

`protocol`, `state` and `config` never import the UI, and `state` never does
I/O, so you can unit test them directly.

## How it fits together

### Sessions

whisk stays connected to every saved server at once. Each server has a
`session` (`ui/session.go`) that holds:

- `cfg`: the saved URL, guest name and token (`config.Server`)
- `client`: the live `*client.Client`, or nil while disconnected
- `store`: the server's `*state.Store`, which survives reconnects
- connection flags: `connecting`, `reconnecting`, `authed`, `needsSignIn`,
  `noReconnect`, and others
- per-server UI state: unread counts, sidebar collapse, and pending
  join/upload/help work

`Model.sessions` lists the sessions in sidebar order. `Model.cur()` returns
the session whose room is open. It is never nil: with no servers it returns
an empty placeholder (`m.none`). Each session has a stable `id`. Every async
message carries that id (`sid`), so Update drops replies that belong to a
removed session.

### The Bubble Tea bridge

All state lives in `Model` and changes only inside `Update`, on the Bubble Tea
goroutine. Nothing else touches it, so it needs no locks. I/O runs in
`tea.Cmd` functions (`ui/cmds.go`), which report back with messages:

| Cmd                          | Returns                               | Handled in        |
|------------------------------|---------------------------------------|-------------------|
| `connectCmd`                 | `connectedMsg` / `connectErrMsg`      | `connection.go`   |
| `waitForEvent`               | `eventMsg` / `disconnectMsg`          | `connection.go`   |
| `sessionRequest`/`requestCmd`| `requestDoneMsg{tag, roomID, …}`      | `requests.go`     |
| `notifyCmd`                  | `noopMsg`                             | —                 |
| `uploadCmd`                  | `uploadDoneMsg`                       | `model.go`        |

`waitForEvent` blocks on the client's event channel and returns one event. The
event handler then issues `waitForEvent` again, so each connection always has
exactly one reader. `eventMsg` and `disconnectMsg` also carry the
`*client.Client` they came from. A message from a connection that has since
been replaced does not match `s.client` and is ignored.

### Request tags

Every request goes out with a tag, which is often the method name. The reply's
`requestDoneMsg.tag` decides which handler in `handleRequestDone` runs:

| Tag                                 | Sent by                         | Reply handler                    |
|-------------------------------------|---------------------------------|----------------------------------|
| `auth`, `auth_code`                 | `sendAuthScheme`, `sendCodeApproval` | `finishAuth`               |
| `auth_email` / `auth_external`      | email/external proposals        | `onAuthEmailSent` / `onAuthExternal` |
| `room_list`                         | `roomListCmd`                   | `onRoomList`                     |
| `room_join`, `room_leave`, `room_set` | sidebar, `/join`, `/leave`, `/new`, `/thread` | refresh `room_list`  |
| `history` / `history_forward`       | `historyCmd` / `historyForwardCmd` | `onRoomHistory` / `onRoomForward` |
| `log_history` / `log_forward`       | `logHistoryCmd` / `logForwardCmd`  | `onLogHistory` / `onLogForward`   |
| `me`                                | `/nick`, `/status`, `/avatar` (clear) | `store.ApplyMe`            |
| `message_upload`, `command_upload`  | `/attach`, `/avatar PATH`       | `onUploadCreated` → `uploadCmd`  |
| `server_help`                       | help modal                      | `onServerHelpDone`               |
| `message`, `command`, `reactions`, `status` | composer, keys          | none; the broadcast carries the result |

A transport error, or an error reply, takes a shared path through
`handleRPCError`. Room-less history errors and `server_help` errors have their
own handlers, because they fall back instead of failing.

### Lifecycle of a server

1. **Startup.** `NewModel` creates a session for each saved server, and `Init`
   dials them all. A `--server` that isn't saved opens the sign-in screen for
   it.
2. **Connect.** `onConnected` stores the client and starts `waitForEvent`.
3. **Greeting.** The server's `server` frame arrives as an event, and
   `onGreeting` decides how to sign in:
   - On the sign-in screen, a saved token resumes automatically. Otherwise the
     method chooser opens.
   - In the background, `autoScheme` chooses `token`, or `guest` when the user
     signed in as a guest before. If neither applies, the session is marked
     `needsSignIn` and the sidebar shows **sign in**.
4. **Auth.** `finishAuth` applies `you` and the token to the store and saves
   the server to the config. It then requests `room_list`. A server without
   the `rooms` capability gets a room-less history query instead (`syncLog`).
5. **Rooms.** `onRoomList` applies the list, opens a room, and starts recovery
   (`catchUpRooms`) for any room whose log moved past its checkpoint.
6. **Live.** Notifications go to `onEvent`, which applies them to the store,
   tracks unread messages and mentions, and marks the view dirty.
7. **Disconnect.** `onDisconnect` keeps the store, drops only transient state,
   and reconnects with backoff:
   - An unsolicited `retry_after` sets the delay before the next attempt.
   - An unsolicited `denied`, or a denied background auth, stops automatic
     reconnects until the user acts. Pressing `r` or signing in again
     re-enables them.
8. **Resume.** On reconnect, `room_list` includes the smallest checkpoint as
   `latest_log_id`, so the server returns only rooms that changed. Each changed
   room pages forward from its checkpoint. When several rooms changed and the
   server supports it, they recover in one room-less stream instead (R4).

### The store (`internal/state`)

`state.Store` holds one server's records. It applies the protocol's merge
rules, and the UI only reads the result:

- **Messages** follow the replay rule: the greatest `log_id` wins, and a
  tombstone wins a tie. A snapshot with `prev_room_id` is a move, so the copy
  in the old room is removed. `Room.Order` stays sorted by `message_id`.
- **Notices.** A message without `message_id` is a transient notice
  (§3.5.3). It is buffered in `notices` and never installed as a message.
- **Users** take a complete object (`setUserComplete`) from `auth`, `me`,
  `room_list` and `room_update`, and are merged field by field
  (`mergeCurrentUser`) from `user` notifications. In a merge, a field that is
  present but empty clears the kept value, so the raw JSON is checked to see
  which fields were sent.
- **Reactions** store each user's complete emoji set per message.
  `ReactionTotals` adds them up for display.
- **History cursors.** Each room tracks `FirstLogID`, `MoreBack`, `Loaded`,
  `Loading` and a recovery `Checkpoint`. The combined room-less log has the same
  fields in `Store.Log`.
- **Per-connection state.** Typing, mutes, presence and notices belong to one
  connection. `ResetPresence` clears them at sign-in, and `ResetTransient`
  clears what's needed when a connection is lost.

Log ids are decimal strings. Always compare them with
`protocol.LogIDAfter`, and step them with `NextLogID`/`PrevLogID`. Never
compare them as raw strings.

### Rendering

`View` draws either the sign-in screen (`auth_view.go`) or the chat screen
(`chat_view.go`). The chat screen has a header, a sidebar on the left, the
message viewport with the composer below it, and a bottom bar with key help
and status.

- The sidebar (`refreshRoomItems`, `sidebarView`) lists a header per server,
  then its public rooms, then a **private** subheader with its private rooms.
  Group labels are decoration: `moveRoomCursor` skips them, and `cursorTo`
  never matches them. A private room with exactly two members is a one-to-one
  chat, so `roomName` shows the other member's name instead of the title,
  which may name both people.

- Building message content is expensive, so it only happens when `chat.dirty`
  is set. When a handler changes something that affects the message list, it
  sets the flag. `Update` then calls `renderContent` once at the end.
- `renderContent` records where each message starts (`msgPos`). Selection,
  scrolling and the scroll anchor (which keeps the view steady when older
  history is prepended) all use these line numbers. Every rendered line must
  therefore be exactly one terminal row. Content is wrapped to the viewport
  width minus the selection gutter, and the selection bar sits only in that
  gutter.
- Every fixed row is clipped to the terminal width, and pane heights are
  clamped. A frame taller than the terminal makes Bubble Tea drop the header.
- Markdown goes through glamour, using a fixed dark or light style (`--theme`)
  and never auto-detection. Auto-detection sends an OSC 11 query, which hangs
  some terminals.

## Common changes

**Handle a new notification.** Decode it in `state.Store.ApplyNotification`
and add a `Store.ApplyX` method with a test in `store_test.go`. If the UI must
react (for example by refreshing the sidebar or marking the view dirty), add a
case in `onEvent` (`ui/connection.go`).

**Send a new request.** Use `requestCmd(s, "<tag>", "<method>", params)`. If
the reply needs handling, add a case for the tag in `handleRequestDone`,
decode the result with `decodeResult`, and put the logic in a small `onX`
method.

**Add a slash command.** Add a case in `dispatchCommand`
(`ui/commands.go`). Add its name to `clientCommands` and its help line to
`helpCommands` (`ui/help.go`). Then add it to the README's command table.
Check capabilities with `m.requireCap("…")`, which also writes the status
message.

**Add a key binding.** Handle the key in `handleRoomsKey`,
`handleMessagesKey` or `updateChat` (`ui/chat.go`). Then update `helpKeys`
(`ui/help.go`), `helpLine` (`ui/chat_view.go`), and the README's key table.

**Add a config field.** Add it to `config.Config` or `config.Server`. Only the
current fields are written; legacy fields are migrated in `migrate`.

## Testing

- `internal/protocol`, `internal/config` and `internal/state` are plain unit
  tests.
- `internal/client` tests run against an in-process `httptest` WebSocket
  server.
- `internal/ui` tests build a `Model`, feed it messages through `Update` or
  call handlers directly, and check the model and the rendered `View()`. The
  main helpers are:
  - `testCfg`, `newTestChat`, `newRoomSidebar` (`ui_test.go`)
  - `newTestModel` (`conformance_test.go`)
  - `newTwoServers` (`servers_test.go`)
  - `liveMessage`, `withCommandCap` (`design_test.go`)
  - `serverEvent` (`auth_test.go`)
- `conformance_test.go` is organized by audit items.

Before you commit, run `gofmt -l .`, `go vet ./...` and `go test -race ./...`.
