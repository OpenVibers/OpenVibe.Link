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

	"github.com/OpenVibers/OpenVibe.Link/internal/gitrelay"
)

// cmdClone serves one "Clone to my computer" link from a repository page. The person sees the repository and the
// folder here and answers; git runs with their own git, inside the root they chose, and the page shows progress.
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
	approve := func(_ context.Context, req gitrelay.CloneRequest) (gitrelay.CloneDecision, error) {
		_, name, err := gitrelay.RepoName(req.Repo)
		if err != nil {
			return gitrelay.CloneDecision{}, err
		}
		folder := filepath.Join(root, name)
		fmt.Fprintf(stdout, "\nOpenVibe Git: clone %s\n  into %s\n", req.Repo, folder)
		if yes {
			return gitrelay.CloneDecision{Approve: true, Folder: folder}, nil
		}
		ok, err := confirm(stdin, stdout, "Clone it here?")
		if err != nil {
			return gitrelay.CloneDecision{}, err
		}
		return gitrelay.CloneDecision{Approve: ok, Folder: folder}, nil
	}
	overwrite := func(_ context.Context, req gitrelay.CloneRequest, folder string) (bool, error) {
		fmt.Fprintf(stdout, "\n%s already has files in it. Cloning into it adds this repository's files beside them.\n", folder)
		if yes {
			return true, nil
		}
		return confirm(stdin, stdout, "Clone into it anyway?")
	}
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

// cmdGitConnect publishes this computer's repositories to OpenVibe Git, read-only, until Ctrl-C. It needs a link
// from the person's Git page; the machine only ever polls Git, so nothing is opened on this computer.
func cmdGitConnect(g *globals, raw, root string, stdout io.Writer) error {
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
	return gitrelay.MachineConnect(ctx, raw, gitrelay.MachineOptions{Root: root, Log: g.log, Name: hostName()})
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
