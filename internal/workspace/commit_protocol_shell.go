package workspace

import (
	"context"
	"fmt"
	"path/filepath"
)

// CommitCheckInvocation binds a configured command to the final accepted
// object. It is ephemeral and carries no journal identity.
type CommitCheckInvocation struct {
	checkID  ID
	commit   GitObjectID
	tree     GitObjectID
	runner   ID
	command  Argv
	worktree string
}

func (invocation CommitCheckInvocation) CheckID() ID         { return invocation.checkID }
func (invocation CommitCheckInvocation) Commit() GitObjectID { return invocation.commit }
func (invocation CommitCheckInvocation) Tree() GitObjectID   { return invocation.tree }
func (invocation CommitCheckInvocation) Runner() ID          { return invocation.runner }
func (invocation CommitCheckInvocation) Worktree() string    { return invocation.worktree }
func (invocation CommitCheckInvocation) Command() Argv {
	return Argv{values: invocation.command.Values()}
}

func (invocation CommitCheckInvocation) validate() error {
	if invocation.checkID.IsZero() || invocation.commit.IsZero() || invocation.tree.IsZero() ||
		invocation.commit.Algorithm() != invocation.tree.Algorithm() || invocation.runner.IsZero() ||
		len(invocation.command.values) == 0 || !filepath.IsAbs(filepath.Clean(invocation.worktree)) {
		return fmt.Errorf("configured check invocation has incomplete bindings")
	}
	return nil
}

// CommitCheckRunnerPort materializes the invocation's exact final commit in
// isolated storage and executes its configured command.
type CommitCheckRunnerPort interface {
	RunConfiguredCheck(context.Context, CommitCheckInvocation) error
}
