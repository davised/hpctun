package slurmjob

import (
	"strings"
	"testing"
)

func TestNeverClears(t *testing.T) {
	for _, r := range []string{"PartitionTimeLimit", "BadConstraints", "InvalidAccount", "DependencyNeverSatisfied",
		"QOSMaxWallDurationPerJobLimit", "AssocMaxCpuPerJobLimit"} {
		if msg, ok := NeverClears(r); !ok || msg == "" {
			t.Errorf("%s should never clear", r)
		}
	}
	for _, r := range []string{"None", "Priority", "Resources", "ReqNodeNotAvail", "Dependency", "BeginTime",
		"QOSMaxJobsPerUserLimit", "AssocGrpCpuLimit", "SomethingNew"} {
		if _, ok := NeverClears(r); ok {
			t.Errorf("%s should be waiting", r)
		}
	}
}

func TestExplain(t *testing.T) {
	if h := Explain("Requested nodes not in this partition"); !strings.Contains(h, "--node") {
		t.Errorf("got %q", h)
	}
	if h := Explain("Invalid account or account/partition combination specified"); !strings.Contains(h, "--partition") {
		t.Errorf("got %q", h)
	}
	if h := Explain("something else"); h != "" {
		t.Errorf("got %q", h)
	}
}
