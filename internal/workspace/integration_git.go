package workspace

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
)

type LocalIntegrationGitAdapter struct {
	target LocalTargetGitAdapter
}

func NewLocalIntegrationGitAdapter(
	executable string,
	environment []EnvironmentVariable,
) (LocalIntegrationGitAdapter, error) {
	target, err := NewLocalTargetGitAdapter(executable, environment)
	if err != nil {
		return LocalIntegrationGitAdapter{}, err
	}
	return LocalIntegrationGitAdapter{target: target}, nil
}

func DefaultLocalIntegrationGitAdapter() LocalIntegrationGitAdapter {
	adapter, _ := NewLocalIntegrationGitAdapter("git", nil)
	return adapter
}

func (adapter LocalIntegrationGitAdapter) InspectAttempt(
	ctx context.Context,
	binding LocalTargetBinding,
	worktree string,
	expectedHead, expectedTree GitObjectID,
) (result AttemptGitInspection, resultErr error) {
	if ctx == nil {
		return AttemptGitInspection{}, fmt.Errorf(
			"integration attempt inspection requires context",
		)
	}
	if binding.IsZero() || expectedHead.IsZero() ||
		expectedTree.IsZero() ||
		expectedHead.Algorithm() != binding.objectFormat ||
		expectedTree.Algorithm() != binding.objectFormat {
		return AttemptGitInspection{}, fmt.Errorf(
			"integration attempt inspection requires target-format head and tree bindings",
		)
	}
	session, err := adapter.target.openBoundSession(binding)
	if err != nil {
		return AttemptGitInspection{}, err
	}
	defer func() {
		resultErr = errors.Join(resultErr, session.Close())
	}()
	inspection, err := adapter.target.git.InspectAttemptWorktree(
		ctx, binding.root, worktree,
	)
	if err != nil {
		return AttemptGitInspection{}, err
	}
	if err := verifyIntegrationAttemptInspection(
		binding, filepath.Clean(worktree),
		expectedHead, expectedTree, inspection,
	); err != nil {
		return AttemptGitInspection{}, err
	}
	if err := session.Verify(); err != nil {
		return AttemptGitInspection{}, err
	}
	return inspection, nil
}

func (adapter LocalIntegrationGitAdapter) InspectIntegration(
	ctx context.Context,
	binding LocalTargetBinding,
	intent MergeUnitIntegrationIntent,
) (result IntegrationGitInspection, resultErr error) {
	if ctx == nil {
		return IntegrationGitInspection{}, fmt.Errorf(
			"integration inspection requires context",
		)
	}
	if err := validateIntegrationGitRequest(
		binding, intent,
	); err != nil {
		return IntegrationGitInspection{}, err
	}
	if _, err := adapter.inspectBoundAttempt(
		ctx, binding, intent,
	); err != nil {
		return IntegrationGitInspection{}, err
	}
	session, err := adapter.target.openBoundSession(binding)
	if err != nil {
		return IntegrationGitInspection{}, err
	}
	defer func() {
		resultErr = errors.Join(resultErr, session.Close())
	}()
	return inspectIntegrationSession(
		ctx, session, intent,
	)
}

func (adapter LocalIntegrationGitAdapter) CreateIntegrationCommit(
	ctx context.Context,
	binding LocalTargetBinding,
	intent MergeUnitIntegrationIntent,
) (resultErr error) {
	if ctx == nil {
		return fmt.Errorf(
			"integration commit creation requires context",
		)
	}
	if err := validateIntegrationGitRequest(
		binding, intent,
	); err != nil {
		return err
	}
	if _, err := adapter.inspectBoundAttempt(
		ctx, binding, intent,
	); err != nil {
		return err
	}
	session, err := adapter.target.openBoundSession(binding)
	if err != nil {
		return err
	}
	defer func() {
		resultErr = errors.Join(resultErr, session.Close())
	}()
	inspection, err := inspectIntegrationSession(
		ctx, session, intent,
	)
	if err != nil {
		return err
	}
	if inspection.refState == IntegrationRefExpectedMerge &&
		inspection.expectedCommit {
		return nil
	}
	if inspection.refState != IntegrationRefExpectedHead {
		return integrationDriftError(inspection)
	}
	if inspection.expectedCommit {
		return nil
	}
	output, exitCode, err := session.run(
		ctx, intent.commitContent(),
		"hash-object", "-t", "commit", "-w", "--stdin",
	)
	if err != nil || exitCode != 0 {
		return gitExitError(
			"create deterministic integration commit",
			exitCode, err,
		)
	}
	created, err := qualifyGitObjectID(
		binding.objectFormat,
		strings.TrimSpace(string(output)),
	)
	if err != nil {
		return fmt.Errorf(
			"parse deterministic integration commit: %w", err,
		)
	}
	if created != intent.expectedMerge {
		return fmt.Errorf(
			"created integration commit %s does not match expected %s",
			created, intent.expectedMerge,
		)
	}
	if err := verifyExpectedIntegrationCommit(
		ctx, session, intent,
	); err != nil {
		return err
	}
	return session.Verify()
}

func (adapter LocalIntegrationGitAdapter) PublishIntegration(
	ctx context.Context,
	binding LocalTargetBinding,
	intent MergeUnitIntegrationIntent,
	fault IntegrationLifecycleFaultInjector,
) (resultErr error) {
	if ctx == nil {
		return fmt.Errorf(
			"integration publication requires context",
		)
	}
	if err := validateIntegrationGitRequest(
		binding, intent,
	); err != nil {
		return err
	}
	if _, err := adapter.inspectBoundAttempt(
		ctx, binding, intent,
	); err != nil {
		return err
	}
	session, err := adapter.target.openBoundSession(binding)
	if err != nil {
		return err
	}
	defer func() {
		resultErr = errors.Join(resultErr, session.Close())
	}()
	inspection, err := inspectIntegrationSession(
		ctx, session, intent,
	)
	if err != nil {
		return err
	}
	if inspection.refState == IntegrationRefExpectedMerge {
		if !inspection.expectedCommit {
			return fmt.Errorf(
				"feature ref points to a missing expected integration commit",
			)
		}
		return nil
	}
	if inspection.refState != IntegrationRefExpectedHead {
		return integrationDriftError(inspection)
	}
	if !inspection.expectedCommit {
		return fmt.Errorf(
			"expected integration commit %s has not been created",
			intent.expectedMerge,
		)
	}
	if _, err := adapter.inspectBoundAttempt(
		ctx, binding, intent,
	); err != nil {
		return fmt.Errorf(
			"verify exact attempt worktree immediately before feature-ref publication: %w",
			err,
		)
	}
	if err := session.Verify(); err != nil {
		return err
	}
	// The feature-ref old-object field supplies the compare-and-swap check; the
	// detached scratch attempt is re-inspected immediately around the transaction.
	transaction := fmt.Sprintf(
		"update %s %s %s\n",
		intent.featureRef, gitObjectHex(intent.expectedMerge), gitObjectHex(intent.expectedFeatureHead),
	)
	if err := session.runPreparedReferenceTransaction(
		ctx,
		integrationReflogMessage(intent.digest),
		[]byte(transaction),
		func() error {
			exists, preparedHead, preparedMarker, err :=
				session.inspectFeatureRef(ctx)
			if err != nil {
				return err
			}
			if !exists ||
				preparedHead != intent.expectedFeatureHead ||
				preparedMarker != intent.expectedFeatureMarker {
				return fmt.Errorf(
					"feature ref changed from its exact owned head and marker before prepared publication",
				)
			}
			return injectIntegrationLifecycleFault(
				fault, IntegrationFaultAfterRefPrepared,
			)
		},
	); err != nil {
		return fmt.Errorf(
			"publish feature ref with prepared compare-and-swap: %w",
			err,
		)
	}
	if _, err := adapter.inspectBoundAttempt(
		ctx, binding, intent,
	); err != nil {
		return fmt.Errorf(
			"verify exact attempt worktree after feature-ref publication: %w",
			err,
		)
	}
	verified, err := inspectIntegrationSession(
		ctx, session, intent,
	)
	if err != nil {
		return err
	}
	if verified.refState != IntegrationRefExpectedMerge ||
		!verified.expectedCommit {
		return fmt.Errorf(
			"integration publication did not produce the exact expected merge",
		)
	}
	return session.Verify()
}

func integrationReflogMessage(intentDigest Digest) string {
	return "feature workspace integration " + intentDigest.String()
}

func (adapter LocalIntegrationGitAdapter) VerifyCompletedIntegration(
	ctx context.Context,
	binding LocalTargetBinding,
	chain []MergeUnitIntegrationIntent,
) (resultErr error) {
	if ctx == nil {
		return fmt.Errorf(
			"completed integration verification requires context",
		)
	}
	if len(chain) == 0 {
		return fmt.Errorf(
			"completed integration verification requires its durable frontier chain",
		)
	}
	for index, intent := range chain {
		if err := validateIntegrationTargetRequest(
			binding, intent,
		); err != nil {
			return err
		}
		if index != 0 &&
			intent.expectedFeatureHead !=
				chain[index-1].expectedMerge {
			return fmt.Errorf(
				"completed integration verification chain is not first-parent contiguous",
			)
		}
	}
	completed := chain[0]
	frontier := chain[len(chain)-1]
	if completed.workspaceID != frontier.workspaceID ||
		completed.generation != frontier.generation {
		return fmt.Errorf(
			"completed integration and current frontier do not share one workspace generation",
		)
	}
	session, err := adapter.target.openBoundSession(binding)
	if err != nil {
		return err
	}
	defer func() {
		resultErr = errors.Join(resultErr, session.Close())
	}()
	if _, err := session.inspectRegisteredWorktrees(ctx); err != nil {
		return err
	}
	exists, featureHead, featureMarker, err :=
		session.inspectFeatureRef(ctx)
	if err != nil {
		return err
	}
	if !exists {
		return fmt.Errorf(
			"owned feature ref %s is absent", completed.featureRef,
		)
	}
	frontierCommit, err := integrationCommitExists(
		ctx, session, frontier,
	)
	if err != nil {
		return err
	}
	refState, err := classifyIntegrationRefState(
		ctx, session, featureHead, frontier, frontierCommit,
	)
	if err != nil {
		return err
	}
	inspection, err := NewIntegrationGitInspection(
		featureHead, refState, frontierCommit,
	)
	if err != nil {
		return err
	}
	if refState != IntegrationRefExpectedMerge {
		return integrationDriftError(inspection)
	}
	for index, intent := range chain {
		expectedCommit := frontierCommit
		if index != len(chain)-1 {
			expectedCommit, err = integrationCommitExists(
				ctx, session, intent,
			)
			if err != nil {
				return err
			}
		}
		if !expectedCommit {
			return fmt.Errorf(
				"durable integration frontier commit %s is absent",
				intent.expectedMerge,
			)
		}
		if err := verifyIntegrationCommitObject(
			ctx, session, intent.acceptedHead, intent.acceptedTree,
		); err != nil {
			return fmt.Errorf(
				"verify durable accepted integration head for %s: %w",
				intent.mergeUnit, err,
			)
		}
		if err := verifyIntegrationTreeClosure(
			ctx, session, intent.acceptedTree,
		); err != nil {
			return fmt.Errorf(
				"verify durable accepted integration tree for %s: %w",
				intent.mergeUnit, err,
			)
		}
		ancestor, err := integrationIsAncestor(
			ctx, session, intent.expectedFeatureHead,
			intent.acceptedHead,
		)
		if err != nil {
			return err
		}
		if !ancestor {
			return fmt.Errorf(
				"expected feature parent %s is not an ancestor of durable accepted head %s for %s",
				intent.expectedFeatureHead, intent.acceptedHead,
				intent.mergeUnit,
			)
		}
	}
	if featureMarker != integrationReflogMessage(frontier.digest) {
		return fmt.Errorf(
			"completed integration feature ref does not retain its exact merge and marker for the current durable frontier",
		)
	}
	return session.Verify()
}

func (adapter LocalIntegrationGitAdapter) inspectBoundAttempt(
	ctx context.Context,
	binding LocalTargetBinding,
	intent MergeUnitIntegrationIntent,
) (AttemptGitInspection, error) {
	inspection, err := adapter.InspectAttempt(
		ctx, binding, intent.attemptWorktreeBinding.worktree,
		intent.acceptedHead, intent.acceptedTree,
	)
	if err != nil {
		return AttemptGitInspection{}, err
	}
	if inspection.worktreeBinding != intent.attemptWorktreeBinding {
		return AttemptGitInspection{}, fmt.Errorf(
			"attempt worktree Git binding changed after durable integration intent",
		)
	}
	if err := adapter.importDetachedAttemptObjects(
		ctx, binding, inspection.worktreeBinding.worktree, intent.acceptedHead,
	); err != nil {
		return AttemptGitInspection{}, err
	}
	return inspection, nil
}

// importDetachedAttemptObjects copies the accepted object closure into the
// target object database without creating an attempt ref. Detached attempts
// have their own Git administration, so their commits are otherwise visible
// only through the attempt's object directory.
func (adapter LocalIntegrationGitAdapter) importDetachedAttemptObjects(
	ctx context.Context,
	binding LocalTargetBinding,
	worktree string,
	head GitObjectID,
) error {
	if worktree == "" || head.IsZero() {
		return fmt.Errorf("detached attempt import requires worktree and accepted head")
	}
	// A raw object ID is not an advertised upload-pack ref, so Git rejects a
	// fetch of it from the detached repository. HEAD is advertised even when it
	// is detached. The caller has already inspected that HEAD as `head`; verify
	// the imported object below before allowing integration to use it.
	output, exitCode, err := adapter.target.git.run(
		ctx, binding.root,
		"-c", "protocol.file.allow=always",
		"fetch", "--no-tags", "--no-write-fetch-head", worktree, "HEAD",
	)
	if err != nil || exitCode != 0 {
		if err == nil && len(output) != 0 {
			return fmt.Errorf(
				"import detached attempt objects: Git exited with status %d: %s",
				exitCode, strings.TrimSpace(string(output)),
			)
		}
		return gitExitError("import detached attempt objects", exitCode, err)
	}
	_, exitCode, err = adapter.target.git.run(
		ctx, binding.root, "cat-file", "-e", objectHex(head)+"^{commit}",
	)
	if err != nil || exitCode != 0 {
		return gitExitError(
			"verify imported detached attempt object "+head.String(),
			exitCode, err,
		)
	}
	return nil
}

func verifyIntegrationAttemptInspection(
	target LocalTargetBinding,
	worktree string,
	expectedHead, expectedTree GitObjectID,
	inspection AttemptGitInspection,
) error {
	if inspection.branchExists || !inspection.worktreeExists || inspection.worktreeRegistered ||
		!inspection.clean || inspection.worktreeHead != expectedHead ||
		inspection.worktreeTree != expectedTree ||
		inspection.worktreeBinding.IsZero() {
		return fmt.Errorf("integration requires an exact clean detached attempt worktree at its accepted head and tree")
	}
	attemptBinding := inspection.worktreeBinding
	if attemptBinding.worktree != worktree {
		return fmt.Errorf(
			"integration attempt worktree path changed during exact inspection",
		)
	}
	if attemptBinding.commonDirectory == target.commonDirectory {
		return fmt.Errorf(
			"integration attempt unexpectedly shares the target Git administration",
		)
	}
	return nil
}

func validateIntegrationGitRequest(
	binding LocalTargetBinding,
	intent MergeUnitIntegrationIntent,
) error {
	if err := validateIntegrationTargetRequest(
		binding, intent,
	); err != nil {
		return err
	}
	return nil
}

func validateIntegrationTargetRequest(
	binding LocalTargetBinding,
	intent MergeUnitIntegrationIntent,
) error {
	if binding.IsZero() {
		return fmt.Errorf(
			"integration Git request requires a local target binding",
		)
	}
	if err := intent.validate(); err != nil {
		return err
	}
	if binding.featureRef != intent.featureRef ||
		binding.objectFormat != intent.expectedFeatureHead.Algorithm() {
		return fmt.Errorf(
			"integration intent does not match the bound local target",
		)
	}
	return nil
}

func inspectIntegrationSession(
	ctx context.Context,
	session *localTargetGitSession,
	intent MergeUnitIntegrationIntent,
) (IntegrationGitInspection, error) {
	if err := session.Verify(); err != nil {
		return IntegrationGitInspection{}, err
	}
	if _, err := session.inspectRegisteredWorktrees(ctx); err != nil {
		return IntegrationGitInspection{}, err
	}
	exists, featureHead, featureMarker, err := session.inspectFeatureRef(ctx)
	if err != nil {
		return IntegrationGitInspection{}, err
	}
	if !exists {
		return IntegrationGitInspection{}, fmt.Errorf(
			"owned feature ref %s is absent", intent.featureRef,
		)
	}
	switch featureHead {
	case intent.expectedFeatureHead:
		if featureMarker != intent.expectedFeatureMarker {
			return IntegrationGitInspection{}, fmt.Errorf(
				"feature ref %s at the expected prior head has no exact durable workspace marker",
				intent.featureRef,
			)
		}
	case intent.expectedMerge:
		if featureMarker != integrationReflogMessage(intent.digest) {
			return IntegrationGitInspection{}, fmt.Errorf(
				"feature ref %s at the expected merge has no exact integration marker",
				intent.featureRef,
			)
		}
	}
	if err := verifyIntegrationCommitObject(
		ctx, session, featureHead, GitObjectID{},
	); err != nil {
		return IntegrationGitInspection{}, fmt.Errorf(
			"inspect current feature head: %w", err,
		)
	}
	if err := verifyIntegrationCommitObject(
		ctx, session, intent.acceptedHead, intent.acceptedTree,
	); err != nil {
		return IntegrationGitInspection{}, fmt.Errorf(
			"verify accepted integration head: %w", err,
		)
	}
	ancestor, err := integrationIsAncestor(
		ctx, session, intent.expectedFeatureHead,
		intent.acceptedHead,
	)
	if err != nil {
		return IntegrationGitInspection{}, err
	}
	if !ancestor {
		return IntegrationGitInspection{}, fmt.Errorf(
			"expected feature head %s is not an ancestor of accepted head %s",
			intent.expectedFeatureHead, intent.acceptedHead,
		)
	}
	expectedCommit, err := integrationCommitExists(
		ctx, session, intent,
	)
	if err != nil {
		return IntegrationGitInspection{}, err
	}
	refState, err := classifyIntegrationRefState(
		ctx, session, featureHead, intent, expectedCommit,
	)
	if err != nil {
		return IntegrationGitInspection{}, err
	}
	if err := session.Verify(); err != nil {
		return IntegrationGitInspection{}, err
	}
	return NewIntegrationGitInspection(
		featureHead, refState, expectedCommit,
	)
}

func resolveIntegrationRef(
	ctx context.Context,
	session *localTargetGitSession,
	ref string,
) (GitObjectID, error) {
	output, exitCode, err := session.run(
		ctx, nil, "show-ref", "--verify", ref,
	)
	if err != nil || exitCode != 0 {
		return GitObjectID{}, gitExitError(
			"resolve integration ref "+ref, exitCode, err,
		)
	}
	fields := strings.Fields(string(output))
	if len(fields) != 2 || fields[1] != ref {
		return GitObjectID{}, fmt.Errorf(
			"Git returned malformed integration ref data for %s", ref,
		)
	}
	return qualifyGitObjectID(
		session.binding.objectFormat, fields[0],
	)
}

func verifyIntegrationCommitObject(
	ctx context.Context,
	session *localTargetGitSession,
	commit GitObjectID,
	expectedTree GitObjectID,
) error {
	if commit.IsZero() ||
		commit.Algorithm() != session.binding.objectFormat {
		return fmt.Errorf(
			"integration commit does not use the repository object format",
		)
	}
	output, exitCode, err := session.run(
		ctx, nil, "cat-file", "commit", gitObjectHex(commit),
	)
	if err != nil || exitCode != 0 {
		return gitExitError(
			"read integration commit "+commit.String(),
			exitCode, err,
		)
	}
	tree, err := parseIntegrationCommitTree(
		output, session.binding.objectFormat,
	)
	if err != nil {
		return err
	}
	if !expectedTree.IsZero() && tree != expectedTree {
		return fmt.Errorf(
			"integration commit %s has tree %s, expected %s",
			commit, tree, expectedTree,
		)
	}
	return nil
}

func verifyIntegrationTreeClosure(
	ctx context.Context,
	session *localTargetGitSession,
	tree GitObjectID,
) error {
	if tree.IsZero() ||
		tree.Algorithm() != session.binding.objectFormat {
		return fmt.Errorf(
			"integration tree does not use the repository object format",
		)
	}
	output, exitCode, err := session.run(
		ctx, nil, "cat-file", "-t", gitObjectHex(tree),
	)
	if err != nil || exitCode != 0 {
		return gitExitError(
			"inspect integration tree type", exitCode, err,
		)
	}
	if strings.TrimSpace(string(output)) != "tree" {
		return fmt.Errorf(
			"integration tree %s is not a Git tree object", tree,
		)
	}
	_, exitCode, err = session.run(
		ctx, nil,
		"rev-list", "--quiet", "--objects", "--missing=error",
		gitObjectHex(tree),
	)
	if err != nil || exitCode != 0 {
		return gitExitError(
			"verify integration tree object closure", exitCode, err,
		)
	}
	return nil
}

func parseIntegrationCommitTree(
	content []byte,
	algorithm GitHashAlgorithm,
) (GitObjectID, error) {
	separator := bytes.Index(content, []byte("\n\n"))
	if separator < 0 {
		return GitObjectID{}, fmt.Errorf(
			"Git commit object has no message separator",
		)
	}
	var tree GitObjectID
	for _, line := range strings.Split(
		string(content[:separator]), "\n",
	) {
		if strings.HasPrefix(line, " ") {
			continue
		}
		name, value, found := strings.Cut(line, " ")
		if !found {
			return GitObjectID{}, fmt.Errorf(
				"Git commit header is malformed",
			)
		}
		if name != "tree" {
			continue
		}
		if !tree.IsZero() {
			return GitObjectID{}, fmt.Errorf(
				"Git commit repeats its tree header",
			)
		}
		parsed, err := qualifyGitObjectID(algorithm, value)
		if err != nil {
			return GitObjectID{}, err
		}
		tree = parsed
	}
	if tree.IsZero() {
		return GitObjectID{}, fmt.Errorf("Git commit has no tree")
	}
	return tree, nil
}

func integrationCommitExists(
	ctx context.Context,
	session *localTargetGitSession,
	intent MergeUnitIntegrationIntent,
) (bool, error) {
	output, exitCode, err := session.run(
		ctx, nil, "cat-file", "commit",
		gitObjectHex(intent.expectedMerge),
	)
	if err != nil {
		return false, err
	}
	if exitCode == 128 {
		return false, nil
	}
	if exitCode != 0 {
		return false, gitExitError(
			"inspect expected integration commit",
			exitCode, nil,
		)
	}
	if !bytes.Equal(output, intent.commitContent()) {
		return false, fmt.Errorf(
			"expected integration object %s has noncanonical content",
			intent.expectedMerge,
		)
	}
	return true, nil
}

func verifyExpectedIntegrationCommit(
	ctx context.Context,
	session *localTargetGitSession,
	intent MergeUnitIntegrationIntent,
) error {
	exists, err := integrationCommitExists(ctx, session, intent)
	if err != nil {
		return err
	}
	if !exists {
		return fmt.Errorf(
			"expected integration commit %s is absent",
			intent.expectedMerge,
		)
	}
	return nil
}

func integrationIsAncestor(
	ctx context.Context,
	session *localTargetGitSession,
	ancestor, descendant GitObjectID,
) (bool, error) {
	if ancestor.IsZero() || descendant.IsZero() ||
		ancestor.Algorithm() != session.binding.objectFormat ||
		descendant.Algorithm() != session.binding.objectFormat {
		return false, fmt.Errorf(
			"integration ancestry requires repository-format commits",
		)
	}
	_, exitCode, err := session.run(
		ctx, nil, "merge-base", "--is-ancestor",
		gitObjectHex(ancestor), gitObjectHex(descendant),
	)
	if err != nil {
		return false, err
	}
	switch exitCode {
	case 0:
		return true, nil
	case 1:
		return false, nil
	default:
		return false, gitExitError(
			"inspect integration ancestry", exitCode, nil,
		)
	}
}

func classifyIntegrationRefState(
	ctx context.Context,
	session *localTargetGitSession,
	current GitObjectID,
	intent MergeUnitIntegrationIntent,
	expectedCommit bool,
) (IntegrationRefState, error) {
	switch current {
	case intent.expectedFeatureHead:
		return IntegrationRefExpectedHead, nil
	case intent.expectedMerge:
		if !expectedCommit {
			return "", fmt.Errorf(
				"feature ref points to absent expected integration object",
			)
		}
		return IntegrationRefExpectedMerge, nil
	}
	if expectedCommit {
		ancestor, err := integrationIsAncestor(
			ctx, session, current, intent.expectedMerge,
		)
		if err != nil {
			return "", err
		}
		if ancestor {
			return IntegrationRefAncestorDrift, nil
		}
		descendant, err := integrationIsAncestor(
			ctx, session, intent.expectedMerge, current,
		)
		if err != nil {
			return "", err
		}
		if descendant {
			return IntegrationRefDescendantDrift, nil
		}
		return IntegrationRefUnrelatedDrift, nil
	}
	for _, parent := range []GitObjectID{
		intent.expectedFeatureHead, intent.acceptedHead,
	} {
		ancestor, err := integrationIsAncestor(
			ctx, session, current, parent,
		)
		if err != nil {
			return "", err
		}
		if ancestor {
			return IntegrationRefAncestorDrift, nil
		}
	}
	for _, parent := range []GitObjectID{
		intent.expectedFeatureHead, intent.acceptedHead,
	} {
		descendant, err := integrationIsAncestor(
			ctx, session, parent, current,
		)
		if err != nil {
			return "", err
		}
		if descendant {
			return IntegrationRefDescendantDrift, nil
		}
	}
	return IntegrationRefUnrelatedDrift, nil
}

func integrationDriftError(
	inspection IntegrationGitInspection,
) error {
	return fmt.Errorf(
		"feature ref drifted to %s (%s); refusing integration recovery",
		inspection.featureHead, inspection.refState,
	)
}
