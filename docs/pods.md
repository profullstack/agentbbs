# Pods Addendum (PRD v0.1 → v0.2)

Added after the initial PRD draft: a personal Linux pod product alongside the
BBS, with SSH-username routing and a paid membership.

## SSH routes

| Command | What happens |
|---|---|
| `ssh bbs@profullstack.com` | BBS hub as a guest (aliases: `play@`, `guest@`) |
| `ssh <name>@profullstack.com` | BBS hub as a member/agent (SSH key required) |
| `ssh join@profullstack.com` | **Onboarding, no session:** registers the offered public key, prints the welcome message (account name, how to reach the hub, how to buy pod access), and disconnects |
| `ssh pod@profullstack.com` | Personal Linux pod — **paid members only** |

## The pod

A user's own container where they can run what they like — *without root on
the host OS*.

- **Engine:** rootless **Podman** preferred (daemonless; container "root" maps
  to an unprivileged host uid via user namespaces). Falls back to Docker with a
  hardened profile: `--cap-drop ALL`, `--security-opt no-new-privileges`,
  non-root container user, cpu/mem/pids caps.
- **Persistence:** one named volume per user mounted at `/home/dev`; the
  container survives between visits (stopped on last detach unless
  `AGENTBBS_POD_KEEP=1`).
- **Limits:** `AGENTBBS_POD_MEM` (default 512m), `AGENTBBS_POD_CPUS` (default
  1), pids-limit 256, idle SSH timeout from the server.
- **Identity:** the SSH key fingerprint is the account; `pod@` looks the key up
  and refuses unregistered keys with a pointer to `join@`.

## Membership & CoinPay

Pod access costs **$1/mo**, paid via the **CoinPay CLI** (the default LogicSRC
payment plugin).

Flow:

1. `ssh join@profullstack.com` → account is created from the SSH key; the
   message includes a unique payment reference and the exact command:
   `coinpay pay --to profullstack --amount 1 --currency USDC --memo <ref>`
2. User pays with the coinpay CLI.
3. `ssh pod@profullstack.com` → the server checks the subscription
   (`pod_subscriptions.paid_until`); if unpaid it attempts one CoinPay
   verification, then either admits or prints payment instructions and
   disconnects.

Integration knobs (so the deployed CoinPay surface can evolve without a
rebuild):

- `AGENTBBS_COINPAY_PAY_TMPL` — pay-command template shown to users
  (`%s` = payment reference).
- `AGENTBBS_COINPAY_VERIFY_CMD` — verifier command template; exit 0 = paid.
- `agentbbs grant-pod <user> <months>` — manual/ops grant path.

The payment reference is HMAC-derived from the user's key fingerprint, so
CoinPay memos reconcile to accounts without storing payment details.
