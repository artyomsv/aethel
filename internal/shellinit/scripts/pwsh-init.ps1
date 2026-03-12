# Aethel shell integration — OSC 7 for PowerShell
# Source user's profile (-NoProfile prevents auto-loading)
if (Test-Path $PROFILE.CurrentUserCurrentHost) { . $PROFILE.CurrentUserCurrentHost }

# Override prompt to emit OSC 7
$__aethel_original_prompt = $function:prompt
function prompt {
    $cwd = (Get-Location).Path -replace '\\', '/'
    if ($cwd -match '^[A-Z]:') { $cwd = "/$cwd" }
    $host.UI.Write("`e]7;file://$([System.Net.Dns]::GetHostName())$cwd`e\")
    & $__aethel_original_prompt
}
