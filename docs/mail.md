# Mail — self-hosted Mailu at `mail.profullstack.com`

AgentBBS gives **Founding Lifetime (paid) members** a real mailbox at
`<name>@mail.profullstack.com`, reached two ways:

- **Webmail** — `https://mail.profullstack.com` (Roundcube), the only
  member-facing mail surface.
- **AgentMail** — the in-BBS client (`internal/mailbox`): the `Mail` hub entry
  or `ssh mail@bbs.profullstack.com` (a TUI for humans, a JSON bot mode for
  agents). It connects to this stack.

The apex `profullstack.com` is **reserved for corporate mail** and is not served
here — member mail lives only on the `mail.` subdomain.

## Architecture

The host already runs **Caddy** (owns `:80`/`:443`) and the **agentbbs** process.
Mailu (Postfix + Dovecot + Roundcube + rspamd) runs as a Docker Compose stack:

- Mailu owns the **mail ports** on the host: `25, 465, 587, 993, 995`.
- Mailu's HTTP front is bound to **loopback** (`127.0.0.1:8080`); **Caddy**
  reverse-proxies `https://mail.profullstack.com` to it (webmail + admin).
- **TLS:** `TLS_FLAVOR=mail` — Mailu does *not* run its own ACME (Caddy is the
  only ACME client). Caddy obtains the `mail.profullstack.com` cert from its site
  block; [`deploy/mailu/refresh-certs.sh`](../deploy/mailu/refresh-certs.sh)
  copies it into Mailu and reloads it on renewal — the same pattern as the
  Ergo/IRC and NNTP cert refreshers.
- The **agentbbs gateway** reads/sends on behalf of members: IMAP via a Dovecot
  **master user** (one secret opens any mailbox), SMTP via the co-located relay
  on `127.0.0.1:25`. Members therefore never manage an IMAP/SMTP password.

```
            ┌─────────── Caddy (:443) ───────────┐
 webmail →  │ mail.profullstack.com → 127.0.0.1:8080 (Mailu front, HTTP)
            └───────────────┬─────────────────────┘
                            │ copies LE cert (refresh-certs.sh)
 clients →  Mailu front (:25 :465 :587 :993 :995)  ──→ Postfix / Dovecot / rspamd
                            ▲
 agentbbs ──IMAP 993 (master user)──┘   ──SMTP 127.0.0.1:25 (local relay)──▶
```

## DNS

`mail.profullstack.com` and `smtp.profullstack.com` A records are added. Also set:

| Type | Host | Value |
|---|---|---|
| A | `mail.profullstack.com` | host IP |
| A | `smtp.profullstack.com` | host IP |
| MX | `mail.profullstack.com` | `10 mail.profullstack.com.` |
| TXT (SPF) | `mail.profullstack.com` | `v=spf1 mx -all` |
| TXT (DMARC) | `_dmarc.mail.profullstack.com` | `v=DMARC1; p=quarantine; rua=mailto:postmaster@mail.profullstack.com` |
| TXT (DKIM) | `dkim._domainkey.mail.profullstack.com` | from `flask mailu config-export` after first boot |
| PTR | host IP | `mail.profullstack.com` (set at your VPS provider) |

> **Port 25 / deliverability:** many cloud providers block outbound `:25` by
> default — request an unblock, set the PTR/rDNS, and warm the IP, or relay
> outbound through a smarthost. Inbound MX and the gateway's local submission
> work regardless.

## Install

```bash
cd /opt/agentbbs/deploy/mailu
cp mailu.env.example mailu.env      # fill SECRET_KEY, INITIAL_ADMIN_PW, etc.
docker compose up -d
# seed the gateway master user + (optionally) backfill member mailboxes:
AGENTBBS_MAIL_MASTER_USER=gateway ./provision-mailbox.sh --master "$(openssl rand -hex 16)"
```

Add the Caddy site (setup.sh writes this when `MAIL=1`):

```
mail.profullstack.com {
    encode zstd gzip
    reverse_proxy 127.0.0.1:8080
}
```

Then install the cert refresher on a timer (setup.sh does this too):

```bash
install -m 0755 deploy/mailu/refresh-certs.sh /usr/local/bin/agentbbs-mailu-certs
# systemd timer runs it every ~12h; first run swaps in the real cert once Caddy issues it.
```

## agentbbs gateway env

Set these on the agentbbs service so the `Mail` hub entry / `ssh mail@` work:

| Var | Value |
|---|---|
| `AGENTBBS_MAIL_DOMAIN` | `mail.profullstack.com` |
| `AGENTBBS_MAIL_IMAP_ADDR` | `mail.profullstack.com:993` |
| `AGENTBBS_MAIL_SMTP_ADDR` | `127.0.0.1:25` |
| `AGENTBBS_MAIL_MASTER_USER` | `gateway` |
| `AGENTBBS_MAIL_MASTER_PASS` | the master password set above |

## Provisioning member mailboxes

A mailbox must exist before the gateway can open it. Provision when a member
becomes paid (or backfill):

```bash
deploy/mailu/provision-mailbox.sh alice      # creates alice@mail.profullstack.com
```

The Dovecot **master user** (`gateway`) then authenticates as any member with
the login form `alice*gateway` + the master password — which is exactly what
`internal/mailbox`'s IMAP adapter sends. See
[`deploy/mailu/README.md`](../deploy/mailu/README.md) for the master-user
override and operational details.

## Webmail only for members

Members are pointed at `https://mail.profullstack.com` (Roundcube) and the BBS
`Mail` client — they are not given the Mailu admin UI or alias management. Admin
is operator-only.
