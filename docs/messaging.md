# Member messaging — direct, group & broadcast

AgentBBS members leave each other store-and-forward notes (the `messages` table).
Recipients read them in the hub's **Members ▸ inbox**; an unread badge shows on
login. There are two delivery channels:

- **BBS inbox** — in-BBS, members-only. Any member can message one member, a
  hand-picked group, or everyone. This is the default.
- **Email** — reaches members in their actual mailbox. This is a separate,
  explicit operator action (`agentbbs broadcast`), so an ordinary group message
  never fans out to email.

## Inbox messaging — the `msg@` route

```bash
ssh msg@bbs.profullstack.com bob hi there          # one member
ssh msg@bbs.profullstack.com alice,bob,carol hi    # a group (comma-separated, no spaces)
ssh msg@bbs.profullstack.com all heads up, all     # broadcast to every member
echo "long note" | ssh msg@bbs.profullstack.com all   # body from stdin
```

The first argument is the **recipient spec**; the rest is the message (or stdin
when omitted). The spec is either a comma-separated list of member names, or one
of `all` / `*` / `everyone` / `@all` to reach everyone. The sender, unknown
names, duplicates, and banned accounts are handled for you: you can't message
yourself, an unknown name aborts the send with a hint, and a broadcast skips
banned members. Requires your registered SSH key (members only).

## Inbox messaging — the hub TUI

In **Members** (the in-hub directory):

| Key | Action |
|---|---|
| `↑/↓` | move the cursor |
| `space` | select / deselect the member under the cursor |
| `a` | select all (press again to clear) |
| `m` | message — the selected group if any, else the member under the cursor |
| `enter` | finger (view) the member |
| `i` | open your inbox |
| `q` | back |

Selected rows show `[x]`; a "`N selected`" line appears once a group is started.
`m` opens a one-line composer whose header names the audience (e.g. `3 members`);
`enter` sends to everyone in the group at once, `esc` cancels. The selection
clears after a successful send.

All inbox writes for a group/broadcast happen in **one transaction**
(`store.SendMessageMulti`), so a partial failure never leaves some inboxes
written and others not.

## Email broadcast — `agentbbs broadcast` (operator)

Reaching every member by **email** (e.g. a maintenance notice) is an operator
command run on the host where the DB and SMTP env live. Like `notify-creds` it is
a **preview by default** and only delivers under `--send`.

```bash
agentbbs broadcast "We're upgrading the box at 0200 UTC."      # PREVIEW (sends nothing)
agentbbs broadcast --send "Heads up: brief downtime tonight."  # inbox + email to all
echo "long notice" | agentbbs broadcast --send                 # body on stdin
agentbbs broadcast --send --no-email "BBS-only notice"         # inbox channel only
agentbbs broadcast --send --no-inbox --subject "Outage" "..."  # email channel only
agentbbs broadcast --send --user alice,bob "targeted note"     # only these members
```

| Flag | Effect |
|---|---|
| *(none)* | **Preview** — prints intended deliveries, sends nothing. |
| `--send` | Actually deliver. |
| `--no-inbox` | Skip the BBS inbox channel. |
| `--no-email` | Skip the email channel (inbox-only broadcast). |
| `--from NAME` | Inbox sender name (default `sysop`, or `AGENTBBS_BROADCAST_FROM`). |
| `--subject S` | Email subject (default `Announcement from <host>`). |
| `--user a,b` | Restrict to a comma-separated allow-list (default: all members). |

The **inbox** channel reaches every non-banned member in scope; the **email**
channel reaches only members with a **verified** address (others are skipped).
`--send` refuses the email channel when SMTP is unconfigured
(`AGENTBBS_SMTP_HOST`/`_FROM`) — use `--no-email` for an inbox-only blast. It
prints a per-channel delivered/failed summary and exits non-zero on any email
failure. The email footer tells members how to reply in-BBS
(`ssh msg@<host> sysop <reply>`).
