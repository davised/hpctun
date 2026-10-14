package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"hpctun/internal/copycmd"
	"hpctun/internal/slurmjob"
	"hpctun/internal/state"
)

const (
	sinfoCache    = "sinfo.cache" // state-dir cache of sinfo for completion
	sinfoCacheAge = 2 * time.Minute
)

// codeNodes returns the nodes the user can submit to, from a cache under
// two minutes old or from sinfo on the gateway. Like hpctun cp's completion
// it never prompts: the query runs with BatchMode and a 5-second limit, and
// any failure gives nothing.
func (a *app) codeNodes(ctx context.Context) []slurmjob.Node {
	cfg, err := a.loadConfig()
	if err != nil || cfg.Code == nil || cfg.Gateway == "" {
		return nil
	}
	dir, err := a.stateDir()
	if err != nil {
		return nil
	}
	path := filepath.Join(dir, sinfoCache)
	if fi, err := os.Stat(path); err == nil && time.Since(fi.ModTime()) < sinfoCacheAge {
		if b, err := os.ReadFile(path); err == nil {
			return slurmjob.ParseNodes(string(b))
		}
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	out, err := a.slurm(cfg.Gateway).Nodes(ctx)
	if err != nil {
		return nil
	}
	if state.EnsureDir(dir) == nil {
		_ = os.WriteFile(path, []byte(out), 0o600)
	}
	return slurmjob.ParseNodes(out)
}

func completionCtx(cmd *cobra.Command) context.Context {
	if ctx := cmd.Context(); ctx != nil {
		return ctx
	}
	return context.Background()
}

func (a *app) completeCodeNodes(cmd *cobra.Command, _ []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	var cands []string
	for _, n := range a.codeNodes(completionCtx(cmd)) {
		if !strings.HasPrefix(n.Name, toComplete) {
			continue
		}
		desc := fmt.Sprintf("%s · %s · %d CPUs", strings.Join(n.Partitions, ", "), n.State, n.CPUs)
		if n.GRES != "" {
			desc += " · " + n.GRES
		}
		cands = append(cands, n.Name+"\t"+desc)
	}
	return cands, cobra.ShellCompDirectiveNoFileComp
}

func (a *app) completeCodePartitions(cmd *cobra.Command, _ []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	var cands []string
	for _, p := range slurmjob.Partitions(a.codeNodes(completionCtx(cmd))) {
		if !strings.HasPrefix(p.Name, toComplete) {
			continue
		}
		unit := "nodes"
		if p.Nodes == 1 {
			unit = "node"
		}
		cands = append(cands, fmt.Sprintf("%s\t%d %s, %d idle", p.Name, p.Nodes, unit, p.Idle))
	}
	return cands, cobra.ShellCompDirectiveNoFileComp
}

// completeCodeFolder completes the folder from the cluster: it lists the
// home directory the nodes share on the gateway, as hpctun cp completes remote
// paths, and offers directories only. Only the one folder completes;
// --status and --stop take none. Cobra drops a typed --
// before calling this (and ArgsLenAtDash always counts one of its own), so
// the first word after `hpctun code --` still offers folders; any later
// sbatch argument does not.
func (a *app) completeCodeFolder(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	none := cobra.ShellCompDirectiveNoFileComp
	if len(args) > 0 || strings.HasPrefix(toComplete, "-") {
		return nil, none
	}
	for _, f := range []string{"status", "stop"} {
		if cmd.Flags().Changed(f) {
			return nil, none
		}
	}
	cfg, err := a.loadConfig()
	if err != nil || cfg.Code == nil || cfg.Gateway == "" {
		return nil, none
	}
	var dirs []string
	for _, e := range copycmd.ListRemote(completionCtx(cmd), a.runner(), "", cfg.Gateway, toComplete, 0) {
		if strings.HasSuffix(e, "/") {
			dirs = append(dirs, e)
		}
	}
	return dirs, cobra.ShellCompDirectiveNoSpace | none
}
