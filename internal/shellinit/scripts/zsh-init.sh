# Quil shell integration — OSC 7 + OSC 133 for zsh
# Restore ZDOTDIR permanently, source user's .zshrc
if [ -n "${QUIL_ORIG_ZDOTDIR+x}" ]; then
    ZDOTDIR="${QUIL_ORIG_ZDOTDIR}"
else
    ZDOTDIR="${HOME}"
fi
[ -f "${ZDOTDIR}/.zshrc" ] && . "${ZDOTDIR}/.zshrc"

# OSC 7 hooks (chpwd fires on cd)
__quil_osc7() { printf '\e]7;file://%s%s\e\\' "${HOST:-localhost}" "${PWD}" }
(( ${chpwd_functions[(Ie)__quil_osc7]:-0} )) || chpwd_functions+=(__quil_osc7)

# OSC 133: command markers for notification center
# precmd must capture $? immediately before any other function clobbers it
#
# D reports a COMMAND's exit, so it is emitted only when preexec saw one since
# the previous prompt (zsh's preexec fires for user commands only). A prompt
# drawn with nothing run — the first one after startup, a bare Enter — emits
# no D: the daemon reads every D as "the command you sent has finished", and
# a task handed to a fresh shell used to complete on the startup prompt.
__quil_ran=
__quil_precmd() {
    local ec=$?
    if [ -n "$__quil_ran" ]; then
        printf '\e]133;D;%d\e\\' "$ec"
        __quil_ran=
    fi
    printf '\e]133;A\e\\'
}
__quil_preexec() { __quil_ran=1; printf '\e]133;B\e\\'; }
# Insert precmd FIRST (before osc7) so $? is captured before osc7 runs
(( ${precmd_functions[(Ie)__quil_precmd]:-0} )) || precmd_functions=(__quil_precmd $precmd_functions)
(( ${precmd_functions[(Ie)__quil_osc7]:-0} )) || precmd_functions+=(__quil_osc7)
(( ${preexec_functions[(Ie)__quil_preexec]:-0} )) || preexec_functions+=(__quil_preexec)
