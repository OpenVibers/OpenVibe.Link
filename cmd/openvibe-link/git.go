package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/OpenVibers/OpenVibe.Link/internal/gitrelay"
)

// cmdClone serves one "Clone to my computer" link from a repository page. The person sees the repository and the
// folder on the desktop and here, and answers either; git runs with their own git, inside the root they chose,
// and the page shows progress.
func cmdClone(g *globals, raw, root string, yes bool, stdin io.Reader, stdout io.Writer) error {
	if root == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return err
		}
		root = filepath.Join(home, "OpenVibe")
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	approve, overwrite := cloneApproverWithDesktop(root, yes, stdin, stdout, realDesktop{})
	res, err := gitrelay.Clone(ctx, raw, gitrelay.CloneOptions{Root: root, Approve: approve, ApproveOverwrite: overwrite})
	if err != nil {
		return err
	}
	if res.Reason != "" {
		fmt.Fprintln(stdout, res.Reason+"; nothing was written.")
		return nil
	}
	fmt.Fprintln(stdout, "Cloned into", res.Folder)
	return nil
}

// cloneApprover is how a clone is allowed on this machine: the repository, who asked on the page, the folder and
// how long is left, then one y/N here. --yes answers yes without asking, for a script that means it. Without a
// terminal there is no one to ask, and the answer is no.
func cloneApprover(root string, yes bool, stdin io.Reader, stdout io.Writer) (
	func(context.Context, gitrelay.CloneRequest) (gitrelay.CloneDecision, error),
	func(context.Context, gitrelay.CloneRequest, string) (bool, error),
) {
	return cloneApproverWithDesktop(root, yes, stdin, stdout, nil)
}

// cloneApproverWithDesktop asks the same question on the desktop when desktop is
// set (a prompt with Approve and Deny) and in the terminal: the first answer
// wins, and the desktop prompt is taken down once the terminal answers. With a
// nil desktop only the terminal asks, as before.
func cloneApproverWithDesktop(root string, yes bool, stdin io.Reader, stdout io.Writer, desktop desktopPrompter) (
	func(context.Context, gitrelay.CloneRequest) (gitrelay.CloneDecision, error),
	func(context.Context, gitrelay.CloneRequest, string) (bool, error),
) {
	approve := func(ctx context.Context, req gitrelay.CloneRequest) (gitrelay.CloneDecision, error) {
		_, name, err := gitrelay.RepoName(req.Repo)
		if err != nil {
			return gitrelay.CloneDecision{}, err
		}
		folder := filepath.Join(root, name)
		left := time.Until(req.ExpiresAt).Round(time.Second)
		if !req.ExpiresAt.IsZero() && left > 0 {
			fmt.Fprintf(stdout, "\nOpenVibe Git: clone %s\n  into %s\n  asked by %s, %s left\n", req.Repo, folder, orSomeone(req.RequestedBy), left)
		} else {
			fmt.Fprintf(stdout, "\nOpenVibe Git: clone %s\n  into %s\n", req.Repo, folder)
		}
		if yes {
			return gitrelay.CloneDecision{Approve: true, Folder: folder}, nil
		}
		ok, err := askPerson(ctx, stdin, stdout, desktop, "Clone it here?",
			"OpenVibe: clone "+req.Repo+"?",
			"Clone "+req.Repo+" into "+folder+"\nAsked by "+orSomeone(req.RequestedBy)+cloneLeft(req.ExpiresAt),
			req.ExpiresAt)
		if err != nil {
			return gitrelay.CloneDecision{}, err
		}
		return gitrelay.CloneDecision{Approve: ok, Folder: folder}, nil
	}
	overwrite := func(ctx context.Context, req gitrelay.CloneRequest, folder string) (bool, error) {
		fmt.Fprintf(stdout, "\n%s already has files in it. Cloning into it adds this repository's files beside them.\n", folder)
		if yes {
			return true, nil
		}
		return askPerson(ctx, stdin, stdout, desktop, "Clone into it anyway?",
			"OpenVibe: clone into "+filepath.Base(folder)+"?",
			folder+" already has files in it. Cloning adds this repository's files beside them.",
			req.ExpiresAt)
	}
	return approve, overwrite
}

// cloneLeft is the desktop body's countdown: ", 1m20s left", or "" without it.
func cloneLeft(expiresAt time.Time) string {
	if expiresAt.IsZero() {
		return ""
	}
	if left := time.Until(expiresAt).Round(time.Second); left > 0 {
		return ", " + left.String() + " left"
	}
	return ""
}

// askPerson takes one yes/no answer from the terminal and the desktop prompt,
// whichever answers first. Without a desktop it is exactly confirm: a closed
// input refuses, and a missing one errors. With one, a piped input does not
// decide (there is no person at it); the desktop does. Past the request's expiry
// there is nothing to ask anymore, and the answer is an error, not a refusal.
func askPerson(ctx context.Context, stdin io.Reader, stdout io.Writer, desktop desktopPrompter, question, title, body string, expiresAt time.Time) (bool, error) {
	if ctx.Err() != nil {
		return false, ctx.Err()
	}
	if desktop == nil {
		return confirm(stdin, stdout, question)
	}
	if !expiresAt.IsZero() && time.Until(expiresAt) <= 0 {
		return false, errCloneExpired
	}
	if !desktop.available() {
		// No prompt to show: the terminal decides alone, exactly as before.
		return confirm(stdin, stdout, question)
	}
	// A pipe or a file is not the person, so it does not decide when the
	// desktop asks; a stray pipe cannot refuse for them.
	useTerminal := stdin != nil && isTerminal(stdin)
	type verdict struct {
		approved bool
		asked    bool // this side actually asked; otherwise wait for the other
		err      error
	}
	wait := ctx
	cancel := context.CancelFunc(func() {})
	if !expiresAt.IsZero() {
		wait, cancel = context.WithTimeout(ctx, time.Until(expiresAt))
	}
	defer cancel()
	ch := make(chan verdict, 2)
	if useTerminal {
		go func() {
			ok, err := confirm(stdin, stdout, question)
			ch <- verdict{approved: ok, asked: err == nil, err: err}
		}()
	}
	go func() {
		ans := desktop.ask(wait, title, body, time.Until(expiresAt))
		if ans == nil {
			ch <- verdict{asked: false}
			return
		}
		ch <- verdict{approved: *ans, asked: true}
	}()
	pending := 1
	if useTerminal {
		pending = 2
	}
	for pending > 0 {
		select {
		case v := <-ch:
			if v.err != nil {
				return false, v.err
			}
			if !v.asked {
				pending--
				continue
			}
			return v.approved, nil
		case <-wait.Done():
			if ctx.Err() != nil {
				return false, ctx.Err()
			}
			return false, errCloneExpired
		}
	}
	return false, errNobodyToAsk
}

func orSomeone(who string) string {
	if strings.TrimSpace(who) == "" {
		return "someone on the page"
	}
	return who
}

// cmdGitConnect publishes this computer's repositories to OpenVibe Git, read-only, until Ctrl-C, and lets the person
// clone one of their repositories here: a page's request waits until it is answered on the desktop or with y here.
// It needs a link from the person's Git page; the machine only ever polls Git, so nothing is opened on this computer.
func cmdGitConnect(g *globals, raw, root string, yes bool, stdin io.Reader, stdout io.Writer) error {
	if root == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return err
		}
		root = filepath.Join(home, "OpenVibe")
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	fmt.Fprintf(stdout, "Publishing the repositories under %s to OpenVibe Git, read-only, until Ctrl-C.\n", root)
	fmt.Fprintln(stdout, "A clone asked for from your repository page waits for your answer, on the desktop or here; nothing is written before it.")
	approve, overwrite := cloneApprover(root, yes, stdin, stdout)
	return gitrelay.MachineConnect(ctx, raw, gitrelay.MachineOptions{
		Root: root, Log: g.log, Name: hostName(), Approve: approve, ApproveOverwrite: overwrite,
	})
}

func hostName() string {
	h, _ := os.Hostname()
	return h
}

// confirm reads one yes/no answer. Anything but y or yes is no, including end of input.
func confirm(stdin io.Reader, stdout io.Writer, question string) (bool, error) {
	if stdin == nil {
		return false, errors.New("there is no terminal to ask on; pass --yes to approve from a script")
	}
	fmt.Fprintf(stdout, "%s [y/N] ", question)
	line, err := bufio.NewReader(stdin).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return false, err
	}
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "y", "yes":
		return true, nil
	}
	return false, nil
}
