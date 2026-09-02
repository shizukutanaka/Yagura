# yagura bash completion
#
# Install:
#   sudo cp yagura-completion.bash /etc/bash_completion.d/yagura
#   or for current user:
#   source ./yagura-completion.bash >> ~/.bashrc
#
# Provides tab-completion for subcommands and common flags.

_yagura() {
    local cur prev opts subcommand
    COMPREPLY=()
    cur="${COMP_WORDS[COMP_CWORD]}"
    prev="${COMP_WORDS[COMP_CWORD-1]}"
    subcommand="${COMP_WORDS[1]}"

    # Top-level subcommands
    local subcmds="verify version help secret"

    if [[ $COMP_CWORD -eq 1 ]]; then
        COMPREPLY=( $(compgen -W "${subcmds}" -- "${cur}") )
        return 0
    fi

    # `yagura secret <subsubcommand>`
    if [[ "${subcommand}" == "secret" ]]; then
        if [[ $COMP_CWORD -eq 2 ]]; then
            COMPREPLY=( $(compgen -W "set get list delete" -- "${cur}") )
            return 0
        fi
        # `yagura secret get/set/delete <name>` — complete from existing secret names
        if [[ $COMP_CWORD -eq 3 ]] && [[ "${COMP_WORDS[2]}" =~ ^(get|delete)$ ]]; then
            local secrets_dir="${YAGURA_STATE_DIR:-$HOME/.yagura}/secrets"
            if [[ -d "${secrets_dir}" ]]; then
                local names
                names=$(ls "${secrets_dir}" 2>/dev/null | sed -n 's/\.enc$//p')
                COMPREPLY=( $(compgen -W "${names}" -- "${cur}") )
            fi
            return 0
        fi
    fi

    return 0
}

complete -F _yagura yagura
