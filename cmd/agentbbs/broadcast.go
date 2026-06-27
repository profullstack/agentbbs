package main

// broadcast sends one announcement to the whole membership over BOTH channels:
//   - inbox: a store-and-forward message in every member's BBS inbox (Members ▸
//            inbox), the same place msg@ delivers to.
//   - email: a plain-text email to every member with a verified address.
//
// It is the explicit "mail all users" action — distinct from the per-member /
// group msg@ route, so a normal group message never fans out to email. It is an
// OPERATOR command (run on the host where the DB + env live); members broadcast
// to inboxes with `ssh msg@host all`, but reaching everyone by email goes through
// here.
//
// Like notify-creds it is a PREVIEW by default (scans + prints what it would do,
// sends nothing) and only acts under --send.
//
//	agentbbs broadcast "We're upgrading the box at 0200 UTC."     # preview
//	agentbbs broadcast --send "Heads up: ..."                     # inbox + email
//	echo "long notice" | agentbbs broadcast --send                # body on stdin
//	agentbbs broadcast --send --no-email "BBS-only notice"        # inbox only
//	agentbbs broadcast --send --no-inbox --subject "Outage" "..." # email only

import (
	"bufio"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/profullstack/agentbbs/internal/mail"
	"github.com/profullstack/agentbbs/internal/store"
)

func broadcastCmd(st store.Store, args []string) {
	fs := flag.NewFlagSet("broadcast", flag.ExitOnError)
	send := fs.Bool("send", false, "actually deliver (default: preview only)")
	noInbox := fs.Bool("no-inbox", false, "skip the BBS inbox channel")
	noEmail := fs.Bool("no-email", false, "skip the email channel")
	from := fs.String("from", env("AGENTBBS_BROADCAST_FROM", "sysop"), "inbox sender name (from_user)")
	subject := fs.String("subject", "", "email subject (default: \"Announcement from <host>\")")
	only := fs.String("user", "", "comma-separated usernames to target (default: all members)")
	fs.Parse(args)

	host := env("AGENTBBS_HOST", "bbs.profullstack.com")
	if strings.TrimSpace(*subject) == "" {
		*subject = "Announcement from " + host
	}

	// Body: remaining args joined, else stdin.
	body := strings.TrimSpace(strings.Join(fs.Args(), " "))
	if body == "" {
		b, _ := io.ReadAll(bufio.NewReader(os.Stdin))
		body = strings.TrimSpace(string(b))
	}
	if body == "" {
		fmt.Fprintln(os.Stderr, "empty message — nothing to broadcast (pass text as args or on stdin)")
		os.Exit(2)
	}

	doInbox, doEmail := !*noInbox, !*noEmail
	if !doInbox && !doEmail {
		fmt.Fprintln(os.Stderr, "both channels disabled — nothing to do")
		os.Exit(2)
	}

	// Optional username allow-list.
	var want map[string]bool
	if strings.TrimSpace(*only) != "" {
		want = map[string]bool{}
		for _, n := range strings.Split(*only, ",") {
			if n = strings.ToLower(strings.TrimSpace(n)); n != "" {
				want[n] = true
			}
		}
	}

	users, err := st.ListUsers(100000)
	if err != nil {
		fmt.Fprintln(os.Stderr, "list users:", err)
		os.Exit(1)
	}

	smtp := mail.ConfigFromEnv()
	if doEmail && *send && !smtp.Configured() {
		fmt.Fprintln(os.Stderr, "error: SMTP not configured (AGENTBBS_SMTP_HOST/_FROM) — cannot send email "+
			"(use --no-email for an inbox-only broadcast)")
		os.Exit(1)
	}

	if !*send {
		fmt.Println("PREVIEW (nothing delivered) — re-run with --send to broadcast")
	}
	fmt.Printf("from %q · subject %q · channels: %s\n", *from, *subject, channelLabel(doInbox, doEmail))

	// Collect the inbox recipient list (all non-banned members in scope), and
	// count/send email per verified member.
	var inboxNames []string
	var emailTargets, emailOK, emailErr int
	for _, u := range users {
		if u.Banned {
			continue
		}
		if want != nil && !want[strings.ToLower(u.Name)] {
			continue
		}

		if doInbox {
			inboxNames = append(inboxNames, u.Name)
		}

		if doEmail {
			if !u.EmailVerified || u.Email == "" {
				continue
			}
			emailTargets++
			if !*send {
				fmt.Printf("  [email] %-20s -> %s\n", u.Name, u.Email)
			} else if err := smtp.Send(u.Email, *subject, broadcastEmailBody(u.Name, body, host)); err != nil {
				emailErr++
				fmt.Fprintf(os.Stderr, "  [email] %s: %v\n", u.Name, err)
			} else {
				emailOK++
			}
		}
	}

	var inboxSent int
	if doInbox {
		if !*send {
			fmt.Printf("  [inbox] would deliver to %d member(s)\n", len(inboxNames))
		} else if inboxSent, err = st.SendMessageMulti(*from, inboxNames, body); err != nil {
			fmt.Fprintln(os.Stderr, "  [inbox] send:", err)
			os.Exit(1)
		}
	}

	fmt.Println()
	if *send {
		if doInbox {
			fmt.Printf("inbox: %d delivered\n", inboxSent)
		}
		if doEmail {
			fmt.Printf("email: %d sent, %d failed (of %d verified)\n", emailOK, emailErr, emailTargets)
		}
		if emailErr > 0 {
			os.Exit(1)
		}
	} else {
		fmt.Printf("%d member(s) in scope.\n", len(inboxNames))
	}
}

func channelLabel(inbox, email bool) string {
	switch {
	case inbox && email:
		return "inbox + email"
	case inbox:
		return "inbox only"
	default:
		return "email only"
	}
}

func broadcastEmailBody(name, body, host string) string {
	return "Hi " + name + ",\n\n" +
		body + "\n\n" +
		"— " + host + "\n\n" +
		"(This is a broadcast to all members. Reply in the BBS: ssh msg@" + host + " sysop <your reply>.)\n"
}
