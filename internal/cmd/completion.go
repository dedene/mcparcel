package cmd

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/dedene/mcparcel/internal/output"
)

// CompletionCmd prints a shell completion script. Each script asks
// mcparcel __complete for its candidates, so completion follows the
// installed version and the current configuration. Without a shell it says
// how to set up the one $SHELL names.
type CompletionCmd struct {
	Shell string `arg:"" optional:"" name:"shell" json:"-" help:"bash, zsh or fish. Leave it out to see how to set up your shell."`
}

// completionData is the JSON form of completion.
type completionData struct {
	Shell  string `json:"shell"`
	Script string `json:"script"`
}

// completionSetupData is the JSON form of completion without a shell: the
// shell $SHELL names when mcparcel completes it, and the setup lines.
type completionSetupData struct {
	Shell string            `json:"shell,omitempty"`
	Setup []completionSetup `json:"setup"`
}

type completionSetup struct {
	Shell   string `json:"shell"`
	Command string `json:"command"`
}

// completionSetups is the one line that loads each shell's script.
var completionSetups = []completionSetup{
	{"bash", `echo 'eval "$(mcparcel completion bash)"' >> ~/.bashrc`},
	{"zsh", `echo 'source <(mcparcel completion zsh)' >> ~/.zshrc   # after compinit`},
	{"fish", `mcparcel completion fish > ~/.config/fish/completions/mcparcel.fish`},
}

// completionTerminal is stdoutTerminal; tests replace it.
var completionTerminal = stdoutTerminal

func (c *CompletionCmd) Help() string {
	return `Completes commands, flags, connection IDs and call targets. It reads your configuration offline: it never starts servers or the runtime, and it lists a connection's tools only as far as its configuration names them. mcparcel must be on your PATH.

Install:

` + setupLines(completionSetups)
}

// Run prints the script for the named shell, or without one how to set up
// completion.
func (c *CompletionCmd) Run(s *Streams, opts *CommandOptions) error {
	if c.Shell == "" {
		return completionSetupHint(s, opts, filepath.Base(os.Getenv("SHELL")))
	}
	script, ok := completionScripts[c.Shell]
	if !ok {
		return output.UsageError("completion", "Unknown shell: choose bash, zsh or fish.", "<shell>", "")
	}
	if opts.JSON {
		return writeSuccess(s, opts, completionData{Shell: c.Shell, Script: script})
	}
	if _, err := io.WriteString(s.Out, script); err != nil {
		return &commandWriteFailure{err}
	}
	return nil
}

// completionSetupHint answers completion without a shell: on a terminal,
// the setup line for the login shell (every line when mcparcel does not
// complete it). In a pipe or $(...) the hint would be read as the script,
// so it stays the missing-argument error, naming the shell to pass. The
// shell is shown only when it is one of the three.
func completionSetupHint(s *Streams, opts *CommandOptions, login string) error {
	setups := completionSetups
	shell := ""
	for _, setup := range completionSetups {
		if setup.Shell == login {
			setups, shell = []completionSetup{setup}, login
		}
	}
	if opts.JSON {
		return writeSuccess(s, opts, completionSetupData{Shell: shell, Setup: setups})
	}
	if !completionTerminal(s.Out) {
		err := output.UsageError("completion", "Missing argument <shell>.", "<shell>", "")
		err.NextAction = "Run mcparcel completion bash, zsh or fish."
		if shell != "" {
			err.NextAction = "Run mcparcel completion " + shell + "."
		}
		return err
	}
	var text string
	if shell != "" {
		text = fmt.Sprintf("Set up %s completion once with:\n\n    %s\n\nOther shells: mcparcel completion --help.\n", shell, setups[0].Command)
	} else {
		text = "mcparcel completes bash, zsh and fish. Set up yours once with:\n\n" + setupLines(setups) + "\n"
	}
	if _, err := io.WriteString(s.Out, text); err != nil {
		return &commandWriteFailure{err}
	}
	return nil
}

// setupLines lists setups as indented "shell  command" lines.
func setupLines(setups []completionSetup) string {
	var b strings.Builder
	for i, setup := range setups {
		if i > 0 {
			b.WriteString("\n")
		}
		fmt.Fprintf(&b, "    %-4s  %s", setup.Shell, setup.Command)
	}
	return b.String()
}

// A candidate ending in "." (a call target's connection) or "=" takes no
// space after it, so the next keystroke continues the same word.
var completionScripts = map[string]string{
	"bash": `# bash completion for mcparcel: eval "$(mcparcel completion bash)"
_mcparcel() {
    local cur words cword line i
    if declare -F _get_comp_words_by_ref >/dev/null 2>&1; then
        _get_comp_words_by_ref -n =: cur words cword
    else
        # Without bash-completion, split the line on blanks only, so a
        # canonical ID keeps its colon.
        line="${COMP_LINE:0:COMP_POINT}"
        read -ra words <<< "$line"
        if [[ -z $line || $line == *[[:space:]] ]]; then
            words+=("")
        fi
        cword=$(( ${#words[@]} - 1 ))
        cur="${words[cword]}"
    fi
    COMPREPLY=()
    while IFS= read -r line; do
        COMPREPLY+=("$line")
    done < <(command mcparcel __complete "${words[@]:1:cword-1}" "$cur" 2>/dev/null)
    if [[ ${#COMPREPLY[@]} -eq 1 ]]; then
        if [[ ${COMPREPLY[0]} == *[.=] ]]; then
            compopt -o nospace 2>/dev/null
        elif ! type compopt >/dev/null 2>&1; then
            # Bash 3.2 has no compopt and is registered with nospace, so
            # the space goes in by hand.
            COMPREPLY[0]+=" "
        fi
    fi
    # Bash replaces only the part of the word after its last colon.
    if [[ $cur == *:* && $COMP_WORDBREAKS == *:* ]]; then
        local prefix="${cur%"${cur##*:}"}"
        for i in "${!COMPREPLY[@]}"; do
            COMPREPLY[i]="${COMPREPLY[i]#"$prefix"}"
        done
    fi
}
if type compopt >/dev/null 2>&1; then
    complete -o default -F _mcparcel mcparcel
else
    complete -o nospace -o default -F _mcparcel mcparcel
fi
`,
	"zsh": `#compdef mcparcel
# zsh completion for mcparcel: source <(mcparcel completion zsh), or save
# it as _mcparcel in a directory on $fpath.
compdef _mcparcel mcparcel

_mcparcel() {
    local -a candidates spaced joined
    local candidate
    candidates=("${(@f)$(command mcparcel __complete "${(@)words[2,CURRENT-1]}" "${words[CURRENT]}" 2>/dev/null)}")
    candidates=(${candidates:#})
    if (( ${#candidates} == 0 )); then
        _files
        return
    fi
    for candidate in "${candidates[@]}"; do
        if [[ $candidate == *[.=] ]]; then
            joined+=("$candidate")
        else
            spaced+=("$candidate")
        fi
    done
    (( ${#spaced} )) && compadd -- "${spaced[@]}"
    (( ${#joined} )) && compadd -S '' -- "${joined[@]}"
    return 0
}

if [[ "$funcstack[1]" == "_mcparcel" ]]; then
    _mcparcel "$@"
fi
`,
	"fish": `# fish completion for mcparcel: mcparcel completion fish > ~/.config/fish/completions/mcparcel.fish
function __mcparcel_complete
    set -l words (commandline -opc)
    set -e words[1]
    set -l current (commandline -ct)
    set -l candidates (command mcparcel __complete $words "$current" 2>/dev/null)
    if test (count $candidates) -eq 0
        __fish_complete_path "$current"
        return
    end
    printf '%s\n' $candidates
end
complete -c mcparcel -f -a '(__mcparcel_complete)'
`,
}
