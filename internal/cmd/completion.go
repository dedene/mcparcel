package cmd

import (
	"io"

	"github.com/dedene/mcparcel/internal/output"
)

// CompletionCmd prints a shell completion script. Each script asks
// mcparcel __complete for its candidates, so completion follows the
// installed version and the current configuration.
type CompletionCmd struct {
	Shell string `arg:"" name:"shell" json:"-" help:"bash, zsh or fish."`
}

// completionData is the JSON form of completion.
type completionData struct {
	Shell  string `json:"shell"`
	Script string `json:"script"`
}

func (c *CompletionCmd) Help() string {
	return `Completes commands, flags, connection IDs and call targets. It reads your configuration offline: it never starts servers or the runtime, and it lists a connection's tools only as far as its configuration names them. mcparcel must be on your PATH.

Install:

    bash  echo 'eval "$(mcparcel completion bash)"' >> ~/.bashrc
    zsh   echo 'source <(mcparcel completion zsh)' >> ~/.zshrc   # after compinit
    fish  mcparcel completion fish > ~/.config/fish/completions/mcparcel.fish`
}

// Run prints the script for the named shell.
func (c *CompletionCmd) Run(s *Streams, opts *CommandOptions) error {
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
