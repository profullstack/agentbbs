# deploy/mailu — self-hosted mail for `mail.profullstack.com`

Mailu (Postfix + Dovecot + Roundcube + rspamd) as a Docker Compose stack,
fronted by the host Caddy. Full setup, DNS, and architecture: [`docs/mail.md`](../../docs/mail.md).

## Files

| File | Purpose |
|---|---|
| `docker-compose.yml` | the Mailu services (mail ports on host, HTTP on loopback) |
| `mailu.env.example` | config template → copy to `mailu.env` and fill secrets |
| `refresh-certs.sh` | copy Caddy's `mail.$DOMAIN` cert into Mailu, reload (timer) |
| `provision-mailbox.sh` | create a member mailbox / the gateway master user |

`mailu.env`, `certs/`, and `data/` are gitignored (secrets + state).

## Gateway master user

The agentbbs gateway opens any member's mailbox over IMAP with a single secret,
using Dovecot's **master user** feature (login `<name>*<master>`). Enable it with
a Dovecot override so Mailu accepts the `*` separator:

`data/overrides/dovecot/auth-master.conf`:

```
auth_master_user_separator = *
passdb {
  driver = static
  args = nopassword=y
  master = yes
  result_success = continue
}
```

Then create the master account and point agentbbs at it:

```bash
./provision-mailbox.sh --master "$(openssl rand -hex 16)"
# AGENTBBS_MAIL_MASTER_USER=gateway, AGENTBBS_MAIL_MASTER_PASS=<that secret>
```

> The exact master-passdb wiring varies by Mailu version; verify against your
> pinned image before relying on it in production. SMTP submission from the
> gateway uses the trusted local relay (`127.0.0.1:25`), not the master user.

## Ops

```bash
docker compose up -d            # start
docker compose logs -f smtp     # tail Postfix
docker compose exec admin flask mailu config-export   # DKIM keys, etc.
docker compose down             # stop
```
