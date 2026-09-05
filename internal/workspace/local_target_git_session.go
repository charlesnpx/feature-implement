package workspace

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
)

// localTargetGitSession centralizes the isolated Git invocation used for one
// bound target. It deliberately does not retain filesystem identities for
// Git's administration files: the target profile and compare-and-swap are the
// admission and mutation boundaries.
type localTargetGitSession struct {
	adapter LocalTargetGitAdapter
	binding LocalTargetBinding
}

func (adapter LocalTargetGitAdapter) openBoundSession(
	binding LocalTargetBinding,
) (*localTargetGitSession, error) {
	if binding.IsZero() {
		return nil, fmt.Errorf("bound local target Git session requires a target binding")
	}
	root, err := requireCanonicalObservedTargetPath("target worktree", binding.root)
	if err != nil {
		return nil, err
	}
	if root != binding.root {
		return nil, fmt.Errorf("bound local target root differs from its admitted path")
	}
	return &localTargetGitSession{adapter: adapter, binding: binding}, nil
}

func (session *localTargetGitSession) Verify() error {
	if session == nil || session.binding.IsZero() ||
		session.adapter.git.executable == "" {
		return fmt.Errorf("bound local target Git session is closed")
	}
	return nil
}

func (session *localTargetGitSession) Close() error {
	if session == nil {
		return nil
	}
	session.adapter = LocalTargetGitAdapter{}
	session.binding = LocalTargetBinding{}
	return nil
}

func (session *localTargetGitSession) ensureFeatureBranchAvailable(
	ctx context.Context,
) error {
	if err := session.Verify(); err != nil {
		return err
	}
	return session.adapter.rejectCheckedOutFeatureBranch(
		ctx, session.binding.root, session.binding.featureRef,
	)
}

func (session *localTargetGitSession) run(
	ctx context.Context,
	input []byte,
	arguments ...string,
) ([]byte, int, error) {
	if ctx == nil {
		return nil, -1, fmt.Errorf("bound local target Git command requires context")
	}
	if err := session.ensureFeatureBranchAvailable(ctx); err != nil {
		return nil, -1, err
	}
	command, err := session.command(ctx, arguments...)
	if err != nil {
		return nil, -1, err
	}
	if input != nil {
		command.Stdin = bytes.NewReader(input)
	}
	var stdout, stderr boundedProcessBuffer
	stdout.maximum = maxAttemptGitOutputBytes
	stderr.maximum = 64 * 1024
	command.Stdout, command.Stderr = &stdout, &stderr
	runErr := command.Run()
	if stdout.exceeded || stderr.exceeded {
		return nil, -1, fmt.Errorf("Git output exceeded its bound")
	}
	exitCode := 0
	if runErr != nil {
		var exitError *exec.ExitError
		if !errors.As(runErr, &exitError) {
			return nil, -1, runErr
		}
		exitCode = exitError.ExitCode()
	}
	output := stdout.bytes()
	if exitCode != 0 && len(output) == 0 {
		output = stderr.bytes()
	}
	return output, exitCode, nil
}

func (session *localTargetGitSession) command(
	ctx context.Context,
	arguments ...string,
) (*exec.Cmd, error) {
	if ctx == nil {
		return nil, fmt.Errorf("bound local target Git command requires context")
	}
	if err := session.Verify(); err != nil {
		return nil, err
	}
	command := exec.CommandContext(
		ctx,
		session.adapter.git.executable,
		trustedGitArguments(session.binding.root, arguments...)...,
	)
	environment, err := BuildIsolatedProcessEnvironment(
		os.Environ(), session.adapter.git.environment,
	)
	if err != nil {
		return nil, err
	}
	command.Env = environment
	return command, nil
}

// runPreparedReferenceTransaction preserves Git's prepare/commit protocol so
// the branch update remains a single compare-and-swap. The preflight branch
// check prevents the tool from updating an operator's checked-out branch.
func (session *localTargetGitSession) runPreparedReferenceTransaction(
	ctx context.Context,
	commands []byte,
	inspectPrepared func() error,
) (resultErr error) {
	if ctx == nil || len(commands) == 0 || inspectPrepared == nil {
		return fmt.Errorf("prepared reference transaction requires context, commands, and inspection")
	}
	if err := session.ensureFeatureBranchAvailable(ctx); err != nil {
		return err
	}
	command, err := session.command(ctx, "update-ref", "--stdin", "--no-deref")
	if err != nil {
		return err
	}
	stdin, err := command.StdinPipe()
	if err != nil {
		return err
	}
	stdout, err := command.StdoutPipe()
	if err != nil {
		_ = stdin.Close()
		return err
	}
	var stderr boundedProcessBuffer
	stderr.maximum = 64 * 1024
	command.Stderr = &stderr
	if err := command.Start(); err != nil {
		_ = stdin.Close()
		return err
	}

	response := bufio.NewReader(io.LimitReader(stdout, 64*1024+1))
	waited := false
	prepared := false
	commitSent := false
	wait := func() error {
		if waited {
			return nil
		}
		waited = true
		waitErr := command.Wait()
		if stderr.exceeded {
			return fmt.Errorf("Git transaction error output exceeded its bound")
		}
		if waitErr != nil {
			return fmt.Errorf(
				"Git reference transaction failed: %w: %s",
				waitErr, strings.TrimSpace(string(stderr.bytes())),
			)
		}
		return nil
	}
	send := func(action, expected string) error {
		if _, err := io.WriteString(stdin, action+"\n"); err != nil {
			return fmt.Errorf("send Git reference transaction %s: %w", action, err)
		}
		line, err := response.ReadString('\n')
		if err != nil {
			return fmt.Errorf("read Git reference transaction %s response: %w", action, err)
		}
		if line != expected+"\n" {
			return fmt.Errorf(
				"Git reference transaction %s response is %q, expected %q",
				action, strings.TrimSuffix(line, "\n"), expected,
			)
		}
		return nil
	}
	finish := func(action, expected string) error {
		if action == "commit" {
			commitSent = true
		}
		sendErr := send(action, expected)
		if sendErr == nil {
			prepared = false
		}
		closeErr := stdin.Close()
		return errors.Join(sendErr, closeErr, wait())
	}
	defer func() {
		if waited {
			return
		}
		var cleanupErr error
		if prepared && !commitSent {
			cleanupErr = send("abort", "abort: ok")
			prepared = false
		}
		resultErr = errors.Join(resultErr, cleanupErr, stdin.Close(), wait())
	}()

	if err := send("start", "start: ok"); err != nil {
		return err
	}
	if commands[len(commands)-1] != '\n' {
		commands = append(append([]byte(nil), commands...), '\n')
	}
	if _, err := stdin.Write(commands); err != nil {
		return fmt.Errorf("queue Git reference transaction commands: %w", err)
	}
	if err := send("prepare", "prepare: ok"); err != nil {
		return err
	}
	prepared = true
	if err := inspectPrepared(); err != nil {
		return errors.Join(err, finish("abort", "abort: ok"))
	}
	return finish("commit", "commit: ok")
}

func (session *localTargetGitSession) inspectFeatureRef(
	ctx context.Context,
) (bool, GitObjectID, error) {
	output, exitCode, err := session.run(ctx, nil, "show-ref", "--verify", session.binding.featureRef)
	if err != nil {
		return false, GitObjectID{}, err
	}
	if exitCode == 1 || exitCode == 128 {
		return false, GitObjectID{}, nil
	}
	if exitCode != 0 {
		return false, GitObjectID{}, fmt.Errorf(
			"inspect feature ref %s: Git exited with status %d",
			session.binding.featureRef, exitCode,
		)
	}
	fields := strings.Fields(string(output))
	if len(fields) != 2 || fields[1] != session.binding.featureRef {
		return false, GitObjectID{}, fmt.Errorf("Git returned malformed feature-ref data")
	}
	head, err := qualifyGitObjectID(session.binding.objectFormat, fields[0])
	if err != nil {
		return false, GitObjectID{}, err
	}
	return true, head, nil
}
