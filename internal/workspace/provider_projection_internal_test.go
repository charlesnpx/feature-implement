package workspace

import "testing"

func TestProviderPullRequestIdentityIsScopedPerAttempt(t *testing.T) {
	repository, _ := NewRepositoryIdentity("https://github.com/example/provider-projection.git")
	provider := MustID("github")
	firstPR, _ := newPullRequestIdentity(provider, repository, 71)
	secondPR, _ := newPullRequestIdentity(provider, repository, 72)
	firstAttempt, secondAttempt := MustID("attempt-one"), MustID("attempt-two")
	projection := ProviderRuntimeProjection{
		intents: []ProviderIntentProjection{
			{
				intent: ProviderIntent{kind: ProviderIntentOpenPullRequest, scope: providerIntentScope{attemptID: firstAttempt}},
				status: ProviderIntentSucceeded, result: ProviderResult{pullRequest: firstPR},
			},
			{
				intent: ProviderIntent{kind: ProviderIntentOpenPullRequest, scope: providerIntentScope{attemptID: secondAttempt}},
				status: ProviderIntentSucceeded, result: ProviderResult{pullRequest: secondPR},
			},
		},
	}
	if actual, ok := projection.PullRequestForAttempt(firstAttempt); !ok || actual != firstPR {
		t.Fatalf("first attempt pull request = %#v ok=%v, want %#v", actual, ok, firstPR)
	}
	if actual, ok := projection.PullRequestForAttempt(secondAttempt); !ok || actual != secondPR {
		t.Fatalf("second attempt pull request = %#v ok=%v, want %#v", actual, ok, secondPR)
	}
	if actual, ok := projection.PullRequest(); ok || !actual.IsZero() {
		t.Fatalf("unscoped pull request leaked across attempts: %#v ok=%v", actual, ok)
	}
}
