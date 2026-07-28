package workspace

import (
	"strings"
	"testing"
)

func TestReviewFindingIdentityRejectsDuplicatesAndTamperedCollisions(t *testing.T) {
	request := DigestBytes([]byte("review request"))
	isolation := StrictReviewIsolationProof()
	first, err := NewReviewFinding(ReviewFindingOptions{
		Severity: ReviewSeverityHigh, Category: MustID("correctness"), Path: "review.go", Line: 1,
		Summary: "first", EvidenceDigest: DigestBytes([]byte("first evidence")),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := NewReviewResultSubmission(ReviewResultSubmissionOptions{
		RequestDigest: request, ReviewerInstance: MustID("reviewer"), Status: ReviewResultCompleted,
		Findings: []ReviewFinding{first, first}, Isolation: isolation,
	}); err == nil || !strings.Contains(err.Error(), "duplicate review finding") {
		t.Fatalf("duplicate finding error = %v", err)
	}

	second, err := NewReviewFinding(ReviewFindingOptions{
		Severity: ReviewSeverityHigh, Category: MustID("correctness"), Path: "review.go", Line: 2,
		Summary: "second", EvidenceDigest: DigestBytes([]byte("second evidence")),
	})
	if err != nil {
		t.Fatal(err)
	}
	second.id = first.id
	if _, err := NewReviewResultSubmission(ReviewResultSubmissionOptions{
		RequestDigest: request, ReviewerInstance: MustID("reviewer"), Status: ReviewResultCompleted,
		Findings: []ReviewFinding{first, second}, Isolation: isolation,
	}); err == nil || !strings.Contains(err.Error(), "engine-derived content") {
		t.Fatalf("tampered finding collision error = %v", err)
	}
}
