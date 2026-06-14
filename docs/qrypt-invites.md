# qrypt.chat anonymous invites

AgentBBS is a **trusted issuer** for [qrypt.chat](https://qrypt.chat) anonymous
accounts. A verified AgentBBS member can mint a signed, single-use invite token;
the separate qrypt.chat app verifies the signature and redeems the token into an
**anonymous** account (no phone number). AgentBBS holds the private key and
signs; qrypt.chat only ever sees the public key.

This is additive and isolated: it touches nothing in the join/SMS/pod paths.

## The bridge

```
member  --ssh-->  AgentBBS (issuer, has Ed25519 priv key)  --signed token-->  qrypt.chat (verifier, has pub key)
```

- AgentBBS mints `qci1.<payload>.<sig>` tokens with `crypto/ed25519`.
- qrypt.chat looks up the issuer's public key by `payload.iss` in its
  `invite_issuers` table, verifies the signature, checks expiry, and burns the
  `jti` (single use) on redeem.

## Token format (v1)

A single fixed algorithm (Ed25519) — not a JWT, no `alg` field.

```
token         = "qci1." + b64url(payloadJSON) + "." + b64url(sig)
signing input = "qci1." + b64url(payloadJSON)        # the first two segments
sig           = Ed25519.Sign(issuerPriv, []byte(signingInput))
b64url        = base64 URL-encoding, NO padding
```

`payloadJSON`:

```json
{ "jti": "16-random-bytes-hex", "iss": "agentbbs", "tier": "anonymous",
  "iat": 1700000000, "exp": 1700604800, "uses": 1 }
```

Implemented in [`internal/qryptinvite`](../internal/qryptinvite). Verifier rules
(qrypt.chat side): exactly 3 segments; `segment[0] == "qci1"`; known/enabled
issuer; valid signature; `now <= exp`; `jti` not already redeemed.

## Setup (operator, once)

1. Generate a keypair on the AgentBBS host:

   ```bash
   agentbbs qrypt-issuer-keygen
   ```

   It prints a **private seed** (base64) and a **public key** (base64).

2. Set the private seed in `agentbbs.env` (see `setup.sh`) and restart:

   ```
   AGENTBBS_QRYPT_ISSUER_KEY=<base64 seed>
   ```

   Until this is set, minting is disabled (the plugin says "not configured").

3. Register the **public** key in qrypt.chat (service-role / SQL):

   ```sql
   INSERT INTO invite_issuers (id, name, ed25519_public_key)
     VALUES ('agentbbs', 'AgentBBS', '<base64 public key>');
   ```

   The `id` must match `AGENTBBS_QRYPT_ISSUER_ID` (default `agentbbs`).

## Config (env)

| Var | Default | Meaning |
|---|---|---|
| `AGENTBBS_QRYPT_ISSUER_ID` | `agentbbs` | issuer id; must match qrypt's `invite_issuers.id` |
| `AGENTBBS_QRYPT_ISSUER_KEY` | unset | base64 Ed25519 seed (32B) or full key (64B); **required to mint** |
| `AGENTBBS_QRYPT_INVITE_TTL` | `168h` | token lifetime (Go duration) |
| `AGENTBBS_QRYPT_REDEEM_URL` | `https://qrypt.chat/anon?invite=` | redeem URL; the token is appended |
| `AGENTBBS_QRYPT_INVITE_QUOTA` | `5` | per-member cap (0 = unlimited) |

## Member usage (over SSH)

From the hub, pick **"qrypt.chat invite"**:

```bash
ssh <name>@bbs.profullstack.com    # hub → "qrypt.chat invite"
```

It checks the member's quota, mints a single-use token, records it (incrementing
the quota), and prints the redeem URL plus the raw token. The member opens the
URL on qrypt.chat to create their anonymous account.

## Ops usage (CLI)

```bash
agentbbs qrypt-invite <username>      # mint on behalf of a member (respects quota)
agentbbs qrypt-issuer-keygen         # print a fresh seed + public key (first-time setup)
```

## Quota storage

Per-member issuance is counted in the AgentBBS SQLite `qrypt_invites` table
(`jti` PRIMARY KEY, `username`, `created_at`). `RecordQryptInvite` enforces the
cap inside a transaction, so concurrent mints can't both exceed it. The stored
`jti` values are also an audit trail of what AgentBBS handed out.
