package gitops

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

const maxGitOutputBytes = 8 * 1024 * 1024

type Result struct {
	Branch  string `json:"branch,omitempty"`
	Status  string `json:"status,omitempty"`
	Stat    string `json:"stat,omitempty"`
	Numstat string `json:"numstat,omitempty"`
	Diff    string `json:"diff,omitempty"`
	Log     string `json:"log,omitempty"`
	Output  string `json:"output,omitempty"`
}

func CurrentBranch(ctx context.Context, workspace string) (string, error) {
	return run(ctx, workspace, "branch", "--show-current")
}

func Snapshot(ctx context.Context, workspace string, includeDiff bool) (Result, error) {
	readCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	tasks := []gitReadTask{
		{name: "branch", args: []string{"branch", "--show-current"}, required: true},
		{name: "status", args: []string{"status", "--short"}, required: true},
		{name: "stat", args: []string{"diff", "--stat"}, required: true},
		{name: "numstat", args: []string{"diff", "--numstat"}, required: true},
		{name: "log", args: []string{"log", "--oneline", "-n", "20"}},
	}
	if includeDiff {
		tasks = append(tasks, gitReadTask{name: "diff", args: []string{"diff"}, required: true})
	}

	results := make(chan gitReadResult, len(tasks))
	for _, task := range tasks {
		task := task
		go func() {
			output, err := run(readCtx, workspace, task.args...)
			results <- gitReadResult{task: task, output: output, err: err}
		}()
	}

	var result Result
	for range tasks {
		item := <-results
		if item.err != nil && item.task.required {
			return Result{}, fmt.Errorf("git %s: %w: %s", strings.Join(item.task.args, " "), item.err, strings.TrimSpace(item.output))
		}
		switch item.task.name {
		case "branch":
			result.Branch = item.output
		case "status":
			result.Status = item.output
		case "stat":
			result.Stat = item.output
		case "numstat":
			result.Numstat = item.output
		case "log":
			if item.err == nil {
				result.Log = item.output
			}
		case "diff":
			result.Diff = item.output
		}
	}
	return result, nil
}

type gitReadTask struct {
	name     string
	args     []string
	required bool
}

type gitReadResult struct {
	task   gitReadTask
	output string
	err    error
}

func Commit(ctx context.Context, workspace, message string) (string, error) {
	message = strings.TrimSpace(message)
	if message == "" {
		return "", errors.New("commit message is required")
	}
	if len(message) > 8*1024 {
		return "", errors.New("commit message exceeds 8 KiB limit")
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
	var out limitedBuffer
	out.limit = maxGitOutputBytes
	cmd.Stdout = &out
	cmd.Stderr = &out
	err := cmd.Run()
	return out.String(), err
}

type limitedBuffer struct {
	bytes.Buffer
	limit     int
	truncated bool
}

func (b *limitedBuffer) Write(value []byte) (int, error) {
	originalLength := len(value)
	remaining := b.limit - b.Len()
	if remaining > 0 {
		if len(value) > remaining {
			value = value[:remaining]
			b.truncated = true
		}
		_, _ = b.Buffer.Write(value)
	} else if len(value) > 0 {
		b.truncated = true
	}
	return originalLength, nil
}

func (b *limitedBuffer) String() string {
	value := b.Buffer.String()
	if b.truncated {
		value += "\n... output truncated at 8 MiB"
	}
	return value
}
