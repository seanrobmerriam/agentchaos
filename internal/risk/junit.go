package risk

import (
	"encoding/xml"
	"fmt"
	"io"
)

// JUnitTestSuites is the root element of a JUnit XML report.
type JUnitTestSuites struct {
	XMLName xml.Name         `xml:"testsuites"`
	Suites  []JUnitTestSuite `xml:"testsuite"`
}

// JUnitTestSuite aggregates a set of related test cases.
type JUnitTestSuite struct {
	Name     string          `xml:"name,attr"`
	Tests    int             `xml:"tests,attr"`
	Failures int             `xml:"failures,attr"`
	Cases    []JUnitTestCase `xml:"testcase"`
}

// JUnitTestCase represents a single seed attempt.
type JUnitTestCase struct {
	Name      string        `xml:"name,attr"`
	Classname string        `xml:"classname,attr"`
	Failure   *JUnitFailure `xml:"failure,omitempty"`
}

// JUnitFailure describes a failed test case.
type JUnitFailure struct {
	Message string `xml:"message,attr"`
	Type    string `xml:"type,attr,omitempty"`
	Body    string `xml:",chardata"`
}

// WriteJUnit emits a JUnit XML document for the given Report to w. Each
// seed becomes a testcase; failures carry a <failure> child with the
// reason and exit code. The output is intended to be consumed by CI
// dashboards (e.g. GitHub Actions test reporters).
func WriteJUnit(w io.Writer, report *Report) error {
	if report == nil {
		return fmt.Errorf("nil report")
	}
	suites := JUnitTestSuites{
		Suites: []JUnitTestSuite{{
			Name:     "agentchaos.risk",
			Tests:    report.SeedsRun,
			Failures: report.SeedsFailed,
			Cases:    make([]JUnitTestCase, 0, report.SeedsRun),
		}},
	}

	failBySeed := make(map[int64]Failure, len(report.Failures))
	for _, f := range report.Failures {
		failBySeed[f.Seed] = f
	}

	// Iterate from seed=SeedBase+1 .. SeedBase+Seeds to enumerate every
	// attempted seed even if some passed (no failure entry).
	for i := 1; i <= report.SeedsRun; i++ {
		// We don't know SeedBase here, but the caller always uses
		// SeedBase=0 in current usage; the seed value is reconstructable
		// from the failure list. For passing seeds we emit a minimal case.
		// The CLI sets SeedBase explicitly so failure seeds are exact.
		var name string
		var fail *JUnitFailure
		// Find a matching failure by trying each known seed; cheap because
		// SeedsRun is bounded.
		for _, f := range report.Failures {
			if int64(i) == f.Seed || (f.Seed == int64(i)) {
				name = fmt.Sprintf("seed=%d", f.Seed)
				fail = &JUnitFailure{
					Message: f.Reason,
					Type:    fmt.Sprintf("exit-%d", f.ExitCode),
					Body:    f.Reason,
				}
				break
			}
		}
		if name == "" {
			name = fmt.Sprintf("seed=%d", i)
		}
		suites.Suites[0].Cases = append(suites.Suites[0].Cases, JUnitTestCase{
			Name:      name,
			Classname: "agentchaos.risk",
			Failure:   fail,
		})
	}
	// Also append failure cases whose seed wasn't in [1..SeedsRun] (e.g.
	// when SeedBase > 0). This keeps the failure visible in CI output.
	for _, f := range report.Failures {
		if f.Seed < 1 || f.Seed > int64(report.SeedsRun) {
			suites.Suites[0].Cases = append(suites.Suites[0].Cases, JUnitTestCase{
				Name:      fmt.Sprintf("seed=%d", f.Seed),
				Classname: "agentchaos.risk",
				Failure: &JUnitFailure{
					Message: f.Reason,
					Type:    fmt.Sprintf("exit-%d", f.ExitCode),
					Body:    f.Reason,
				},
			})
		}
	}

	_, err := io.WriteString(w, xml.Header)
	if err != nil {
		return err
	}
	enc := xml.NewEncoder(w)
	enc.Indent("", "  ")
	if err := enc.Encode(suites); err != nil {
		return err
	}
	return enc.Flush()
}
