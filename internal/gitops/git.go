package gitops

import (
	"bytes"
	"context"
	"errors"
	"os/exec"
	"time"
)

type Result struct {
	Branch  string `json:"branch,omitempty"`
	Status  string `json:"status,omitempty"`
	Stat    string `json:"stat,omitempty"`
	Numstat string `json:"numstat,omitempty"`
	Diff    string `json:"diff,omitempty"`
	Log     string `json:"log,omitempty"`
	Output  string `json:"output,omitempty"`
}

func Snapshot(ctx context.Context, workspace string, includeDiff bool) (Result, error) {
	branch, err := run(ctx, workspace, "branch", "--show-current")
	if err != nil {
		return Result{}, err
	}
	status, err := run(ctx, workspace, "status", "--short")
	if err != nil {
		return Result{}, err
	}
	stat, err := run(ctx, workspace, "diff", "--stat")
	if err != nil {
		return Result{}, err
	}
	numstat, err := run(ctx, workspace, "diff", "--numstat")
	if err != nil {
		return Result{}, err
	}
	log, err := run(ctx, workspace, "log", "--oneline", "-n", "20")
	if err != nil {
		log = ""
	}
	result := Result{Branch: branch, Status: status, Stat: stat, Numstat: numstat, Log: log}
	if includeDiff {
		diff, err := run(ctx, workspace, "diff")
		if err != nil {
			return Result{}, err
		}
		result.Diff = diff
	}
	return result, nil
}

func Commit(ctx context.Context, workspace, message string) (string, error) {
	if message == "" {
		return "", errors.New("commit message is required")
	}
	return run(ctx, workspace, "commit", "-am", message)
}

func Push(ctx context.Context, workspace string) (string, error) {
	return run(ctx, workspace, "push")
}

func Undo(ctx context.Context, workspace string) (string, error) {
	return run(ctx, workspace, "restore", ".")
}

func run(ctx context.Context, workspace string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = workspace
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	err := cmd.Run()
	return out.String(), err
}
