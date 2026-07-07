package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/chzyer/readline"
	"github.com/google/uuid"
	"golang.org/x/term"

	"github.com/rebuno/kernel/internal/domain"
	"github.com/rebuno/kernel/internal/kernel"
)

// isInteractive reports whether stdin is a terminal. When it isn't (piped
// input, a file or /dev/null, docker without a TTY, CI) the kernel serves
// without starting the REPL.
func isInteractive() bool {
	return term.IsTerminal(int(os.Stdin.Fd()))
}

// runREPL reads commands from stdin and drives the kernel in-process. It shares
// the same store as the running HTTP server, so executions created here are
// dispatched by the server's scheduler. Leaving the REPL (quit or EOF) calls
// shutdown to stop the kernel.
func runREPL(ctx context.Context, k *kernel.Kernel, shutdown context.CancelFunc) {
	defer shutdown()
	fmt.Println("  repl      type 'help' for commands, 'quit' to stop")

	rl, err := readline.NewEx(&readline.Config{
		Prompt:          "rebuno> ",
		HistoryFile:     replHistoryFile(),
		InterruptPrompt: "^C",
		EOFPrompt:       "quit",
	})
	if err != nil {
		runREPLBasic(ctx, k)
		return
	}
	defer rl.Close()

	for {
		fmt.Fprintln(rl.Stdout())
		line, err := rl.Readline()
		if err != nil {
			// Ctrl-C on a non-empty line clears it and keeps going; on an empty
			// line, or Ctrl-D (io.EOF), it stops the REPL and shuts down.
			if err == readline.ErrInterrupt && line != "" {
				continue
			}
			return
		}
		line = strings.TrimSpace(line)
		if line != "" && dispatchCmd(ctx, k, line) {
			return // quit
		}
		select {
		case <-ctx.Done():
			return
		default:
		}
	}
}

func runREPLBasic(ctx context.Context, k *kernel.Kernel) {
	sc := bufio.NewScanner(os.Stdin)
	fmt.Print("\nrebuno> ")
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line != "" && dispatchCmd(ctx, k, line) {
			return // quit
		}
		select {
		case <-ctx.Done():
			return
		default:
		}
		fmt.Print("rebuno> ")
	}
	fmt.Println() // newline after Ctrl-D
}

// replHistoryFile is where readline persists command history between sessions.
// An empty string (no home dir) disables persistence but keeps in-session history.
func replHistoryFile() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".rebuno_repl_history")
}

func dispatchCmd(ctx context.Context, k *kernel.Kernel, line string) (quit bool) {
	fields := strings.Fields(line)
	switch fields[0] {
	case "help", "?":
		printHelp()
	case "quit", "exit":
		return true
	case "agent", "agents":
		cmdAgent(ctx, k, fields)
	case "exec", "execution", "executions":
		cmdExec(ctx, k, fields, line)
	default:
		fmt.Printf("  unknown command %q — type 'help'\n", fields[0])
	}
	return false
}

func cmdAgent(ctx context.Context, k *kernel.Kernel, fields []string) {
	sub := "ls"
	if len(fields) >= 2 {
		sub = fields[1]
	}
	switch sub {
	case "ls", "list":
		agents, _ := k.ListAgents(ctx)
		if len(agents) == 0 {
			fmt.Println("  (no agents registered — use 'agent add <config.yaml>')")
			return
		}
		fmt.Printf("  %-20s %-34s %s\n", "ID", "WEBHOOK", "POLICY")
		for _, a := range agents {
			pol := "none"
			if a.PolicyBundle != "" {
				pol = fmt.Sprintf("%d bytes", len(a.PolicyBundle))
			}
			fmt.Printf("  %-20s %-34s %s\n", a.ID, a.WebhookURL, pol)
		}
	case "get":
		if len(fields) < 3 {
			fmt.Println("  usage: agent get <id>")
			return
		}
		a, err := k.GetAgent(ctx, fields[2])
		if err != nil {
			fmt.Println("  error:", err)
			return
		}
		fmt.Printf("  id          %s\n", a.ID)
		fmt.Printf("  webhook     %s\n", a.WebhookURL)
		fmt.Printf("  registered  %s\n", a.RegisteredAt.Format(time.RFC3339))
		if a.PolicyBundle == "" {
			fmt.Println("  policy      none (permissive)")
		} else {
			fmt.Printf("  policy      (%d bytes)\n", len(a.PolicyBundle))
			for _, ln := range strings.Split(strings.TrimRight(a.PolicyBundle, "\n"), "\n") {
				fmt.Printf("    │ %s\n", ln)
			}
		}
	case "add":
		if len(fields) < 3 {
			fmt.Println("  usage: agent add <config-file.yaml>")
			return
		}
		agents, err := loadAgentConfig(fields[2])
		if err != nil {
			fmt.Println("  error:", err)
			return
		}
		if err := registerAgents(ctx, k, agents); err != nil {
			fmt.Println("  error:", err)
			return
		}
		fmt.Printf("  registered %d agent(s) from %s\n", len(agents), fields[2])
	case "rm", "delete":
		if len(fields) < 3 {
			fmt.Println("  usage: agent rm <id>")
			return
		}
		if err := k.DeleteAgent(ctx, fields[2]); err != nil {
			fmt.Println("  error:", err)
			return
		}
		fmt.Printf("  deleted %s\n", fields[2])
	default:
		fmt.Printf("  unknown agent subcommand %q (ls, get, add, rm)\n", sub)
	}
}

func cmdExec(ctx context.Context, k *kernel.Kernel, fields []string, line string) {
	sub := "ls"
	if len(fields) >= 2 {
		sub = fields[1]
	}
	switch sub {
	case "ls", "list":
		page, err := k.ListExecutions(ctx, domain.ExecutionFilter{})
		if err != nil {
			fmt.Println("  error:", err)
			return
		}
		if len(page.Executions) == 0 {
			fmt.Println("  (no executions — use 'exec create <agent> [json]')")
			return
		}
		fmt.Printf("  %-10s %-20s %-11s %s\n", "ID", "AGENT", "STATUS", "AGE")
		for _, e := range page.Executions {
			fmt.Printf("  %-10s %-20s %-11s %s\n", shortID(e.ID), e.AgentID, e.Status, age(e.CreatedAt))
		}
		if page.NextCursor != "" {
			fmt.Println("  … (more — only the most recent page is shown)")
		}
	case "create":
		// Everything after "<cmd> create <agentID>" is the JSON input, kept raw
		// so spaces inside the JSON survive.
		rest := strings.TrimSpace(line)
		rest = strings.TrimSpace(strings.TrimPrefix(rest, fields[0]))
		rest = strings.TrimSpace(strings.TrimPrefix(rest, "create"))
		agentID, input, _ := strings.Cut(rest, " ")
		input = strings.TrimSpace(input)
		if agentID == "" {
			fmt.Println("  usage: exec create <agent-id> [json-input]")
			return
		}
		if input == "" {
			input = "{}"
		}
		if !json.Valid([]byte(input)) {
			fmt.Printf("  error: input is not valid JSON: %s\n", input)
			return
		}
		exec, err := k.CreateExecution(ctx, agentID, json.RawMessage(input), "")
		if err != nil {
			fmt.Println("  error:", err)
			return
		}
		fmt.Printf("  created %s (%s) — 'exec watch %s' to follow\n", shortID(exec.ID), exec.Status, shortID(exec.ID))
	case "get":
		id, err := resolveExecID(ctx, k, fields)
		if err != nil {
			fmt.Println("  error:", err)
			return
		}
		e, err := k.GetExecution(ctx, id)
		if err != nil {
			fmt.Println("  error:", err)
			return
		}
		fmt.Printf("  id       %s\n", e.ID)
		fmt.Printf("  agent    %s\n", e.AgentID)
		fmt.Printf("  status   %s\n", e.Status)
		fmt.Printf("  created  %s (%s ago)\n", e.CreatedAt.Format(time.RFC3339), age(e.CreatedAt))
		if len(e.Output) > 0 {
			fmt.Printf("  output   %s\n", oneLine(e.Output, 200))
		}
		if e.FailureReason != "" {
			fmt.Printf("  failure  %s\n", e.FailureReason)
		}
	case "watch":
		id, err := resolveExecID(ctx, k, fields)
		if err != nil {
			fmt.Println("  error:", err)
			return
		}
		watchExecution(ctx, k, id)
	case "events":
		id, err := resolveExecID(ctx, k, fields)
		if err != nil {
			fmt.Println("  error:", err)
			return
		}
		dumpEvents(ctx, k, id)
	case "cancel":
		id, err := resolveExecID(ctx, k, fields)
		if err != nil {
			fmt.Println("  error:", err)
			return
		}
		if err := k.CancelExecution(ctx, id); err != nil {
			fmt.Println("  error:", err)
			return
		}
		fmt.Printf("  cancelled %s\n", shortID(id))
	default:
		fmt.Printf("  unknown exec subcommand %q (ls, create, get, watch, events, cancel)\n", sub)
	}
}

// dumpEvents prints an execution's entire event log once, with each payload
// pretty-printed in full — unlike 'watch', which tails the log and collapses
// each payload to a single truncated line. It pages through the log so the
// whole history is shown regardless of length.
func dumpEvents(ctx context.Context, k *kernel.Kernel, id uuid.UUID) {
	const batch = 100
	var after int64
	var total int
	for {
		events, err := k.GetEvents(ctx, id, after, batch)
		if err != nil {
			fmt.Println("  error:", err)
			return
		}
		for _, ev := range events {
			fmt.Printf("  [%d] %s\n", ev.EventSeq, ev.Type)
			fmt.Println(indentJSON(ev.Payload))
			after = ev.EventSeq
			total++
		}
		if len(events) < batch {
			break
		}
	}
	fmt.Printf("  --- %d event(s) ---\n", total)
}

// indentJSON pretty-prints a JSON payload indented under its event header,
// falling back to the raw bytes if it is not valid JSON.
func indentJSON(b []byte) string {
	var buf bytes.Buffer
	if err := json.Indent(&buf, b, "      ", "  "); err != nil {
		return "      " + string(b)
	}
	return "      " + buf.String()
}

// watchExecution tails an execution's event log until it reaches a terminal
// status or the kernel shuts down.
func watchExecution(ctx context.Context, k *kernel.Kernel, id uuid.UUID) {
	var after int64
	ticker := time.NewTicker(300 * time.Millisecond)
	defer ticker.Stop()
	for {
		events, err := k.GetEvents(ctx, id, after, 100)
		if err != nil {
			fmt.Println("  error:", err)
			return
		}
		for _, ev := range events {
			fmt.Printf("  [%d] %-26s %s\n", ev.EventSeq, ev.Type, oneLine(ev.Payload, 100))
			after = ev.EventSeq
		}
		if e, err := k.GetExecution(ctx, id); err == nil && e.Status.IsTerminal() {
			fmt.Printf("  --- %s ---\n", e.Status)
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// resolveExecID accepts a full UUID or a unique short-ID prefix (as printed by
// 'exec ls'), so the IDs shown in the REPL can be typed back directly.
func resolveExecID(ctx context.Context, k *kernel.Kernel, fields []string) (uuid.UUID, error) {
	if len(fields) < 3 {
		return uuid.Nil, fmt.Errorf("usage: exec %s <id>", fields[1])
	}
	arg := fields[2]
	if id, err := uuid.Parse(arg); err == nil {
		return id, nil
	}
	page, _ := k.ListExecutions(ctx, domain.ExecutionFilter{Limit: kernel.MaxListExecutionsLimit})
	var matches []uuid.UUID
	for _, e := range page.Executions {
		if strings.HasPrefix(e.ID.String(), arg) {
			matches = append(matches, e.ID)
		}
	}
	switch len(matches) {
	case 1:
		return matches[0], nil
	case 0:
		return uuid.Nil, fmt.Errorf("no execution matching %q", arg)
	default:
		return uuid.Nil, fmt.Errorf("%q is ambiguous (%d matches)", arg, len(matches))
	}
}

func shortID(id uuid.UUID) string { return id.String()[:8] }

func age(t time.Time) string {
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	default:
		return fmt.Sprintf("%dh", int(d.Hours()))
	}
}

// oneLine collapses whitespace and truncates, for showing JSON payloads on a
// single line.
func oneLine(b []byte, max int) string {
	s := strings.Join(strings.Fields(string(b)), " ")
	if len(s) > max {
		s = s[:max] + "…"
	}
	return s
}

func printHelp() {
	fmt.Print(`
commands:
  agent ls                       list registered agents
  agent get <id>                 show an agent and its policy
  agent add <config.yaml>        register agent(s) from a config file
  agent rm <id>                  delete an agent
  exec ls                        list executions (newest first)
  exec create <agent> [json]     start an execution (input defaults to {})
  exec get <id>                  show an execution's status and output
  exec watch <id>                tail an execution's events until it finishes
  exec events <id>               print the full event log with full payloads
  exec cancel <id>               cancel a running execution
  help                           show this help
  quit                           stop the kernel and exit

ids accept a unique short-id prefix (as shown by 'exec ls').
`)
}
