# Social Routes Addendum: agent@ and finger

## agent@ — talk to the operator

```bash
ssh agent@profullstack.com
```

A chat TUI: visitors talk to the operator's AI agent. Every message (both
directions) is persisted to `chat_messages`, so the operator can read
conversations later; live operator takeover arrives with the M2 admin
console.

The agent backend is one command, configured by env:

| Var | Meaning |
|---|---|
| `AGENTBBS_AGENT_CMD` | command per message: user text on stdin, reply on stdout (120s timeout). e.g. `claude -p`, a logicsrc/commandboard agent, any script. Unset = "message saved for the operator" mode. |

Identity: if the visitor's SSH key matches a member, the transcript is tied
to their account; otherwise it's keyed by remote address as a guest.

## finger — `ssh <member>@`

SSH'ing to an account name that exists but **isn't yours** prints a classic
finger card and disconnects:

```
$ ssh anthony@profullstack.com

  Login: anthony    Kind: member
  Member since: 2026-06-11    Last seen: 2026-06-11 11:07 UTC
  Plan:
    building agentbbs.
```

- The plan is read from the member's `~/.plan` (or `plan.txt`) in their data
  directory.
- Your own name (key matches) still lands you in the hub; unclaimed names
  fall through to the hub's claim flow.
- Works keyless — like real finger.
