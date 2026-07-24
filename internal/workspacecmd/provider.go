package workspacecmd

import (
	"context"
	"fmt"

	"github.com/charlesnpx/feature-implement/internal/workspace"
)

type providerReserveInput struct {
	SchemaVersion       int    `json:"schema_version"`
	OccurredAt          string `json:"occurred_at"`
	Kind                string `json:"kind"`
	AttemptID           string `json:"attempt_id"`
	Branch              string `json:"branch"`
	Head                string `json:"head"`
	Tree                string `json:"tree"`
	ExpectedRemoteHead  string `json:"expected_remote_head,omitempty"`
	ExpectRemoteAbsent  bool   `json:"expect_remote_absent,omitempty"`
	IntegrationBaseHead string `json:"integration_base_head,omitempty"`
	Title               string `json:"title,omitempty"`
	Body                string `json:"body,omitempty"`
}

type providerIntentInput struct {
	SchemaVersion int    `json:"schema_version"`
	OccurredAt    string `json:"occurred_at"`
	IntentID      string `json:"intent_id"`
}

type ProviderCommandResult struct {
	SchemaVersion int                       `json:"schema_version"`
	Status        string                    `json:"status"`
	Action        string                    `json:"action"`
	Detail        any                       `json:"detail,omitempty"`
	Report        workspace.WorkspaceReport `json:"report"`
}

func executeProvider(ctx context.Context, bundle workspace.WorkspaceBundle, options Options) (any, error) {
	journal, _, err := openWritableJournal(options)
	if err != nil {
		return nil, err
	}
	defer journal.Close()
	definition := bundle.Definition()
	evaluator, err := workspace.NewAuthorizationEvaluator(systemClock{})
	if err != nil {
		return nil, err
	}
	adapter, err := newGitHubProviderAdapter(definition.Workspace())
	if err != nil {
		return nil, err
	}
	broker, err := workspace.NewProviderBroker(definition.Workspace().Provider(), adapter)
	if err != nil {
		return nil, err
	}
	switch options.Subaction {
	case "reserve":
		var input providerReserveInput
		if err := decodeRequest(options.Input, &input); err != nil {
			return nil, err
		}
		occurredAt, err := parseOccurredAt(input.SchemaVersion, input.OccurredAt)
		if err != nil {
			return nil, err
		}
		intent, err := buildProviderIntent(journal, definition, input)
		if err != nil {
			return nil, err
		}
		projection, _, err := workspace.ReserveProviderIntent(journal, definition, evaluator, workspace.ReserveProviderIntentRequest{
			Intent: intent, OccurredAt: occurredAt,
		})
		if err != nil {
			return nil, err
		}
		return providerCommandResult("provider.reserve", providerIntentView(projection), journal, definition)
	case "preflight", "dispatch", "reconcile", "abandon", "authorize-pr":
		var input providerIntentInput
		if err := decodeRequest(options.Input, &input); err != nil {
			return nil, err
		}
		occurredAt, err := parseOccurredAt(input.SchemaVersion, input.OccurredAt)
		if err != nil {
			return nil, err
		}
		intentID, err := parseID(input.IntentID, "intent_id")
		if err != nil {
			return nil, err
		}
		switch options.Subaction {
		case "preflight":
			preflight, _, err := workspace.RecordProviderMergePreflight(ctx, journal, definition, broker, intentID, occurredAt)
			if err != nil {
				return nil, err
			}
			return providerCommandResult("provider.preflight", map[string]any{
				"intent_id": preflight.IntentID().String(), "preflight_digest": preflight.Digest().String(),
				"observation_digest": preflight.ObservationDigest().String(),
			}, journal, definition)
		case "dispatch":
			ticket, err := workspace.AuthorizeProviderIntentDispatch(journal, definition, evaluator, broker, intentID)
			if err != nil {
				return nil, err
			}
			executed, err := workspace.ExecuteProviderIntent(ctx, journal, definition, broker, ticket, occurredAt)
			if err != nil {
				return nil, err
			}
			return providerCommandResult("provider.dispatch", providerResultView(executed.Result()), journal, definition)
		case "reconcile":
			reconciled, err := workspace.ReconcileProviderIntent(ctx, journal, definition, broker, intentID, occurredAt)
			if err != nil {
				return nil, err
			}
			return providerCommandResult("provider.reconcile", providerIntentView(reconciled.Projection()), journal, definition)
		case "abandon":
			projection, _, err := workspace.AbandonProviderIntent(journal, definition, intentID, occurredAt)
			if err != nil {
				return nil, err
			}
			return providerCommandResult("provider.abandon", providerIntentView(projection), journal, definition)
		case "authorize-pr":
			grant, _, err := workspace.RecordProviderPullRequestAuthorization(ctx, journal, definition, broker, intentID, occurredAt)
			if err != nil {
				return nil, err
			}
			return providerCommandResult("provider.authorize-pr", map[string]any{
				"grant_id": grant.GrantID().String(), "request_digest": grant.RequestDigest().String(),
			}, journal, definition)
		}
	default:
		return nil, fmt.Errorf("unsupported workspace provider action %q", options.Subaction)
	}
	panic("unreachable")
}

func buildProviderIntent(
	journal *workspace.WorkspaceJournal,
	definition workspace.EffectiveWorkspaceDefinition,
	input providerReserveInput,
) (workspace.ProviderIntent, error) {
	snapshot, err := journal.ReadSnapshot()
	if err != nil {
		return workspace.ProviderIntent{}, err
	}
	core, err := workspace.RebuildWorkspaceRuntime(snapshot)
	if err != nil {
		return workspace.ProviderIntent{}, err
	}
	attemptID, err := parseID(input.AttemptID, "attempt_id")
	if err != nil {
		return workspace.ProviderIntent{}, err
	}
	attempt, exists := core.Attempt(attemptID)
	if !exists {
		return workspace.ProviderIntent{}, fmt.Errorf("unknown attempt %s", attemptID)
	}
	head, err := parseGitObject(input.Head, "head")
	if err != nil {
		return workspace.ProviderIntent{}, err
	}
	tree, err := parseGitObject(input.Tree, "tree")
	if err != nil {
		return workspace.ProviderIntent{}, err
	}
	frontier, err := workspace.NewAuthorizationFrontier(attempt.Base(), head)
	if err != nil {
		return workspace.ProviderIntent{}, err
	}
	authorization, err := workspace.RebuildAuthorizationRuntime(snapshot, definition)
	if err != nil {
		return workspace.ProviderIntent{}, err
	}
	providers, err := workspace.RebuildProviderRuntime(snapshot, definition)
	if err != nil {
		return workspace.ProviderIntent{}, err
	}
	pullRequest, _ := providers.PullRequestForAttempt(attemptID)
	scope := workspace.ProviderIntentScopeOptions{
		WorkspaceID: definition.Workspace().ID(), Generation: definition.Generation(), AttemptID: attemptID,
		MergeUnit: attempt.MergeUnit(), Repository: definition.Workspace().Repository(), Remote: definition.Workspace().Remote(),
		SerialSegment: attempt.SerialSegment(), Frontier: frontier, PullRequest: pullRequest, Epoch: authorization.State().Epoch(),
	}
	switch workspace.ProviderIntentKind(input.Kind) {
	case workspace.ProviderIntentPush:
		expected := workspace.GitObjectID{}
		if input.ExpectedRemoteHead != "" {
			expected, err = parseGitObject(input.ExpectedRemoteHead, "expected_remote_head")
			if err != nil {
				return workspace.ProviderIntent{}, err
			}
		}
		return workspace.NewProviderPushIntent(workspace.ProviderPushIntentOptions{
			Scope: scope, Branch: input.Branch, ExpectedRemoteHead: expected,
			ExpectRemoteAbsent: input.ExpectRemoteAbsent, Head: head, Tree: tree,
		})
	case workspace.ProviderIntentOpenPullRequest:
		return workspace.NewProviderOpenPullRequestIntent(workspace.ProviderOpenPullRequestIntentOptions{
			Scope: scope, Branch: input.Branch, BaseRef: definition.Workspace().BaseRef(), Head: head, Tree: tree,
			Title: input.Title, Body: input.Body,
		})
	case workspace.ProviderIntentMerge:
		baseHead, err := parseGitObject(input.IntegrationBaseHead, "integration_base_head")
		if err != nil {
			return workspace.ProviderIntent{}, err
		}
		return workspace.NewProviderMergeIntent(workspace.ProviderMergeIntentOptions{
			Scope: scope, Branch: input.Branch, BaseRef: definition.Workspace().BaseRef(), IntegrationBaseHead: baseHead,
			Head: head, Tree: tree, Strategy: workspace.ProviderMergeCommit,
		})
	default:
		return workspace.ProviderIntent{}, fmt.Errorf("unsupported provider intent kind %q", input.Kind)
	}
}

func providerCommandResult(action string, detail any, journal *workspace.WorkspaceJournal, definition workspace.EffectiveWorkspaceDefinition) (ProviderCommandResult, error) {
	base, err := mutationResult(action, journal, definition, nil)
	if err != nil {
		return ProviderCommandResult{}, err
	}
	return ProviderCommandResult{
		SchemaVersion: requestSchemaVersion, Status: base.Status, Action: action, Detail: detail, Report: base.Report,
	}, nil
}

func providerIntentView(projection workspace.ProviderIntentProjection) map[string]any {
	intent := projection.Intent()
	return map[string]any{
		"intent_id": intent.IntentID().String(), "intent_digest": intent.Digest().String(), "kind": intent.Kind(),
		"attempt_id": intent.AttemptID().String(), "plan_id": intent.MergeUnit().PlanID().String(),
		"merge_unit_id": intent.MergeUnit().MergeUnitID().String(), "generation": intent.Generation().String(),
		"status": projection.Status(), "needs_reconciliation": projection.NeedsReconciliation(),
		"idempotency_key": intent.IdempotencyKey().String(),
	}
}

func providerResultView(result workspace.ProviderResult) map[string]any {
	view := map[string]any{
		"intent_id": result.IntentID().String(), "result_digest": result.Digest().String(), "kind": result.Kind(),
		"status": result.Status(), "request_marker": result.RequestMarker(),
	}
	if pullRequest, exists := result.PullRequest(); exists {
		view["pull_request"] = pullRequest.Number()
	}
	if !result.RemoteHead().IsZero() {
		view["remote_head"] = result.RemoteHead().String()
	}
	if !result.MergeCommit().IsZero() {
		view["merge_commit"] = result.MergeCommit().String()
		view["final_base_head"] = result.FinalBaseHead().String()
	}
	return view
}
