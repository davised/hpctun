package slurmjob

import (
	"context"
	"fmt"
	"io"
	"strconv"
	"strings"
)

// Node is a node the user can submit to, as sinfo reports it.
type Node struct {
	Name       string
	Partitions []string
	State      string // sinfo's short state without flag characters, e.g. idle, mix
	CPUs       int
	GRES       string // e.g. gpu:a100:2; empty when none
}

// Partition counts the usable nodes of one partition.
type Partition struct {
	Name        string
	Nodes, Idle int
}

// sinfoScript lists one line per node and partition. Without --all, sinfo
// hides partitions the user's groups cannot use (not ones restricted by
// account: sbatch --test-only catches those).
const sinfoScript = `sinfo -h -N -o "%N|%P|%t|%c|%G"`

// unusable are states a job cannot start in.
var unusable = map[string]bool{"down": true, "drain": true, "drng": true, "fail": true, "failg": true,
	"inval": true, "maint": true, "unk": true, "futr": true, "npc": true}

// Nodes returns sinfoScript's output from the gateway.
func (c Client) Nodes(ctx context.Context) (string, error) {
	out, code, err := c.Exec(ctx, c.remote(sinfoScript), "", io.Discard)
	if err != nil {
		return "", err
	}
	if code != 0 {
		return "", fmt.Errorf("sinfo on %s failed (exit status %d)", c.Gateway, code)
	}
	return out, nil
}

// ParseNodes reads sinfoScript's output, keeping nodes a job could start on,
// each once with all its partitions. A trailing * on a state means the node
// is not responding; on a partition, that it is the default.
func ParseNodes(out string) []Node {
	var nodes []Node
	idx := map[string]int{}
	for _, l := range strings.Split(out, "\n") {
		f := strings.Split(strings.TrimSpace(l), "|")
		if len(f) != 5 {
			continue
		}
		st := strings.TrimRight(f[2], "*~#!%$@^-+")
		if strings.HasSuffix(f[2], "*") || unusable[st] {
			continue
		}
		part := strings.TrimSuffix(f[1], "*")
		if i, ok := idx[f[0]]; ok {
			nodes[i].Partitions = append(nodes[i].Partitions, part)
			continue
		}
		cpus, _ := strconv.Atoi(f[3])
		gres := f[4]
		if gres == "(null)" {
			gres = ""
		}
		idx[f[0]] = len(nodes)
		nodes = append(nodes, Node{Name: f[0], Partitions: []string{part}, State: st, CPUs: cpus, GRES: gres})
	}
	return nodes
}

// Partitions counts nodes per partition, in the order they first appear.
func Partitions(nodes []Node) []Partition {
	var ps []Partition
	idx := map[string]int{}
	for _, n := range nodes {
		for _, p := range n.Partitions {
			i, ok := idx[p]
			if !ok {
				i = len(ps)
				idx[p] = i
				ps = append(ps, Partition{Name: p})
			}
			ps[i].Nodes++
			if n.State == "idle" {
				ps[i].Idle++
			}
		}
	}
	return ps
}
