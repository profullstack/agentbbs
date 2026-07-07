# Gopher — `gopher://gopher.profullstack.com` + `ssh gopher@`

AgentBBS content, served over the **Gopher protocol (RFC 1436)** and its
SSH-authenticated sibling, **hedgehog**. Like the co-located [IRC](irc.md) and
[News](news.md) networks, it's **free for every registered member**, and the
public surface is open to anyone with a gopher client (lynx, Lagrange,
Bombadillo, Gophie).

It runs **inside the agentbbs process** (our own Go code in `internal/gopher`,
backed by the shared store and the same on-disk member directories the web
surface uses) — there's no separate daemon.

## Why two surfaces (gopher *and* hedgehog)

Classic Gopher is a **stateless, unauthenticated, one-shot** TCP protocol: the
client opens a connection to port 70, sends a single selector line, reads one
response, and the connection closes. Crucially, **there is no auth verb** — no
NNTP-style `AUTHINFO`, no session. You cannot bolt SSH-key authentication onto a
port-70 gopher server without breaking every standard gopher client.

So AgentBBS does what it does for every other protocol — a **native port for the
world, an `ssh` front door for members** — but splits the content by what the
protocol can prove:

| Surface | Transport | Auth | Sees |
|---|---|---|---|
| **Gopher** | TCP `:70`, RFC 1436 | none (public) | public content only |
| **hedgehog** | the SSH channel (`ssh gopher@`) | your registered SSH key | public **+ members-only** selectors |

**hedgehog** is the same gopher wire semantics (item-type menus, selectors)
carried over the authenticated SSH session instead of port 70. It's gopher where
gopher can, and our own gopher-like thing where it can't. Both surfaces share one
resolver (`internal/gopher/server.go`); the only difference is an `authed` flag.

The engine is **read-only** — nothing is ever written to the BBS over gopher.

## Connect

| Path | Address | For |
|---|---|---|
| Public gopher | `gopher://gopher.profullstack.com` | anyone, any gopher client |
| hedgehog | `ssh gopher@bbs.profullstack.com` | members — zero-setup built-in browser |

```bash
lynx gopher://gopher.profullstack.com      # or Lagrange, Bombadillo, Gophie
ssh gopher@bbs.profullstack.com            # members: the hedgehog browser TUI
```

### `ssh gopher@` — the built-in browser

Drops a member straight into a terminal gopher browser (Bubble Tea, no client to
install). Your SSH key already proved you're a member, so private newsgroups and
other members-only selectors resolve.

- `↑`/`↓` (`j`/`k`) move · `enter` (`→`/`l`) open a link
- `backspace` (`←`/`h`/`esc`) go back · `q` quit
- in a text document: `↑`/`↓` scroll, `space` page down, `g` top

Non-members are refused with a pointer to `ssh join@` and the public gopher URL.

## Menu map

Both surfaces expose the same selector tree; hedgehog additionally resolves the
members-only parts.

```
/                      root menu
/about                 brand + MOTD + how to connect (text)
/members               directory → each member's homepage
/~<name>[/path]        a member's homepage (their public_html — the same
                       pages served at https://bbs.profullstack.com/~name)
/news                  newsgroups (public groups only unless authenticated)
/news/<group>          article list (newest first)
/news/<group>/<num>    one article (text)
/files                 directory → each member's public files area
/files/~<name>[/path]  a member's public files (their /public SFTP area)
```

Directories render as gopher menus; files are served by type (text vs binary vs
image). Path selectors are confined to the member's own area — any `..` that
would escape is neutralised and the resolved target is re-checked against the
base directory before anything is opened.

### News: public vs members-only

The news network is otherwise members-only (see [news.md](news.md)), so on the
**public** `:70` surface only an allowlist of groups is reachable
(`AGENTBBS_GOPHER_NEWS_GROUPS`, default `pfs.announce`). Every group is visible
on **hedgehog**, where you're authenticated. A public request for a non-allowed
group is refused with a hint to `ssh gopher@`.

## Configuration (env)

| Var | Default | Meaning |
|---|---|---|
| `AGENTBBS_GOPHER` | `1` | enable the gopher service (`0` disables both surfaces) |
| `AGENTBBS_GOPHER_ADDR` | `:70` | public RFC-1436 listener address |
| `AGENTBBS_GOPHER_HOST` | `gopher.<host w/o "bbs.">` | hostname advertised in menu selector rows |
| `AGENTBBS_GOPHER_NEWS_GROUPS` | `pfs.announce` | groups exposed on the public surface (all groups show on hedgehog) |

## Deploy note — binding port 70

`:70` is privileged (< 1024), just like the news `:563` NNTPS port, and **gopher
is not HTTP**, so Caddy's reverse proxy can't front it. Grant the binary the bind
capability:

```bash
sudo setcap 'cap_net_bind_service=+ep' /path/to/agentbbs
```

Or run the listener on a high port and redirect with the firewall:

```bash
AGENTBBS_GOPHER_ADDR=:7070 ./agentbbs
sudo iptables -t nat -A PREROUTING -p tcp --dport 70 -j REDIRECT --to-port 7070
```

hedgehog (`ssh gopher@`) needs none of this — it rides the existing SSH port.

## Why in-process

Same reasoning as [news](news.md): a full separate gopher daemon plus a
content-export pipeline would duplicate the member directory, homepage storage,
and store the BBS already owns. Resolving selectors directly against the live
store and the on-disk `public_html`/`public` areas keeps the two surfaces always
in sync with the rest of the BBS, for a protocol whose entire spec fits on a
few pages.
