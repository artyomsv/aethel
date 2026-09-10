# Quil shell integration — OSC 7 + OSC 133 for PowerShell
# Source user's profile (-NoProfile prevents auto-loading)
if (Test-Path $PROFILE.CurrentUserCurrentHost) { . $PROFILE.CurrentUserCurrentHost }

# Override prompt to emit OSC 7 + OSC 133 command markers
# Use [char]0x1b for ESC — compatible with both PowerShell 7+ and Windows PowerShell 5.1
$__quil_esc = [char]0x1b
$__quil_original_prompt = $function:prompt
# Id of the last history entry seen at a prompt. OSC 133;D reports a COMMAND's
# exit, so it is emitted only when a command ran since the previous prompt —
# the history id moved. A prompt drawn with nothing run (the first one after
# startup, or a bare Enter) emits no D: the daemon reads every D as "the
# command you sent has finished", and a task handed to a fresh shell used to
# complete on the startup prompt.
$global:__quil_hist = 0
function prompt {
    $ec = $LASTEXITCODE; if ($null -eq $ec) { $ec = 0 }
    $__quil_last = Get-History -Count 1
    $__quil_id = if ($__quil_last) { $__quil_last.Id } else { 0 }
    if ($__quil_id -ne $global:__quil_hist) {
        # OSC 133;D — report previous command exit code
        $host.UI.Write("$__quil_esc]133;D;$ec$__quil_esc\")
        $global:__quil_hist = $__quil_id
    }
    # OSC 133;A — prompt start
    $host.UI.Write("$__quil_esc]133;A$__quil_esc\")
    # OSC 7 — current working directory
    $cwd = (Get-Location).Path -replace '\\', '/'
    if ($cwd -match '^[A-Z]:') { $cwd = "/$cwd" }
    $host.UI.Write("$__quil_esc]7;file://$([System.Net.Dns]::GetHostName())$cwd$__quil_esc\")
    & $__quil_original_prompt
}
