// Copyright 2016 The Chromium Authors. All rights reserved.
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package main

import (
	"fmt"
	"os"
	"strings"

	"v.io/jiri/gerrit"
)

type clNumber int
type patchset int

// A workflow handles the interaction with the Continuous Integrations system.
type workflow interface {
	// listTestsToRun should return a list of test names to run.
	listTestsToRun() (testNames []string)

	// removeOutdatedBuilds should halt and remove all ongoing builds that are older
	// than the given valid ones.
	removeOutdatedBuilds(validCLs map[clNumber]patchset) []error

	// addPresubmitTestBuild should start the given tests with the given CLs.
	addPresubmitTestBuild(cls gerrit.CLList, testNames []string) error

	// postResults should publish message for the given refs.  Verified indicates whether
	// the presubmit tool believes this CL is OK to submit.
	postResults(message string, clRefs []string, verified bool) error
}

// clsSender handles the workflow and business logic of sending groups of related CLs
// to presubmit testing.  The interaction with the CI system is mocked out for testing
// and (in theory) modularity WRT adopting new CI systems.
type clsSender struct {
	clLists []gerrit.CLList
	clsSent int
	worker  workflow
}

// sendCLstoPresubmitTest sends the set of CLLists for presubmit testing.
func (s *clsSender) sendCLsToPresubmitTest() error {
	for _, curCLList := range s.clLists {
		cls := combineCLList(curCLList)
		if len(cls.clMap) == 0 {
			fmt.Println("Skipping empty CL set")
			continue
		}

		// Don't send the CLs to presubmit-test if at least one of them have PresubmitTest: none.
		if cls.skipPresubmitTest {
			fmt.Printf("Skipping %s because presubmit=none\n", cls.clString)
			if err := s.worker.postResults("Presubmit tests skipped.\n", cls.refs, true); err != nil { // Verified +1
				return err
			}
			continue
		}

		// Fetch the list of tests we want to run.
		tests := s.worker.listTestsToRun()

		// Skip if there are no tests.
		if len(tests) == 0 {
			fmt.Printf("Skipping %s because no tests found\n", cls.clString)
			if err := s.worker.postResults("No tests found.\n", cls.refs, true); err != nil { // Verified +1
				return err
			}
			continue
		}

		// Only test code submitted by trusted contributors.
		if !cls.hasTrustedOwner {
			fmt.Printf("Skipping %s because the owner is an external contributor\n", cls.clString)
			if err := s.worker.postResults("Tell Freenode#fuchsia to kick the presubmit tests.\n", cls.refs, false); err != nil {
				return err
			}
			continue
		}

		// Cancel any previous tests from old patch sets that may still be running.
		for _, err := range s.worker.removeOutdatedBuilds(cls.clMap) {
			if err != nil {
				fmt.Fprintln(os.Stderr, err) // Not fatal; just log errors.
			}
		}

		// Finally send the CLs to presubmit-test.
		fmt.Printf("Sending %s to presubmit test\n", cls.clString)
		if err := s.worker.addPresubmitTestBuild(curCLList, tests); err != nil {
			fmt.Fprintf(os.Stderr, "addPresubmitTestBuild failed: %v\n", err)
		} else {
			s.clsSent += len(curCLList)
		}
	}
	return nil
}

// multiPartCLInfo collects all the data about a list of CLs that we run at the same time.
// Because a single logical change may be broken up into multiple individual CLs, we have to
// run tests on many CLs at once.  Colloquially this is referred to as a "multi part" CL.
type multiPartCLInfo struct {
	clMap             map[clNumber]patchset
	clString          string
	skipPresubmitTest bool
	hasTrustedOwner   bool
	refs              []string
}

// combineCLList combines the given individual CLs into a single multiPartCLInfo.
func combineCLList(curCLList gerrit.CLList) multiPartCLInfo {
	result := multiPartCLInfo{}
	result.hasTrustedOwner = true
	result.clMap = map[clNumber]patchset{}
	clStrings := []string{}

	for _, curCL := range curCLList {
		// If we have a malformed ref string, we can't recover.  Must abort.
		cl, ps, err := gerrit.ParseRefString(curCL.Reference())
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return multiPartCLInfo{}
		}

		// Check if the author has indicated this change should avoid presubmit tests.
		if curCL.PresubmitTest == gerrit.PresubmitTestTypeNone {
			result.skipPresubmitTest = true
		}

		// If any of the CLs aren't trusted, mark the whole list as untrusted.
		if !isTrustedContributor(curCL.OwnerEmail()) {
			result.hasTrustedOwner = false
		}

		clStrings = append(clStrings, formatCLString(cl, ps))
		result.clMap[clNumber(cl)] = patchset(ps)
		result.refs = append(result.refs, curCL.Reference())
	}

	result.clString = strings.Join(clStrings, ", ")
	return result
}

// isTrustedContributor returns whether this owner is a "trusted" contributor.  Being trusted
// controls whether we automatically run your code through tests.  Currently this function
// just checks if you're submitting from a google.com address.  In the future, it could use an
// ACL or something.
func isTrustedContributor(emailAddress string) bool {
	return strings.HasSuffix(emailAddress, "@google.com")
}

// formatCLString formats the given cl and patch numbers into a user-readable description.  V23
// does this as a URL like http://go/vcl/xxxx/yy.  We could do something similar, but don't!
func formatCLString(clNumber int, patchsetNumber int) string {
	return fmt.Sprintf("%d/%d", clNumber, patchsetNumber)
}