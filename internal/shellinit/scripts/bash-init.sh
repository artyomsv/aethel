# Aethel shell integration — OSC 7 for bash
# Source user's bashrc (--rcfile replaces normal loading)
if [ -f ~/.bashrc ]; then . ~/.bashrc; fi

# Emit OSC 7 with current working directory after every command
__aethel_osc7() { printf '\e]7;file://%s%s\e\\' "${HOSTNAME:-localhost}" "$PWD"; }
if [[ "${PROMPT_COMMAND}" != *"__aethel_osc7"* ]]; then
    PROMPT_COMMAND="__aethel_osc7${PROMPT_COMMAND:+;$PROMPT_COMMAND}"
fi
