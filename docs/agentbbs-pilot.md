# AgentBBS Operational-Status Pilot

This is an internal-only, opt-in pilot mode for a bounded Helsinki operational-status exchange. It is a separate runtime path, separate SQLite database, and separate SSH host key from normal AgentBBS.

## Current implementation

Set `AGENTBBS_PILOT=1` to enter pilot mode. The process then initializes only:

- an SSH listener (`AGENTBBS_PILOT_ADDR`, default `127.0.0.1:2223`) using an already-provisioned host key (`AGENTBBS_PILOT_HOST_KEY`),
- a dedicated key registry (`AGENTBBS_PILOT_KEYS`), and
- a dedicated status database (`AGENTBBS_PILOT_DB`).

The host-key file must already exist, be an owner-only regular file, and be parseable as an SSH private key. Pilot mode refuses to generate a host key or participant credentials.

The intended initial participant set is **Anikke, Alice, Tom, and Saga**, each with a separately approved SSH public-key fingerprint and explicit audience permissions. No key material or identity approval is created by this source tree.

The process also exposes local operator-only maintenance commands while `AGENTBBS_PILOT=1` is set:

```text
agentbbs pilot-backup <new-destination>
agentbbs pilot-restore <verified-source>
```

Stop the pilot before either command. Backup refuses an existing destination. Restore requires an absent configured destination, checks the SQLite header and `PRAGMA integrity_check`, copies atomically, and leaves the old database untouched until the operator moves it aside. These commands do not transmit data or create credentials.

The listener accepts public-key authentication only. A registry entry binds one exact SSH SHA256 fingerprint to one lowercase name and explicit publish/query audience lists. Duplicate names and fingerprints are rejected, revoked entries are denied, and the username in the SSH request must match the registry name. Registry changes require a process restart in this pinned slice; active sessions are not re-authorized.

The only SSH exec commands are:

```text
ssh alice@pilot-host publish
ssh alice@pilot-host query
```

Each command accepts one bounded JSON object on stdin and returns one JSON object. Publish subjects must equal the authenticated name. Audiences are exact registry capabilities. Status revisions are monotonic per `(audience, subject)`; lower revisions and same-revision conflicting payloads are rejected. `observed_at` and `expires_at` are RFC3339, bounded by freshness/TTL policy, and query responses derive `stale` from the read time rather than trusting stored state.

The default bind is loopback. Explicit non-loopback binds are accepted only for literal `100.64.0.0/10` tailnet addresses, and accepted peers must be in the same tailnet range. This code check is not proof of host firewall policy.

## Example registry shape

Do not copy fingerprints from this example into an operational registry. Provision the exact approved fingerprints through the operator-controlled path and review the file before start:

```json
{
  "keys": [
    {
      "name": "alice",
      "fingerprint": "SHA256:<43-character-base64-sha256>",
      "publish_audiences": ["helsinki"],
      "query_audiences": ["helsinki"],
      "revoked": false
    }
  ]
}
```

## Backup and restore

The status database is a separate SQLite file. Stop the pilot process before an operational copy/restore, preserve mode `0600` and an owner-only parent directory, and verify the copy before replacing the active file. A practical backup from a small operator helper can call `Store.Backup(path)`, which uses SQLite `VACUUM INTO` for a consistent snapshot and refuses an existing destination. Restore is a file replacement only while the pilot is stopped: retain the old database, copy the verified snapshot into the configured database path, apply `0600`/owner-only permissions, start the pilot, and run one authenticated query. Keep the old file until the query succeeds. This source tree does not create a scheduled backup, prove off-host retention, or define a Saga host-owner path.


The tests prove package-level authorization, duplicate/revoked registry handling, exact audience checks, malformed/duplicate JSON rejection, CRLF rejection, request and response bounds, freshness/stale derivation, revision conflicts, rate limiting, private-bind validation, and database restart persistence. They do not prove Saga host ownership, backup/restore procedure, tailnet firewall behavior, source/base-image provenance, rollback of candidate-written data, non-root/read-only deployment isolation, or runtime acceptance on Helsinki infrastructure. No deployment or identity approval is implied by this source change.
