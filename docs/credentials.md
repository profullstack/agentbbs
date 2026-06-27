# Member credentials — git accounts, mailboxes & the `notify-creds` backfill

Every **verified** AgentBBS member gets, for free:

- a **git account** on `git.profullstack.com` (the self-hosted Forgejo backing
  AgentGit) — BBS membership *is* the git account, and
- a **mailbox** at `<name>@mail.profullstack.com` (self-hosted Mailu — see
  [`mail.md`](mail.md)).

This doc covers how those credentials are delivered by email, and the
`agentbbs notify-creds` ops command that (re)sends them.

## Git accounts (automatic)

When a member confirms their email, `provisionGit` (`cmd/agentbbs/main.go`):

1. **Creates** their Forgejo account (`forgejo.EnsureUser`) with a generated
   one-time password (`must_change_password`), if it doesn't exist.
2. **Registers** the SSH key they signed in with (`forgejo.EnsureKey`) so they
   push with the same key — no git password.
3. **Emails** them the web sign-in link, username, and the one-time password
   (`gitWelcomeEmailBody`) via the transactional SMTP relay.

It's idempotent and best-effort: failures are logged, never blocking BBS
verification, and it's a no-op when Forgejo is unconfigured. It runs on email
verification (`join@` and the web `/verify` link) and again, asynchronously, on
each BBS login so an existing member's key is kept in sync.

## `passwd@` — self-service "reset my password everywhere"

A member who forgot their password (or just wants to rotate it) runs:

```bash
ssh passwd@bbs.profullstack.com         # interactive: type a new password twice
ssh passwd@bbs.profullstack.com < pw    # non-interactive: read it from stdin
echo | ssh passwd@bbs.profullstack.com  # empty/no PTY: a strong one is generated for you
```

The route is **gated by the caller's registered SSH key**, so it doubles as the
forgot-password path — no old password is required (the key *is* the proof of
identity). `password@` is an alias. Whatever the member enters is applied as **one
password across every service that has its own credential**:

| Service | How it's set | Notes |
|---|---|---|
| **git** (Forgejo) | admin API — ensure the account, then `SetPassword` (clears `must_change_password`) | git **push** uses the SSH key, not this password; this is for the web UI |
| **mail** (Mailu webmail) | admin API — ensure the mailbox, then `mailu.SetPassword` | the mailbox/IMAP/webmail login |
| **chat** (IRC + The Lounge) | the privileged helper `set-irc-password.sh` via a narrow `sudo` rule | sets all THREE chat credentials to the new password (see below); see [`irc.md`](irc.md) |

BBS/SSH login itself is unaffected — that's always the member's key.

**Chat has three credentials, all set to the new password.** "Chat" spans Ergo
(the IRC server) and The Lounge (the web client at `chat.<domain>`), which between
them keep *three* secrets — `set-irc-password.sh` sets all three so one password
works everywhere:

1. **Ergo SASL** — the pbkdf2 hash in `/var/lib/ergo/irc-passwd` that native IRC
   clients (irssi/HexChat) authenticate with.
2. **The Lounge `saslPassword`** — how the *web* client logs in to Ergo on the
   member's behalf (in the user's JSON `networks[]`).
3. **The Lounge web-login password** — the bcrypt field used to sign in to
   `chat.<domain>` *itself*, set via `thelounge reset <member>`
   (`AGENTBBS_LOUNGE_RESET_CMD`). Missing this was the "I reset my password but
   chat.profullstack.com says auth failed" bug: a member could reach IRC but not
   the web client.

**Why chat needs a helper.** The BBS process runs as the unprivileged `agentbbs`
service user, but the Ergo password store (`ergo:ergo 0600`) and The Lounge user
files are root-owned. `setup.sh` installs `scripts/set-irc-password.sh` to
`/usr/local/sbin/agentbbs-set-irc-password` and a `/etc/sudoers.d/agentbbs-ircpass`
rule letting **only** that one command run as root. The new password travels on
**stdin** (the `set-irc-password.sh <member> -` form, and likewise piped to
`thelounge reset`), so it never appears in the process table or sudo's command log. Each leg is
independent: if one service is unconfigured or fails, the others still apply and
the member sees a per-service ✓/✗ summary. A confirmation email (which never
contains the password) is sent on success.

## `notify-creds` — backfill / re-send (ops)

The git- and mailbox-credential emails were added after some accounts already
existed, so `notify-creds` lets the operator send them to members who never
received them. Run it on the host where the DB and env live.

```bash
agentbbs notify-creds                 # PREVIEW for all verified members (sends nothing)
agentbbs notify-creds --send          # really send git + mailbox to everyone verified
agentbbs notify-creds --git --send    # git creds only
agentbbs notify-creds --mail --send   # mailbox creds only
agentbbs notify-creds --user alice,bob --send
```

| Flag | Effect |
|---|---|
| *(none)* | **Preview only** — scans and prints intended actions; resets no passwords, sends no mail. |
| `--send` | Actually reset passwords, ensure aliases, and send email. |
| `--git` | Include git creds (default: both when neither `--git`/`--mail` is given). |
| `--mail` | Include mailbox creds. |
| `--user a,b` | Restrict to a comma-separated allow-list (default: all verified). |
| `--limit N` | Max accounts to scan (default 100000). |

What `--send` does per verified member (banned / unverified / no-email are
skipped):

- **git** — `forgejo.EnsureUserReset`: creates the account if missing, otherwise
  **resets it to a fresh one-time password** (the original is not recoverable),
  then emails the login link + username + password. The reset clobbers any
  password a member set themselves, which is why it only runs under `--send`.
- **mail** — `forwardemail.CreateAlias` to ensure the `<name>@<domain>` alias,
  then emails the address + webmail link.

Safety rails: it **refuses `--send` when SMTP is unconfigured**, and
**warns-and-skips** the git or mail channel when Forgejo / forwardemail are
unconfigured. It prints a per-channel `sent / failed` summary and exits non-zero
on any failure.

> The `--mail` path uses the **forwardemail.net** alias API
> (`AGENTBBS_FORWARDEMAIL_*`), which is independent of the self-hosted Mailu
> mailbox stack in [`mail.md`](mail.md). Use whichever your deployment has wired.

## Required env

| Var | Default | For |
|---|---|---|
| `AGENTBBS_FORGEJO_URL` | unset | git — Forgejo base URL, e.g. `https://git.profullstack.com` |
| `AGENTBBS_FORGEJO_ADMIN_TOKEN` | unset | git — Forgejo admin token (create/reset users + keys) |
| `AGENTBBS_FORWARDEMAIL_API_KEY` | unset | mail — forwardemail.net API key |
| `AGENTBBS_FORWARDEMAIL_DOMAIN` | `AGENTBBS_MAIL_DOMAIN` | mail — alias domain (falls back to the mail domain, default `mail.profullstack.com`) |
| `AGENTBBS_WEBMAIL_URL` | unset | mail — webmail link put in the email (optional) |
| `AGENTBBS_SET_IRC_PASSWD` | unset (set by `setup.sh` when IRC is on) | chat — path to the privileged `set-irc-password.sh` helper for `passwd@`; empty disables the chat leg |
| `AGENTBBS_SET_IRC_SUDO` | `1` | chat — invoke the helper via `sudo` (set `0` if the BBS already runs as root, e.g. in tests) |
| `AGENTBBS_SMTP_HOST` / `_FROM` | unset | **sending** all of the above emails (required to actually send) |
| `AGENTBBS_SMTP_PORT` / `_USER` / `_PASS` | `587` / unset / unset | SMTP submission (STARTTLS) |

## Two SMTP paths (and why one is `:25`)

AgentBBS has **two different SMTP configs** — don't confuse them:

| Config | Default | Role |
|---|---|---|
| `AGENTBBS_SMTP_*` (`internal/mail`) | port **`587`** (STARTTLS) | **Transactional sender** — `join@` confirmation codes and all `notify-creds` emails. This is an authenticated *submission* port. **Implicit-TLS `465` is not supported** by this code path (it negotiates STARTTLS); use 587 or another STARTTLS port. |
| `AGENTBBS_MAIL_SMTP_ADDR` (`internal/mailbox`) | **`127.0.0.1:25`** | **Gateway relay** — how the in-BBS `Mail` client injects members' outbound mail into the **on-box Mailu/Postfix MTA**. `:25` is correct here: it's a trusted *loopback* hand-off to the local MTA, not a remote authenticated client. 465/587 are submission ports for *remote* clients and don't apply. |

So a `127.0.0.1:25` in the mail config is intentional, not a bug. (And there is
no SMTP port `467` — the implicit-TLS submission port is `465`.) See
[`mail.md`](mail.md) for the full Mailu stack.
