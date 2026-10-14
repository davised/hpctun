package sshsetup

import (
	"context"

	"hpctun/internal/sshctl"
)

// SSHAdd runs `ssh-add -l` and returns its exit status: 0 keys listed, 1 no
// keys, 2 no agent to talk to.
type SSHAdd func(ctx context.Context) (int, error)

// RunnerSSHAdd runs ssh-add -l through r.
func RunnerSSHAdd(r sshctl.Runner) SSHAdd {
	return func(ctx context.Context) (int, error) {
		res, err := r.Run(ctx, "ssh-add", "-l")
		return res.ExitCode, err
	}
}

// AgentNote is CheckAgent's finding when ssh-add cannot reach an agent.
const AgentNote = "the ssh-agent service is not running, so ssh asks for your key's passphrase every time. " +
	"Enable it once in an administrator PowerShell: " +
	"`Get-Service ssh-agent | Set-Service -StartupType Automatic; Start-Service ssh-agent`. " +
	"After that, AddKeysToAgent keeps your key in the agent, across reboots."

// CheckAgent notes a missing ssh agent. It is for Windows, where the agent
// is a service that starts disabled. An ssh-add that cannot run is not
// reported: there is nothing more useful to say than ssh itself will.
func CheckAgent(ctx context.Context, sshAdd SSHAdd) []Finding {
	code, err := sshAdd(ctx)
	if err != nil || code != 2 {
		return nil
	}
	return []Finding{{Note, AgentNote}}
}
