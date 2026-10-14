package slurmjob

// EnsureScript finds the user's job, or submits one, and waits for it to
// run (see the spec's "The ensure script" and this package's tests for the
// protocol). It runs on the gateway as sh -c '...' under any login shell,
// so it is one line with no single quote and no "!". Its inputs arrive on
// stdin, one per line: mode, name, cpus, time, deadline, partition, node,
// account ("-": one is among the extra arguments), then extra sbatch arguments, then a line "@script" and the batch script
// (the rest of stdin), which sbatch reads from its own stdin. The job's
// output goes to $S/job-<id>.log, S being the hpctun state directory; "@new"
// is sent when this run creates $S. $S is made mode 700, so the log (which
// may hold a device code) is private. sbatch keeps the caller's umask:
// Slurm gives it to the job, and so to VS Code's terminals. name may list
// several comma-separated job names; any of them is found, and a new job gets the first (squeue -n
// already takes a comma-separated list). It never cancels its own job when
// its connection closes: a VS Code retry reuses the job, and the deadline
// removes it if nobody comes back.
//
// With no account given and a partition known (given, or the node's), it
// reads the partition's AllowAccounts and DenyAccounts: when they restrict
// accounts and the user's default account is not let in, it takes the
// first of the user's accounts (sacctmgr) that is, sending "@account
// <acct>|<part>", or fails with "noaccount" (spec: "Accounts").
//
// While it waits it polls squeue every 5 seconds for the first minute, then
// every 15, and writes "@alive" to stderr after each poll: once the
// connection is gone that write fails, and the script stops instead of
// polling until the job starts. (A shell whose printf ignores EPIPE, such
// as ksh93 as /bin/sh with SIGPIPE ignored, would not notice.) Held states
// such as REQUEUE_HOLD are waiting, like PENDING. squeue with no state for
// the job is the job ending only when sacct names a state it has ended in;
// a job sacct still has queued or running (or cannot see) gets 12 more
// polls, for a busy slurmctld, before the script gives up on it as "lost";
// find, which never waits, reports it lost at once.
const EnsureScript = `IFS= read -r mode; IFS= read -r name; IFS= read -r cpus; IFS= read -r tl; ` +
	`IFS= read -r deadline; IFS= read -r part; IFS= read -r node; IFS= read -r acct; ` +
	`set --; while IFS= read -r a; do [ "$a" = @script ] && break; set -- "$@" "$a"; done; script=$(cat); ` +
	`ev() { printf "@%s\n" "$*" >&2; }; ` +
	`list=$(squeue -h -u "$USER" -n "$name" -t PENDING,RUNNING,CONFIGURING,SUSPENDED,REQUEUED,RESIZING -o %i --sort=i) || exit 1; ` +
	`id=$(printf "%s\n" "$list" | sed -n 1p); extra=$(printf "%s\n" "$list" | sed -n "2,\$p" | tr "\n" " "); ` +
	`if [ -n "$extra" ]; then scancel $extra; ev "extra $extra"; fi; ` +
	`if [ -z "$id" ]; then ` +
	`if [ "$mode" = find ]; then echo none; exit 3; fi; ` +
	`if [ -n "$node" ] && [ -z "$part" ]; then ` +
	`parts=$(sinfo -h -N -n "$node" -o %P | tr -d "*" | sort -u | tr "\n" " "); n=$(printf "%s\n" $parts | grep -c .); ` +
	`case $n in 0) ev "error nopart|$node"; exit 2;; 1) part=${parts%% *};; *) ev "error manypart|$node|$parts"; exit 2;; esac; fi; ` +
	`if [ "$acct" = - ]; then acct=; elif [ -z "$acct" ] && [ -n "$part" ]; then ` +
	`pl=$(scontrol -o show partition "$part" 2>/dev/null); allow=$(printf "%s\n" $pl | sed -n "s/^AllowAccounts=//p"); deny=$(printf "%s\n" $pl | sed -n "s/^DenyAccounts=//p"); ` +
	`okacct() { case ",${allow:-ALL}," in ",ALL,"|*",$1,"*) ;; *) return 1;; esac; case ",$deny," in *",$1,"*) return 1;; esac; return 0; }; ` +
	`case "${allow:-ALL}|$deny" in "ALL|") ;; *) def=$(sacctmgr -nP show user "$USER" format=defaultaccount 2>/dev/null | sed -n 1p); ` +
	`if [ -n "$def" ] && okacct "$def"; then :; else for x in $(sacctmgr -nP show assoc user="$USER" format=account 2>/dev/null | sort -u); do if okacct "$x"; then acct=$x; break; fi; done; ` +
	`if [ -n "$acct" ]; then ev "account $acct|$part"; else ev "error noaccount|$part|${allow:-ALL}"; exit 2; fi; fi;; esac; fi; ` +
	`S=${XDG_STATE_HOME:-$HOME/.local/state}/hpctun/vscode; if [ -d "$S" ]; then :; else mkdir -p "$S" || { ev "error dir|$S"; exit 2; }; ev new; fi; chmod 700 "$S"; ` +
	`set -- -J "${name%%,*}" -N 1 -c "$cpus" -t "$tl" "--deadline=$deadline" -o "$S/job-%j.log" ${part:+-p "$part"} ${node:+-w "$node"} ${acct:+-A "$acct"} "$@"; ` +
	`if t=$(printf "%s\n" "$script" | sbatch --test-only "$@" 2>&1); then :; else ev "error test|$(printf "%s" "$t" | tr "\n" " ")"; exit 2; fi; ` +
	`if id=$(printf "%s\n" "$script" | sbatch --parsable "$@"); then :; else ev "error submit|sbatch failed"; exit 2; fi; ` +
	`id=${id%%;*}; ev "submitted $id|$(printf "%s" "$t" | tr "\n" " " | sed "s/ *\$//")"; ` +
	`else ev "reuse $id"; fi; ` +
	`last=; i=0; miss=0; while :; do l=$(squeue -h -j "$id" -o "%i|%T|%N|%L|%r" 2>/dev/null); st=$(printf "%s" "$l" | cut -d"|" -f2); ` +
	`case $st in RUNNING) echo "$l"; exit 0;; ` +
	`PENDING|CONFIGURING|SUSPENDED|REQUEUED|RESIZING|REQUEUE_HOLD|REQUEUE_FED|RESV_DEL_HOLD|SPECIAL_EXIT|STOPPED|SIGNALING) ` +
	`if [ "$mode" = find ]; then echo "$l"; exit 0; fi; miss=0; ` +
	`cur=$(printf "%s" "$l" | cut -d"|" -f2,5); [ "$cur" = "$last" ] || { ev "state $cur"; last=$cur; };; ` +
	`*) s=$(sacct -X -n -P -j "$id" --format=State 2>/dev/null | sed -n 1p); ` +
	`case "$st|$s" in "|"|"|PENDING"|"|RUNNING"|"|CONFIGURING"|"|SUSPENDED"|"|REQUEUED"|"|RESIZING"|"|REQUEUE_HOLD"|"|REQUEUE_FED"|"|RESV_DEL_HOLD"|"|SPECIAL_EXIT"|"|STOPPED"|"|SIGNALING") ` +
	`miss=$((miss+1)); [ "$mode" = ensure ] && [ $miss -lt 12 ] || { ev "lost ${s:-UNKNOWN}"; exit 1; };; ` +
	`*) ev "gone ${s:-${st:-UNKNOWN}}"; exit 1;; esac;; esac; ` +
	`ev alive || exit 1; i=$((i+1)); if [ $i -le 12 ]; then sleep 5; else sleep 15; fi; done`
