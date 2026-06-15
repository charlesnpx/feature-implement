package plan

import (
	"fmt"
	"strings"
)

func epicMarkdown(epic Epic) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Epic %s: %s\n\n", num(epic.Number), epic.Name)
	writeParagraph(&b, epic.Summary)
	writeList(&b, "Constraints", epic.Constraints)
	fmt.Fprintln(&b, "## Features")
	for _, feature := range epic.Features {
		fmt.Fprintf(&b, "- `%s` %s\n", feature.ID, feature.Name)
	}
	return b.String()
}

func featureMarkdown(epic Epic, feature Feature) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Feature %s: %s\n\n", num(feature.Number), feature.Name)
	fmt.Fprintf(&b, "Epic: `%s` %s\n\n", epic.ID, epic.Name)
	writeParagraph(&b, feature.Summary)
	writeList(&b, "Constraints", feature.Constraints)
	fmt.Fprintln(&b, "## Stories")
	for _, story := range feature.Stories {
		fmt.Fprintf(&b, "- `%s` %s\n", story.ID, story.Name)
	}
	return b.String()
}

func storyMarkdown(epic Epic, feature Feature, story Story) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Story %s: %s\n\n", num(story.Number), story.Name)
	fmt.Fprintf(&b, "Epic: `%s` %s\n\n", epic.ID, epic.Name)
	fmt.Fprintf(&b, "Feature: `%s` %s\n\n", feature.ID, feature.Name)
	writeParagraph(&b, story.Summary)
	writeList(&b, "Acceptance Criteria", story.Acceptance)
	writeList(&b, "Implementation Notes", story.Implementation)
	writeList(&b, "Dependencies", story.Dependencies)
	return b.String()
}

func writeParagraph(b *strings.Builder, value string) {
	if strings.TrimSpace(value) != "" {
		fmt.Fprintln(b, value)
		fmt.Fprintln(b)
	}
}

func writeList(b *strings.Builder, title string, values []string) {
	if len(values) == 0 {
		return
	}
	fmt.Fprintf(b, "## %s\n\n", title)
	for _, value := range values {
		fmt.Fprintf(b, "- %s\n", value)
	}
	fmt.Fprintln(b)
}
