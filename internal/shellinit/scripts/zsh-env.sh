# Aethel shell integration — zsh environment bootstrap
# Save our ZDOTDIR, restore original so user's .zshenv is found
AETHEL_ZDOTDIR="${ZDOTDIR}"
if [ -n "${AETHEL_ORIG_ZDOTDIR+x}" ]; then
    ZDOTDIR="${AETHEL_ORIG_ZDOTDIR}"
else
    ZDOTDIR="${HOME}"
fi
[ -f "${ZDOTDIR}/.zshenv" ] && . "${ZDOTDIR}/.zshenv"
ZDOTDIR="${AETHEL_ZDOTDIR}"
