package shell

import (
	"fmt"
	"path"
	"strconv"
	"strings"
)

// selfServiceControlError checks literal direct commands, not arbitrary shell
// programs. It deliberately does not descend into sh -c, scripts or the command
// arguments of systemd-run: a separate maintenance unit must remain usable.
func selfServiceControlError(cgroup string, req Request) error {
	// user@UID.service belongs to the system manager; its descendants
	// belong to that user's manager. The same unit name may exist in both.
	services := make(map[serviceControlUnit]string)
	for _, line := range strings.Split(cgroup, "\n") {
		fields := strings.SplitN(line, ":", 3)
		if len(fields) != 3 || (fields[1] != "" && fields[1] != "name=systemd") {
			continue
		}
		userUID := ""
		for _, part := range strings.Split(fields[2], "/") {
			if strings.HasSuffix(part, ".service") {
				services[serviceControlUnit{name: part, user: userUID != ""}] = userUID
			}
			if strings.HasPrefix(part, "user@") && strings.HasSuffix(part, ".service") {
				uid := strings.TrimSuffix(strings.TrimPrefix(part, "user@"), ".service")
				if _, err := strconv.ParseUint(uid, 10, 32); err == nil {
					userUID = uid
				}
			}
		}
	}
	if len(services) == 0 {
		return nil
	}
	commands := literalShellCommands(req.Cmd)
	if req.Argv != nil {
		command := make([]serviceControlWord, len(req.Argv))
		for i, arg := range req.Argv {
			command[i] = serviceControlWord{text: arg, literal: true}
		}
		commands = [][]serviceControlWord{command}
	}
	for _, command := range commands {
		targets, userManager := directServiceControlTargets(command, req.Argv == nil)
		for _, target := range targets {
			switch path.Ext(target) {
			case ".service", ".socket", ".target", ".device", ".mount", ".automount", ".swap", ".timer", ".path", ".slice", ".scope":
			default:
				target += ".service"
			}
			uid, own := services[serviceControlUnit{name: target, user: userManager}]
			if own && userManager {
				// With no explicit directory use the local user's default. An
				// explicit different runtime directory is not our user manager.
				if runtimeDir := req.Env["XDG_RUNTIME_DIR"]; runtimeDir != "" && path.Clean(runtimeDir) != "/run/user/"+uid {
					continue
				}
			}
			if own {
				return fmt.Errorf("Команда отклонена: остановка собственной службы %s прервёт исполнение команды и может оставить сервис выключенным. Запустите обслуживание отдельной службой: sudo systemd-run --collect --unit=<unique> /bin/bash /absolute/maintenance.sh. Замените <unique> уникальным именем, а путь — абсолютным путём к скрипту; проверьте результат: sudo journalctl -u <unique>.service --no-pager.", target)
			}
		}
	}
	return nil
}

type serviceControlWord struct {
	text    string
	literal bool
}

type serviceControlUnit struct {
	name string
	user bool
}

func directServiceControlTargets(words []serviceControlWord, shellSource bool) ([]string, bool) {
	i := 0
	for shellSource && i < len(words) && serviceControlAssignment(words[i].text) {
		i++ // leading environment assignments
	}
	if i < len(words) && words[i].literal && path.Base(words[i].text) == "sudo" {
		i++
		for i < len(words) && strings.HasPrefix(words[i].text, "-") {
			option := words[i].text
			i++
			if option == "--" {
				break
			}
			switch option {
			case "-u", "--user", "-g", "--group", "-h", "--host", "-p", "--prompt", "-C", "--close-from", "-D", "--chdir", "-R", "--chroot", "-T", "--command-timeout":
				i++
			}
		}
	}
	if i >= len(words) || !words[i].literal || path.Base(words[i].text) != "systemctl" {
		return nil, false
	}
	i++
	action := ""
	var targets []string
	userManager := false // systemctl defaults to the system manager, not XDG
	options := true
	for ; i < len(words); i++ {
		word := words[i]
		if options && strings.HasPrefix(word.text, "-") {
			option := word.text
			if option == "--" {
				options = false
				continue
			}
			// A remote manager cannot stop this process's local service.
			if strings.HasPrefix(option, "-H") || option == "--host" || strings.HasPrefix(option, "--host=") ||
				strings.HasPrefix(option, "-M") || option == "--machine" || strings.HasPrefix(option, "--machine=") {
				return nil, false
			}
			switch option {
			case "--user":
				userManager = true
			case "--system":
				userManager = false
			case "-s", "--signal", "--kill-whom", "--kill-who", "--job-mode", "-p", "--property", "-t", "--type", "--state", "--root", "--image":
				i++
			}
			continue
		}
		if action == "" {
			if !word.literal {
				return nil, false
			}
			switch word.text {
			case "stop", "restart", "try-restart", "kill":
				action = word.text
			default:
				return nil, false
			}
		} else if word.literal {
			targets = append(targets, word.text)
		}
	}
	return targets, userManager
}

func serviceControlAssignment(word string) bool {
	name, _, found := strings.Cut(word, "=")
	if !found || name == "" {
		return false
	}
	for i, c := range name {
		if c != '_' && !(c >= 'a' && c <= 'z') && !(c >= 'A' && c <= 'Z') && !(i > 0 && c >= '0' && c <= '9') {
			return false
		}
	}
	return true
}

// Tokenize only literal simple command lists. Quoted arguments are opaque, so
// mentioning a stop inside an echo or a detached maintenance script is harmless.
// Expansions, compound shell syntax and heredoc bodies are outside this guard.
func literalShellCommands(source string) [][]serviceControlWord {
	var commands [][]serviceControlWord
	var words []serviceControlWord
	var text strings.Builder
	quote := byte(0)
	started, literal := false, true
	ioNumber, redirectionOperand := true, false
	flushWord := func() {
		if started {
			if !redirectionOperand {
				words = append(words, serviceControlWord{text: text.String(), literal: literal})
			}
			redirectionOperand = false
		}
		text.Reset()
		started, literal, ioNumber = false, true, true
	}
	flushCommand := func() {
		flushWord()
		redirectionOperand = false
		if len(words) != 0 {
			commands = append(commands, words)
			words = nil
		}
	}
	for i := 0; i < len(source); i++ {
		c := source[i]
		if c == '\\' && quote != '\'' && i+1 < len(source) {
			next := source[i+1]
			if next == '\n' {
				i++
				continue
			}
			if quote == 0 || strings.ContainsRune("\\\"$", rune(next)) || next == 0x60 {
				text.WriteByte(next)
				started = true
				ioNumber = false
				i++
				continue
			}
		}
		if quote != 0 {
			if c == quote {
				quote = 0
			} else {
				text.WriteByte(c)
				if quote == '"' && (c == '$' || c == 0x60) {
					literal = false
				}
			}
			continue
		}
		if c == '<' || c == '>' || (c == '&' && i+1 < len(source) && source[i+1] == '>') {
			if c == '<' && i+1 < len(source) && source[i+1] == '<' {
				flushCommand()
				return commands // heredoc data remains outside this guard
			}
			if started && ioNumber {
				// Only unquoted digits adjacent to the operator are an fd,
				// rather than a command argument (2>file versus '2'>file).
				text.Reset()
				started = false
			}
			flushWord()
			redirectionOperand = true
			if c == '&' {
				i++ // &> and &>> redirect both output streams
			}
			if i+1 < len(source) {
				next := source[i+1]
				if next == '&' || (source[i] == '>' && (next == '>' || next == '|')) || (source[i] == '<' && next == '>') {
					i++ // >>, >|, <>, >& and <& have one operand too
				}
			}
			continue
		}
		switch c {
		case '\'', '"':
			quote, started = c, true
			ioNumber = false
		case ' ', '\t', '\r':
			flushWord()
		case '\n', ';', '&', '|':
			flushCommand()
		case '#':
			if !started {
				for i+1 < len(source) && source[i+1] != '\n' {
					i++
				}
			} else {
				text.WriteByte(c)
				ioNumber = false
			}
		case '$', 0x60:
			// Expansion values stay opaque, but must not hide a later literal
			// systemctl command in the same list.
			started, literal = true, false
			ioNumber = false
			text.WriteByte(c)
			if c == 0x60 {
				i = serviceControlExpansionEnd(source, i, c, c)
			} else if i+1 < len(source) && source[i+1] == '(' {
				i = serviceControlExpansionEnd(source, i+1, '(', ')')
			} else if i+1 < len(source) && source[i+1] == '{' {
				i = serviceControlExpansionEnd(source, i+1, '{', '}')
			}
		case '(', ')':
			// Avoid treating a nested program's lines as direct commands.
			started, literal = true, false
			flushCommand()
			return commands
		default:
			text.WriteByte(c)
			started = true
			ioNumber = ioNumber && c >= '0' && c <= '9'
		}
	}
	if quote == 0 {
		flushCommand()
	}
	return commands
}

// Skip expansion data without interpreting any contained command as direct.
func serviceControlExpansionEnd(source string, start int, open, close byte) int {
	depth, quote := 1, byte(0)
	for i := start + 1; i < len(source); i++ {
		c := source[i]
		if c == '\\' && quote != '\'' {
			i++
			continue
		}
		if open == close {
			if c == close {
				return i
			}
			continue
		}
		if quote != 0 {
			if c == quote {
				quote = 0
			}
			continue
		}
		if c == '\'' || c == '"' {
			quote = c
		} else if c == open {
			depth++
		} else if c == close {
			depth--
			if depth == 0 {
				return i
			}
		}
	}
	return len(source)
}
