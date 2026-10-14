package slurmjob

import "strings"

// never are pending reasons that mean the job cannot start as submitted,
// with what to tell the user. Everything else, unknown reasons included,
// is waiting; the deadline bounds the wait. Move a reason here when it
// turns up on the cluster and never clears.
var never = map[string]string{
	"PartitionTimeLimit":       "its time limit is longer than the partition allows; ask for less with --time",
	"PartitionNodeLimit":       "it asks for more nodes than the partition allows",
	"BadConstraints":           "no node can meet its constraints",
	"InvalidAccount":           "your Slurm account cannot use this partition; choose another with --partition",
	"InvalidQOS":               "its QOS is not valid for you",
	"PartitionDown":            "the partition is down",
	"PartitionInactive":        "the partition is inactive",
	"DependencyNeverSatisfied": "it depends on a job that cannot succeed",
	"JobHeldAdmin":             "an administrator has held it",
}

// NeverClears reports whether a pending reason means the job can never
// start as submitted, and why. Per-job QOS and association limits never
// clear; per-user ones do, when another of the user's jobs ends.
func NeverClears(reason string) (string, bool) {
	if m, ok := never[reason]; ok {
		return m, true
	}
	if (strings.HasPrefix(reason, "QOSMax") || strings.HasPrefix(reason, "AssocMax")) && strings.Contains(reason, "PerJob") {
		return "it asks for more than a per-job limit (" + reason + ") allows", true
	}
	return "", false
}

// hints explain sbatch's refusals (seen on the OSU cluster, Slurm 25.11).
var hints = []struct{ sub, hint string }{
	{"Requested nodes not in this partition", "that node is not in the partition; press Tab after --node to list nodes and their partitions"},
	{"Invalid node name specified", "no node has that name; press Tab after --node to list them"},
	{"More processors requested than permitted", "the node or partition has fewer CPUs than --cpus asks for"},
	{"Invalid account or account/partition combination specified", "your Slurm account cannot use that partition; choose another with --partition"},
	{"Requested time limit is invalid", "the time limit is over the partition's limit; ask for less with --time"},
}

// Explain turns an sbatch error message into advice, or "" when there is
// none to add.
func Explain(msg string) string {
	for _, h := range hints {
		if strings.Contains(msg, h.sub) {
			return h.hint
		}
	}
	return ""
}
