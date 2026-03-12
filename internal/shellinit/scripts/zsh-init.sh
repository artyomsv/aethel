# Aethel shell integration — OSC 7 for zsh
# Restore ZDOTDIR permanently, source user's .zshrc
if [ -n "${AETHEL_ORIG_ZDOTDIR+x}" ]; then
    ZDOTDIR="${AETHEL_ORIG_ZDOTDIR}"
else
    ZDOTDIR="${HOME}"
fi
[ -f "${ZDOTDIR}/.zshrc" ] && . "${ZDOTDIR}/.zshrc"

# OSC 7 hooks (chpwd fires on cd, precmd fires before each prompt)
__aethel_osc7() { printf '\e]7;file://%s%s\e\\' "${HOST:-localhost}" "${PWD}" }
(( ! ${chpwd_functions[(Ie)__aethel_osc7]} )) && chpwd_functions+=(__aethel_osc7)
(( ! ${precmd_functions[(Ie)__aethel_osc7]} )) && precmd_functions+=(__aethel_osc7)
