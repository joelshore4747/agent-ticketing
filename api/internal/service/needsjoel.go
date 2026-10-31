package service

import (
	"regexp"
	"strconv"
	"strings"

	"github.com/joelshore/ticketing/api/internal/apperr"
)

// Rule 27. A `needs-joel` ticket is the one thing in this system that interrupts a human, so it has
// to be followable without reading code or history. TP-216 is why: it sat in Joel's Needs you asking
// someone to "re-read go-service-layer against TP-184's diff and re-pin it", saying nothing about
// where the document was, what to click, or whether it was even his job (it wasn't, and it had
// already been done). The check is mechanical rather than remembered because every agent file
// already told agents to write the section and TP-216 was filed anyway.

// needsJoelTag is the tag that asks for the human, named once so this rule and the starter list in
// tags.go cannot drift apart. Its description points at the "Only Joel" section of the-agents
// guidance; the moves themselves are written down once, there.
const needsJoelTag = "needs-joel"

var (
	// The heading, as a markdown heading or a bold line. Trailing words are allowed so
	// "## What you do (about 3 minutes)" counts: the promise is steps, not a fixed title.
	whatYouDoHeading = regexp.MustCompile(`(?mi)^[ \t]{0,3}(?:#{1,6}[ \t]*|\*\*[ \t]*|__[ \t]*)what you do\b`)
	// A markdown heading ends the section, and only a heading does. A bold line looks like the next
	// section but is not reliably one: TP-242 writes its steps as "**1. `test_loop.py`.**" and puts
	// a bold "**What counts as OK:**" line between two of them. Ending the section there would
	// refuse a ticket that is already exactly what this rule is asking for.
	anyHeading = regexp.MustCompile(`(?m)^[ \t]{0,3}#{1,6}[ \t]`)
	// A numbered step, with the optional bold or italic markers that get wrapped around one.
	numberedStep = regexp.MustCompile(`^[ \t]{0,3}(?:[*_]{1,2}[ \t]*)?(\d{1,2})[.)][ \t]+\S`)
)

// whatYouDoProblem reports why a description fails rule 27, or "" when it passes. It names the one
// thing to fix rather than the whole rule, so the 400 is a next action and not a specification.
func whatYouDoProblem(description string) string {
	loc := whatYouDoHeading.FindStringIndex(description)
	if loc == nil {
		return `it has no "What you do" section (a markdown heading or bold line, for example "## What you do")`
	}

	// Steps are what follows the heading, up to the section that follows it.
	body := description[loc[1]:]
	if nl := strings.IndexByte(body, '\n'); nl >= 0 {
		body = body[nl+1:]
	} else {
		body = ""
	}

	var numbers []int
	for _, line := range strings.Split(body, "\n") {
		step := numberedStep.FindStringSubmatch(line)
		if step == nil {
			if anyHeading.MatchString(line) {
				break
			}
			continue
		}
		n, err := strconv.Atoi(step[1])
		if err != nil {
			continue
		}
		numbers = append(numbers, n)
	}

	switch {
	case len(numbers) == 0:
		return `its "What you do" section has no numbered steps`
	case numbers[0] != 1:
		return `its "What you do" section starts at step ` + strconv.Itoa(numbers[0]) + `, not 1`
	case len(numbers) < 2:
		return `its "What you do" section has only one numbered step`
	}
	return ""
}

// checkNeedsJoel is rule 27 at both call sites: nothing reaches Joel without numbered steps. It is
// given the tags and description the ticket would end up with, so removing the tag, or adding the
// section, both make it pass. subject names the ticket in the error — its key on an update, its
// title on a create, where there is no key yet.
func checkNeedsJoel(tags []string, description, subject string) error {
	if !hasNeedsJoelTag(tags) {
		return nil
	}
	problem := whatYouDoProblem(description)
	if problem == "" {
		return nil
	}
	return apperr.BadRequestf("%s is tagged %s, so it has to say what Joel does, but %s. "+
		"Add numbered steps: where to go (the page, file or command), what to look at and what counts "+
		"as OK, what to click or run, what happens next, and what to do if it is not OK",
		subject, needsJoelTag, problem)
}

func hasNeedsJoelTag(tags []string) bool {
	for _, tag := range tags {
		if strings.ToLower(strings.TrimSpace(tag)) == needsJoelTag {
			return true
		}
	}
	return false
}
