#compdef yagura
# yagura zsh completion
#
# Install:
#   cp yagura-completion.zsh /usr/local/share/zsh/site-functions/_yagura
#   or for current user:
#   mkdir -p ~/.zfunc && cp yagura-completion.zsh ~/.zfunc/_yagura
#   then add `fpath=(~/.zfunc $fpath)` and `autoload -U compinit && compinit` to .zshrc

_yagura() {
    local context state state_descr line
    typeset -A opt_args

    _arguments -C \
        '1: :->command' \
        '*::arg:->args'

    case $state in
        command)
            _values 'yagura subcommand' \
                'verify[Verify audit log hash chain]' \
                'version[Print version and exit]' \
                'help[Show usage]' \
                'secret[Manage encrypted secret store]'
            ;;
        args)
            case $words[1] in
                secret)
                    if (( CURRENT == 2 )); then
                        _values 'secret subcommand' \
                            'set[Encrypt stdin and store]' \
                            'get[Decrypt and print]' \
                            'list[List secret names]' \
                            'delete[Remove a secret]'
                    elif (( CURRENT == 3 )) && [[ $words[2] =~ ^(get|delete)$ ]]; then
                        local secrets_dir="${YAGURA_STATE_DIR:-$HOME/.yagura}/secrets"
                        if [[ -d $secrets_dir ]]; then
                            _path_files -W "$secrets_dir" -g "*.enc(:r)"
                        fi
                    fi
                    ;;
            esac
            ;;
    esac
}

_yagura "$@"
