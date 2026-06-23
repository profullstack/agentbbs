package main

// notify-creds re-emails verified members their account credentials and links:
//   - git:  their git.profullstack.com (Forgejo/AgentGit) web login URL, username,
//           and a freshly reset one-time password (must change on first sign-in).
//   - mail: their <name>@<mail-domain> mailbox address and the webmail link, after
//           ensuring the forwardemail alias exists.
//
// Both features were added after some accounts already existed, so this lets the
// operator backfill notifications to everyone who never received them.
//
// It is a PREVIEW by default — it scans and prints what it would do without
// touching Forgejo, forwardemail, or sending any email. Pass --send to execute.
// Resetting git passwords clobbers any password a member set themselves, which is
// why it only runs under --send.
//
//	agentbbs notify-creds                      # preview for all verified members
//	agentbbs notify-creds --send               # really send git + mail to everyone
//	agentbbs notify-creds --git --send         # git creds only
//	agentbbs notify-creds --mail --send        # mailbox creds only
//	agentbbs notify-creds --user alice,bob --send

import (
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/profullstack/agentbbs/internal/forgejo"
	"github.com/profullstack/agentbbs/internal/forwardemail"
	"github.com/profullstack/agentbbs/internal/mail"
	"github.com/profullstack/agentbbs/internal/store"
)

func notifyCreds(st store.Store, args []string) {
	fs := flag.NewFlagSet("notify-creds", flag.ExitOnError)
	send := fs.Bool("send", false, "actually reset passwords and send email (default: preview only)")
	gitFlag := fs.Bool("git", false, "include git account creds/links")
	mailFlag := fs.Bool("mail", false, "include mailbox creds/links")
	only := fs.String("user", "", "comma-separated usernames to target (default: all verified)")
	limit := fs.Int("limit", 100000, "max accounts to scan")
	fs.Parse(args)

	// Default (neither flag given) is both; either flag alone narrows it.
	doGit, doMail := *gitFlag, *mailFlag
	if !doGit && !doMail {
		doGit, doMail = true, true
	}

	// Resolve the same configs main() builds for the live server.
	smtp := mail.ConfigFromEnv()
	fe := forwardemail.ConfigFromEnv()
	if fe.Domain == "" {
		fe.Domain = env("AGENTBBS_MAIL_DOMAIN", "mail.profullstack.com")
	}
	fj := forgejo.ConfigFromEnv()

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

	users, err := st.ListUsers(*limit)
	if err != nil {
		fmt.Fprintln(os.Stderr, "list users:", err)
		os.Exit(1)
	}

	if !*send {
		fmt.Println("PREVIEW (no email sent, no passwords reset) — re-run with --send to execute")
	}
	if doGit && !fj.Configured() {
		fmt.Fprintln(os.Stderr, "warning: Forgejo not configured (AGENTBBS_FORGEJO_URL/_ADMIN_TOKEN) — skipping git")
		doGit = false
	}
	if doMail && !fe.Configured() {
		fmt.Fprintln(os.Stderr, "warning: forwardemail not configured (AGENTBBS_FORWARDEMAIL_API_KEY/_DOMAIN) — skipping mail")
		doMail = false
	}
	if *send && !smtp.Configured() {
		fmt.Fprintln(os.Stderr, "error: SMTP not configured (AGENTBBS_SMTP_HOST/_FROM) — cannot send email")
		os.Exit(1)
	}
	if !doGit && !doMail {
		fmt.Fprintln(os.Stderr, "nothing to do")
		os.Exit(2)
	}

	var targeted, gitOK, gitErr, mailOK, mailErr int
	for _, u := range users {
		if want != nil && !want[strings.ToLower(u.Name)] {
			continue
		}
		if !u.EmailVerified || u.Email == "" || u.Banned {
			continue
		}
		targeted++

		if doGit {
			if !*send {
				fmt.Printf("  [git]  %-20s -> %s  (reset password + email %s)\n", u.Name, fj.LoginURL(), u.Email)
			} else {
				created, pw, err := fj.EnsureUserReset(u.Name, u.Email)
				if err != nil {
					gitErr++
					fmt.Fprintf(os.Stderr, "  [git]  %s: %v\n", u.Name, err)
				} else if err := smtp.Send(u.Email, "Your git.profullstack.com account is ready",
					gitWelcomeEmailBody(u.Name, pw, fj.LoginURL())); err != nil {
					gitErr++
					fmt.Fprintf(os.Stderr, "  [git]  %s: send: %v\n", u.Name, err)
				} else {
					gitOK++
					verb := "reset+emailed"
					if created {
						verb = "created+emailed"
					}
					fmt.Printf("  [git]  %-20s %s -> %s\n", u.Name, verb, u.Email)
				}
			}
		}

		if doMail {
			addr := fe.Address(u.Name)
			if !*send {
				fmt.Printf("  [mail] %-20s -> %s  (ensure alias + email %s)\n", u.Name, addr, u.Email)
			} else {
				if err := fe.CreateAlias(u.Name, u.Email); err != nil {
					mailErr++
					fmt.Fprintf(os.Stderr, "  [mail] %s: alias: %v\n", u.Name, err)
				} else if err := smtp.Send(u.Email, "Your "+fe.Domain+" mailbox is ready",
					mailWelcomeEmailBody(u.Name, addr, fe.WebmailURL())); err != nil {
					mailErr++
					fmt.Fprintf(os.Stderr, "  [mail] %s: send: %v\n", u.Name, err)
				} else {
					mailOK++
					fmt.Printf("  [mail] %-20s ensured+emailed -> %s\n", u.Name, addr)
				}
			}
		}
	}

	fmt.Printf("\n%d verified account(s) targeted.\n", targeted)
	if *send {
		fmt.Printf("git:  %d sent, %d failed\nmail: %d sent, %d failed\n", gitOK, gitErr, mailOK, mailErr)
		if gitErr > 0 || mailErr > 0 {
			os.Exit(1)
		}
	}
}
