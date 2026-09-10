# Quil shell integration — OSC 7 + OSC 133 for bash
# Source user's bashrc (--rcfile replaces normal loading)
if [ -f ~/.bashrc ]; then . ~/.bashrc; fi

# Emit OSC 7 with current working directory after every command
__quil_osc7() { printf '\e]7;file://%s%s\e\\' "${HOSTNAME:-localhost}" "$PWD"; }

# OSC 133: command markers for notification center
#
# D reports a COMMAND's exit, so it is emitted only when preexec saw one since
# the previous prompt. A prompt drawn with nothing run — the first one after
# startup, a bare Enter — emits no D: the daemon reads every D as "the command
# you sent has finished", and a task handed to a fresh shell used to complete
# on the startup prompt.
#
# The DEBUG trap fires for every top-level simple command, INCLUDING each piece
# of PROMPT_COMMAND, so preexec only counts a command while the prompt is
# "armed" — set by __quil_arm, the LAST piece of PROMPT_COMMAND, and cleared by
# the first command after it, which is the user's.
__quil_ran=
__quil_armed=
__quil_precmd() {
    local ec=$?
    if [ -n "$__quil_ran" ]; then
        printf '\e]133;D;%d\e\\' "$ec"
        __quil_ran=
    fi
    printf '\e]133;A\e\\'
}
__quil_arm() { __quil_armed=1; }
__quil_preexec() {
    [ -n "$__quil_armed" ] || return 0
    __quil_armed=
    __quil_ran=1
    printf '\e]133;B\e\\'
}

if [[ "${PROMPT_COMMAND}" != *"__quil_osc7"* ]]; then
    PROMPT_COMMAND="__quil_precmd;__quil_osc7${PROMPT_COMMAND:+;$PROMPT_COMMAND};__quil_arm"
fi
trap '__quil_preexec' DEBUG
