package mailbox

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"
)

// BotUsage is printed when a bot/agent invokes mail with no/unknown command.
const BotUsage = `agentmail (non-interactive mode) — JSON in, JSON out:

  mailboxes                       list folders
  list [mailbox] [limit]          message summaries (default INBOX)
  read <mailbox> <uid>            full message (marks seen; "peek" 4th arg to keep unseen)
  search <query> [mailbox]        search summaries
  send                            read a JSON Draft on stdin, send it
  reply <mailbox> <uid>           read {"text":...,"replyAll":bool} on stdin
  flag <mailbox> <uid> [on|off]   set/clear the flagged flag
  seen <mailbox> <uid> [on|off]   set/clear the seen flag
  delete <mailbox> <uid>          delete a message

Example:  ssh mail@bbs.profullstack.com list INBOX 20`

// RunBot executes one non-interactive command for agents/bots, writing JSON to
// out. It returns an error (also emitted as {"error":...}) on failure.
func RunBot(ctx context.Context, c *Client, args []string, in io.Reader, out io.Writer) error {
	if len(args) == 0 {
		fmt.Fprintln(out, BotUsage)
		return nil
	}
	enc := json.NewEncoder(out)
	enc.SetIndent("", "  ")
	fail := func(err error) error {
		_ = enc.Encode(map[string]string{"error": err.Error()})
		return err
	}

	switch strings.ToLower(args[0]) {
	case "mailboxes":
		v, err := c.Mailboxes(ctx)
		if err != nil {
			return fail(err)
		}
		return enc.Encode(v)

	case "list", "ls":
		mailbox := Inbox
		if len(args) > 1 {
			mailbox = args[1]
		}
		limit := 0
		if len(args) > 2 {
			limit, _ = strconv.Atoi(args[2])
		}
		v, err := c.List(ctx, mailbox, limit)
		if err != nil {
			return fail(err)
		}
		return enc.Encode(v)

	case "read", "show":
		mailbox, uid, err := mailboxUID(args)
		if err != nil {
			return fail(err)
		}
		peek := len(args) > 3 && strings.EqualFold(args[3], "peek")
		msg, ok, err := c.Read(ctx, mailbox, uid, peek)
		if err != nil {
			return fail(err)
		}
		if !ok {
			return fail(notFound(mailbox, uid))
		}
		return enc.Encode(msg)

	case "search":
		if len(args) < 2 {
			return fail(fmt.Errorf("usage: search <query> [mailbox]"))
		}
		mailbox := ""
		if len(args) > 2 {
			mailbox = args[2]
		}
		v, err := c.Search(ctx, args[1], mailbox, 0)
		if err != nil {
			return fail(err)
		}
		return enc.Encode(v)

	case "send":
		var d Draft
		if err := json.NewDecoder(in).Decode(&d); err != nil {
			return fail(fmt.Errorf("send expects a JSON Draft on stdin: %w", err))
		}
		res, err := c.Send(ctx, d)
		if err != nil {
			return fail(err)
		}
		return enc.Encode(res)

	case "reply":
		mailbox, uid, err := mailboxUID(args)
		if err != nil {
			return fail(err)
		}
		var body struct {
			Text     string `json:"text"`
			ReplyAll bool   `json:"replyAll"`
		}
		if err := json.NewDecoder(in).Decode(&body); err != nil {
			return fail(fmt.Errorf("reply expects {\"text\":...} on stdin: %w", err))
		}
		orig, ok, err := c.Read(ctx, mailbox, uid, true)
		if err != nil {
			return fail(err)
		}
		if !ok {
			return fail(notFound(mailbox, uid))
		}
		res, err := c.Reply(ctx, orig, body.Text, body.ReplyAll)
		if err != nil {
			return fail(err)
		}
		return enc.Encode(res)

	case "flag", "seen":
		mailbox, uid, err := mailboxUID(args)
		if err != nil {
			return fail(err)
		}
		on := true
		if len(args) > 3 {
			on = !strings.EqualFold(args[3], "off") && args[3] != "false" && args[3] != "0"
		}
		if args[0] == "flag" {
			err = c.Flag(ctx, mailbox, uid, on)
		} else {
			err = c.MarkSeen(ctx, mailbox, uid, on)
		}
		if err != nil {
			return fail(err)
		}
		return enc.Encode(map[string]any{"ok": true, "mailbox": mailbox, "uid": uid, args[0]: on})

	case "delete", "rm":
		mailbox, uid, err := mailboxUID(args)
		if err != nil {
			return fail(err)
		}
		if err := c.Delete(ctx, mailbox, uid); err != nil {
			return fail(err)
		}
		return enc.Encode(map[string]any{"ok": true, "deleted": map[string]any{"mailbox": mailbox, "uid": uid}})

	default:
		fmt.Fprintln(out, BotUsage)
		return fmt.Errorf("unknown command: %s", args[0])
	}
}

func mailboxUID(args []string) (string, uint32, error) {
	if len(args) < 3 {
		return "", 0, fmt.Errorf("usage: %s <mailbox> <uid>", args[0])
	}
	uid, err := strconv.ParseUint(args[2], 10, 32)
	if err != nil {
		return "", 0, fmt.Errorf("invalid uid %q", args[2])
	}
	return args[1], uint32(uid), nil
}
